package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	chatpg "github.com/siddhantdubey/chatmem/internal/pg"
	"github.com/siddhantdubey/chatmem/internal/store"
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
