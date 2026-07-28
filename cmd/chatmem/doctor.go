package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/sid077/chatmem/internal/telemetry"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Print a self-diagnostic report of chatmem's install + runtime state",
		RunE: func(cmd *cobra.Command, args []string) error {
			runDoctor()
			return nil
		},
	}
}

func runDoctor() {
	fmt.Printf("chatmem %s   (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	if bin, err := os.Executable(); err == nil {
		fmt.Printf("binary:   %s\n", bin)
	}
	fmt.Println()

	fmt.Println("── environment ──")
	fmt.Printf("HOME:     %s\n", os.Getenv("HOME"))
	fmt.Printf("EUID:     %d\n", os.Geteuid())
	fmt.Printf("data:     %s\n", dataHome())
	fmt.Printf("cache:    %s\n", cacheHome())
	fmt.Println()

	fmt.Println("── checks ──")
	check("not running as root", requireNonRoot)
	check("HOME writable + owned by current user", preflight)
	check("data dir writable", func() error {
		return probeMkdir(dataHome())
	})
	check("cache dir writable", func() error {
		return probeMkdir(cacheHome())
	})
	check(fmt.Sprintf("default port %d is free", defaultPort), func() error {
		return probePort(defaultPort)
	})
	fmt.Println()

	// Telemetry state
	st, err := telemetry.Load(dataHome())
	if err != nil {
		fmt.Printf("telemetry: load failed (%v)\n", err)
	} else {
		state := "disabled"
		if st.Enabled {
			state = "enabled"
		}
		fmt.Println("── telemetry ──")
		fmt.Printf("state:       %s (source: %s)\n", state, st.Source)
		fmt.Printf("install_id:  %s\n", st.InstallID)

		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		client := telemetry.NewClient(st, dataHome(), log, telemetry.Options{Version: version})
		url := client.IngestURL()
		switch {
		case url == "":
			fmt.Println("ingest_url:  (unset — local-only mode)")
		case os.Getenv("CHATMEM_TELEMETRY_URL") != "":
			fmt.Printf("ingest_url:  %s   (from CHATMEM_TELEMETRY_URL)\n", url)
		default:
			fmt.Printf("ingest_url:  %s   (baked into this binary)\n", url)
		}
		if url != "" && st.Enabled {
			check("ingest reachable", func() error { return probeURL(url) })
		}
		pending, _ := client.PendingFiles()
		fmt.Printf("pending payloads: %d\n", len(pending))
		if len(pending) > 0 {
			fmt.Printf("  (list with: chatmem telemetry dump)\n")
		}
	}
}

func check(label string, fn func() error) {
	if err := fn(); err != nil {
		fmt.Printf("  ✗ %s\n     %v\n", label, err)
		return
	}
	fmt.Printf("  ✓ %s\n", label)
}

func probeMkdir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".chatmem-probe")
	f, err := os.Create(probe)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(probe)
}

func probePort(port uint32) error {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("port %d busy: %w", port, err)
	}
	return l.Close()
}

func probeURL(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

