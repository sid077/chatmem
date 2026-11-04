package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	chatmcp "github.com/sid077/chatmem/internal/mcp"
	"github.com/sid077/chatmem/internal/notion"
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
	if err := preflight(); err != nil {
		return err
	}
	// MCP protocol uses stdout — log to stderr.
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dataDir := filepath.Join(dataHome(), "pgdata")
	runtimeDir := filepath.Join(cacheHome(), "pg-runtime")
	log.Info("chatmem mcp starting", "dataDir", dataDir, "port", port)

	// If another chatmem process already has PG up on this port (common when
	// an MCP client restarts us in the middle of the previous instance's
	// shutdown), attach to it instead of trying to start a second one. Two
	// concurrent bootstraps on the same pg-runtime dir were racing and
	// corrupting the extraction — the internal/pg lock fixes that path but
	// attaching is still the friendlier UX (no cold-start wait).
	pgCfg := chatpg.Config{
		DataDir:    dataDir,
		RuntimeDir: runtimeDir,
		Port:       port,
	}
	var sup *pgSupervisor
	if p, ok := tryAttachPostgres(ctx, port); ok {
		sup = newPGSupervisor(pgCfg, p, nil, log)
		log.Info("attached to already-running postgres", "port", port)
	} else {
		pg := chatpg.New(pgCfg)
		if err := pg.Start(ctx); err == nil {
			sup = newPGSupervisor(pgCfg, pg.Pool(), pg, log)
		} else if p, ok := attachAfterLostRace(ctx, port, err); ok {
			// Another chatmem won the bootstrap lock and booted PG while we
			// waited; use theirs.
			sup = newPGSupervisor(pgCfg, p, nil, log)
			log.Info("attached to postgres started concurrently", "port", port)
		} else {
			return fmt.Errorf("start postgres: %w", err)
		}
	}
	pool := sup.pool
	defer func() {
		if err := sup.stop(); err != nil {
			log.Error("stop postgres", "err", err)
		}
	}()

	st := store.New(pool)
	if err := st.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("schema: %w", err)
	}

	tstate, err := telemetry.Load(dataHome())
	if err != nil {
		return fmt.Errorf("load telemetry state: %w", err)
	}
	tclient := telemetry.NewClient(tstate, dataHome(), log, telemetry.Options{Version: version})
	log.Info("telemetry", "enabled", tstate.Enabled, "source", tstate.Source)

	// Notion writer — nil if notion.json is not present (integration disabled).
	notionWriter, err := notion.NewWriter(dataHome(), "")
	if err != nil {
		return fmt.Errorf("load notion config: %w", err)
	}
	if notionWriter != nil {
		notionWriter.OnPublish(publishHook(st))
		log.Info("notion", "enabled", true, "parent", notionWriter.Config().ParentPageID)
	} else {
		log.Info("notion", "enabled", false, "hint", "run `chatmem notion connect <token> --parent <page-id>` to enable")
	}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tclient.Start(ctx) // final flush fires on ctx.Done()
	go sup.watch(ctx, time.Second)

	// Drain any queued notion writes on startup so we don't sit on failures.
	if notionWriter != nil {
		go func() {
			drained, still, err := notionWriter.DrainPending(ctx)
			if err != nil {
				log.Warn("notion drain-pending", "err", err)
			} else if drained > 0 || still > 0 {
				log.Info("notion drained pending", "drained", drained, "still_pending", still)
			}
		}()
	}

	log.Info("mcp ready — serving stdio")
	server := chatmcp.NewServer(chatmcp.Deps{
		Store:        st,
		Aggregator:   tclient.Aggregator(),
		NotionWriter: notionWriter,
		Version:      version,
	})
	return server.Run(ctx, &sdk.StdioTransport{})
}

// tryAttachPostgres returns a pool if a chatmem-compatible Postgres is
// already listening on 127.0.0.1:<port>. Returns ok=false if not. Never
// blocks longer than ~2s so cold startup isn't delayed when there's
// no existing server.
func tryAttachPostgres(ctx context.Context, port uint32) (*pgxpool.Pool, bool) {
	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable&connect_timeout=2", port)
	attachCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pool, err := pgxpool.New(attachCtx, dsn)
	if err != nil {
		return nil, false
	}
	if err := pool.Ping(attachCtx); err != nil {
		pool.Close()
		return nil, false
	}
	return pool, true
}

// publishHook records pages written by a drained pending retry, so the DB
// knows the page id and the next synthesis updates it instead of creating
// a duplicate.
func publishHook(st *store.Store) notion.PublishHook {
	return func(ctx context.Context, conversationID string, sessionType notion.SessionType, out notion.SynthesizeOut) error {
		id, err := uuid.Parse(conversationID)
		if err != nil {
			return fmt.Errorf("pending write has invalid conversation id %q: %w", conversationID, err)
		}
		if out.PageID == "" || out.Skipped {
			return nil
		}
		return st.RecordPublishedPage(ctx, store.RecordSynthesisIn{
			ConversationID: id,
			PageID:         out.PageID,
			PageURL:        out.URL,
			SessionType:    string(sessionType),
			SummaryHash:    out.SummaryHash,
		})
	}
}

// attachAfterLostRace retries the attach when Start failed only because
// the port was taken in the meantime.
func attachAfterLostRace(ctx context.Context, port uint32, startErr error) (*pgxpool.Pool, bool) {
	if !errors.Is(startErr, chatpg.ErrPortInUse) {
		return nil, false
	}
	return tryAttachPostgres(ctx, port)
}
