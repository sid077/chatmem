package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	chatmcp "github.com/sid077/chatmem/internal/mcp"
	chatpg "github.com/sid077/chatmem/internal/pg"
	"github.com/sid077/chatmem/internal/store"
	"github.com/sid077/chatmem/internal/telemetry"
)

func newMCPCmd() *cobra.Command {
	var port uint32
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve stdio MCP (self-contained: starts and manages embedded Postgres for this process)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMCP(cmd.Context(), port)
		},
	}
	cmd.Flags().Uint32Var(&port, "port", defaultPort, "postgres listen port")
	return cmd
}

func runMCP(ctx context.Context, port uint32) error {
	if err := requireNonRoot(); err != nil {
		return err
	}
	// MCP protocol uses stdout — log to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dataDir := filepath.Join(dataHome(), "pgdata")
	runtimeDir := filepath.Join(cacheHome(), "pg-runtime")
	log.Info("chatmem mcp starting", "dataDir", dataDir, "port", port)

	pg := chatpg.New(chatpg.Config{
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		Port:       port,
	})
	if err := pg.Start(ctx); err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	defer func() {
		if err := pg.Stop(); err != nil {
			log.Error("stop postgres", "err", err)
		}
	}()

	st := store.New(pg.Pool())
	if err := st.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("schema: %w", err)
	}

	tstate, err := telemetry.Load(dataHome())
	if err != nil {
		return fmt.Errorf("load telemetry state: %w", err)
	}
	tclient := telemetry.NewClient(tstate, dataHome(), log, telemetry.Options{Version: version})
	log.Info("telemetry", "enabled", tstate.Enabled, "source", tstate.Source)

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tclient.Start(ctx) // final flush fires on ctx.Done()

	log.Info("mcp ready — serving stdio")
	server := chatmcp.NewServer(st, tclient.Aggregator(), version)
	return server.Run(ctx, &sdk.StdioTransport{})
}
