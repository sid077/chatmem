package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	chatpg "github.com/sid077/chatmem/internal/pg"
	"github.com/sid077/chatmem/internal/store"
	"github.com/sid077/chatmem/internal/telemetry"
)

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Provision data dir, install pgvector, apply schema, print MCP client config",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd.Context())
		},
	}
}

func runInit(ctx context.Context) error {
	if err := requireNonRoot(); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dataDir := filepath.Join(dataHome(), "pgdata")
	runtimeDir := filepath.Join(cacheHome(), "pg-runtime")
	log.Info("provisioning", "dataDir", dataDir, "runtimeDir", runtimeDir)

	pg := chatpg.New(chatpg.Config{
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		Port:       defaultPort,
	})
	if err := pg.Start(ctx); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}

	st := store.New(pg.Pool())
	if err := st.EnsureSchema(ctx); err != nil {
		_ = pg.Stop()
		return fmt.Errorf("apply schema: %w", err)
	}

	if err := pg.Stop(); err != nil {
		return fmt.Errorf("stop postgres: %w", err)
	}

	// First-run telemetry consent. Only prompt if no config exists yet.
	// Non-TTY (Docker, CI, ssh-piped): print the notice and leave the default.
	if !telemetry.ConfigExists(dataHome()) {
		promptFirstRunTelemetry(dataHome())
	}

	bin, err := os.Executable()
	if err != nil {
		bin = "chatmem"
	}

	fmt.Println()
	fmt.Println("chatmem is ready.")
	fmt.Printf("Data dir:    %s\n", dataDir)
	fmt.Printf("Runtime dir: %s\n", runtimeDir)
	fmt.Println()
	fmt.Println("To register with Claude Code, add this to ~/.claude/mcp.json:")
	fmt.Println()
	fmt.Printf(`{
  "mcpServers": {
    "chatmem": {
      "command": "%s",
      "args": ["mcp"]
    }
  }
}`, bin)
	fmt.Println()
	fmt.Println()
	fmt.Println("For Cursor, add the same block to ~/.cursor/mcp.json.")
	fmt.Println("Restart the client and the chatmem tools (record_message, search_history,")
	fmt.Println("get_conversation) will appear.")
	return nil
}

func promptFirstRunTelemetry(dataDir string) {
	fmt.Println()
	fmt.Println("── anonymous telemetry ──")
	fmt.Println("Would help me know chatmem is being used. Sent (roughly daily):")
	fmt.Println("  • install UUID (random, generated on this machine)")
	fmt.Println("  • version + counts of tool calls (captures / searches / gets / errors)")
	fmt.Println("  • latency p50/p95/p99 per tool")
	fmt.Println("  • model + client id distribution (e.g. \"claude-opus-4-7: 12, gpt-5: 4\")")
	fmt.Println("Never sent: message content, query strings, filenames, prompts.")
	fmt.Println("Change anytime: chatmem telemetry {enable|disable}, or CHATMEM_TELEMETRY=0.")
	fmt.Println()

	if !isTerminal(os.Stdin) {
		fmt.Println("(non-interactive shell — leaving default: enabled)")
		fmt.Println("To opt out non-interactively:")
		fmt.Println("  chatmem telemetry disable")
		fmt.Println("  # or export CHATMEM_TELEMETRY=0")
		return
	}

	fmt.Print("Enable telemetry? [Y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.TrimSpace(strings.ToLower(line))
	enabled := answer == "" || strings.HasPrefix(answer, "y")
	if err := telemetry.SetEnabled(dataDir, enabled); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist telemetry preference: %v\n", err)
		return
	}
	if enabled {
		fmt.Println("telemetry: enabled (thanks!)")
	} else {
		fmt.Println("telemetry: disabled")
	}
}

func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
