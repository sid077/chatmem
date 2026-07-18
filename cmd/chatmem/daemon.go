package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"

	chatpg "github.com/siddhantdubey/chatmem/internal/pg"
)

const defaultPort uint32 = 54329

func newDaemonCmd() *cobra.Command {
	var port uint32
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived local daemon (manages Postgres + serves MCP)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd.Context(), port)
		},
	}
	cmd.Flags().Uint32Var(&port, "port", defaultPort, "postgres listen port")
	return cmd
}

func runDaemon(ctx context.Context, port uint32) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dataDir := filepath.Join(dataHome(), "pgdata")
	runtimeDir := filepath.Join(cacheHome(), "pg-runtime")
	log.Info("chatmem daemon starting", "dataDir", dataDir, "runtimeDir", runtimeDir, "port", port)

	pg := chatpg.New(chatpg.Config{
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		Port:       port,
	})
	if err := pg.Start(ctx); err != nil {
		return err
	}
	log.Info("postgres ready", "dsn", pg.DSN())

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()

	log.Info("shutting down")
	if err := pg.Stop(); err != nil {
		log.Error("stop postgres", "err", err)
		return err
	}
	return nil
}

func dataHome() string {
	if v := os.Getenv("CHATMEM_HOME"); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "chatmem")
		}
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "chatmem")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "chatmem")
	}
	return filepath.Join(home, ".local", "share", "chatmem")
}

func cacheHome() string {
	if v := os.Getenv("CHATMEM_CACHE"); v != "" {
		return v
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "chatmem-cache")
	}
	return filepath.Join(cache, "chatmem")
}

