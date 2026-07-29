package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/sid077/chatmem/internal/notion"
	chatpg "github.com/sid077/chatmem/internal/pg"
	"github.com/sid077/chatmem/internal/store"
)

func newNotionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notion",
		Short: "Manage the Notion integration (publish conversations as study/debug pages)",
	}
	cmd.AddCommand(
		notionConnectCmd(),
		notionStatusCmd(),
		notionDisconnectCmd(),
		notionListCmd(),
		notionResyncCmd(),
		notionSampleCmd(),
	)
	return cmd
}

func notionConnectCmd() *cobra.Command {
	var parent, workspace string
	var idle, threshold, minMsgs int
	var noAuto bool
	cmd := &cobra.Command{
		Use:   "connect <integration-token>",
		Short: "Save a Notion integration token and set the parent page for new synthesized pages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonRoot(); err != nil {
				return err
			}
			if err := preflight(); err != nil {
				return err
			}
			if parent == "" {
				return fmt.Errorf("--parent is required (paste the page id or share URL of the Notion page you want chatmem to create sub-pages under)")
			}
			parent = normalizeNotionID(parent)
			token := strings.TrimSpace(args[0])
			cfg := notion.Config{
				IntegrationToken: token,
				ParentPageID:     parent,
				WorkspaceID:      workspace,
				ConnectedAt:      time.Now().UTC(),
				AutoSynthesize: notion.AutoSynthesizeCfg{
					Enabled:          !noAuto,
					IdleMinutes:      idle,
					MessageThreshold: threshold,
					MinMessages:      minMsgs,
				},
			}
			if cfg.AutoSynthesize.IdleMinutes == 0 && cfg.AutoSynthesize.MessageThreshold == 0 {
				cfg.AutoSynthesize = notion.DefaultAuto()
				if noAuto {
					cfg.AutoSynthesize.Enabled = false
				}
			}
			// Ping first so we save a working token, not a typo'd one.
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			client := notion.NewClient(token, "")
			user, err := client.Ping(ctx)
			if err != nil {
				return fmt.Errorf("notion API rejected the token: %w\nHINT: create an internal integration at https://www.notion.so/my-integrations, copy the secret, and share the target parent page with the integration", err)
			}
			if err := notion.SaveConfig(dataHome(), cfg); err != nil {
				return err
			}
			fmt.Printf("connected as %s (%s)\n", user.Name, user.ID)
			fmt.Printf("parent page: %s\n", parent)
			if cfg.AutoSynthesize.Enabled {
				fmt.Printf("auto-synthesize: enabled (every %d messages; %d-min idle)\n",
					cfg.AutoSynthesize.MessageThreshold, cfg.AutoSynthesize.IdleMinutes)
			} else {
				fmt.Println("auto-synthesize: disabled (LLM must call synthesize_to_notion explicitly)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&parent, "parent", "", "parent Notion page id (or share URL) — required")
	cmd.Flags().StringVar(&workspace, "workspace", "", "optional Notion workspace id")
	cmd.Flags().IntVar(&idle, "idle-minutes", 10, "auto-synthesize after this many minutes with no new messages")
	cmd.Flags().IntVar(&threshold, "message-threshold", 20, "auto-synthesize after this many messages since last synth")
	cmd.Flags().IntVar(&minMsgs, "min-messages", 4, "never synthesize conversations shorter than this")
	cmd.Flags().BoolVar(&noAuto, "no-auto", false, "disable auto-synthesize (LLM must call synthesize_to_notion explicitly)")
	return cmd
}

func notionStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show notion integration status: token health, parent, auto-synth settings, pending writes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := notion.LoadConfig(dataHome())
			if err != nil {
				return err
			}
			if cfg == nil {
				fmt.Println("notion: not connected")
				fmt.Println("Run: chatmem notion connect <token> --parent <page-id>")
				return nil
			}
			fmt.Printf("notion: connected since %s\n", cfg.ConnectedAt.Format(time.RFC3339))
			fmt.Printf("parent page: %s\n", cfg.ParentPageID)
			fmt.Printf("auto-synthesize: enabled=%v idle_minutes=%d message_threshold=%d min_messages=%d\n",
				cfg.AutoSynthesize.Enabled, cfg.AutoSynthesize.IdleMinutes,
				cfg.AutoSynthesize.MessageThreshold, cfg.AutoSynthesize.MinMessages)

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			client := notion.NewClient(cfg.IntegrationToken, "")
			user, err := client.Ping(ctx)
			if err != nil {
				fmt.Printf("token check: ✗ %v\n", err)
			} else {
				fmt.Printf("token check: ✓ %s (%s)\n", user.Name, user.ID)
			}

			w, err := notion.NewWriter(dataHome(), "")
			if err == nil && w != nil {
				if n, err := w.PendingCount(); err == nil && n > 0 {
					fmt.Printf("pending writes: %d (retried on next chatmem mcp start or chatmem notion resync)\n", n)
				} else {
					fmt.Println("pending writes: 0")
				}
			}
			return nil
		},
	}
}

func notionDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Remove notion.json (revokes the token from chatmem — does not delete published pages)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := notion.DeleteConfig(dataHome()); err != nil {
				return err
			}
			fmt.Println("notion: disconnected (existing pages on Notion are untouched)")
			return nil
		},
	}
}

func notionListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List conversations that have been published to Notion",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonRoot(); err != nil {
				return err
			}
			if err := preflight(); err != nil {
				return err
			}
			s, cleanup, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			pages, err := s.ListNotionPages(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(pages) == 0 {
				fmt.Println("no synthesized pages yet")
				return nil
			}
			for _, p := range pages {
				fmt.Printf("%s  [%s]  %s\n  %s\n  synthesized %s\n\n",
					p.ConversationID, p.SessionType, p.Model, p.URL,
					p.SynthesizedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "max pages to show")
	return cmd
}

func notionResyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resync",
		Short: "Retry any queued Notion writes that previously failed (drains <data>/notion-pending)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := notion.NewWriter(dataHome(), "")
			if err != nil {
				return err
			}
			if w == nil {
				return fmt.Errorf("notion is not configured — run `chatmem notion connect …`")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			drained, still, err := w.DrainPending(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("drained: %d\nstill pending: %d\n", drained, still)
			return nil
		},
	}
}

func notionSampleCmd() *cobra.Command {
	var sessionType string
	cmd := &cobra.Command{
		Use:   "sample",
		Short: "Print a fake sample Summary + rendered block JSON to stdout (no Notion write)",
		RunE: func(cmd *cobra.Command, args []string) error {
			s := sampleStudySummary()
			if sessionType == "debug" {
				s = sampleDebugSummary()
			}
			// Show the LLM-facing Summary
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			fmt.Println("── Summary (what the LLM composes) ──")
			_ = enc.Encode(s)
			fmt.Println()
			fmt.Println("── Rendered Notion blocks (what would be POSTed) ──")
			blocks := notion.Render(s, notion.RenderMeta{
				ConversationID: "00000000-0000-0000-0000-000000000001",
				Model:          "claude-opus-4-7", Provider: "anthropic",
				ClientID: "windsurf", SynthesizedAt: time.Now().UTC(), Version: 1,
			}, notion.Transcript{})
			_ = enc.Encode(blocks)
			return nil
		},
	}
	cmd.Flags().StringVar(&sessionType, "type", "study", "study | debug")
	return cmd
}

// openStore returns a Store bound to chatmem's Postgres. If another chatmem
// process is already running (chatmem mcp / daemon from a live Windsurf or
// Claude Code session), we attach to its Postgres on the default port
// instead of trying to start a second one — Postgres refuses two processes
// on the same data dir. In "attached" mode the cleanup func is a no-op.
func openStore(ctx context.Context) (*store.Store, func(), error) {
	// First try: attach to a running instance.
	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable&connect_timeout=2", defaultPort)
	if pool, err := pgxpool.New(ctx, dsn); err == nil {
		if err := pool.Ping(ctx); err == nil {
			s := store.New(pool)
			if err := s.EnsureSchema(ctx); err == nil {
				return s, func() { pool.Close() }, nil
			}
			pool.Close()
		} else {
			pool.Close()
		}
	}
	// Fall back: start our own embedded PG (owned mode).
	pg := chatpg.New(chatpg.Config{
		DataDir:    dataHome() + "/pgdata",
		RuntimeDir: cacheHome() + "/pg-runtime",
		Port:       defaultPort,
	})
	if err := pg.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("start postgres: %w", err)
	}
	s := store.New(pg.Pool())
	if err := s.EnsureSchema(ctx); err != nil {
		_ = pg.Stop()
		return nil, nil, err
	}
	return s, func() { _ = pg.Stop() }, nil
}

// normalizeNotionID accepts either a raw page id (with or without dashes)
// or a Notion share URL ending in the 32-hex id, and returns the canonical
// dashed uuid form.
func normalizeNotionID(input string) string {
	s := strings.TrimSpace(input)
	// Strip trailing query fragments Notion URLs often have.
	if i := strings.IndexAny(s, "?#"); i > 0 {
		s = s[:i]
	}
	// Take the last path segment.
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	// Notion sometimes prefixes with a slug-hyphen-id — id is the trailing 32 hex.
	if i := strings.LastIndex(s, "-"); i >= 0 && len(s)-i-1 == 32 {
		s = s[i+1:]
	}
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		// Give up; return as-is so the caller can send it and let Notion decide.
		return input
	}
	// Re-insert dashes: 8-4-4-4-12
	u, err := uuid.Parse(s)
	if err != nil {
		return input
	}
	return u.String()
}

func sampleStudySummary() notion.Summary {
	uid := "00000000-0000-0000-0000-000000000001"
	return notion.Summary{
		Title:       "HNSW cosine index — build-time trade-offs",
		SessionType: notion.Study,
		TLDR: []string{
			"HNSW builds a layered graph; recall/latency trade-off is set at build time.",
			"m + ef_construction dominate build cost.",
			"pgvector defaults (m=16, ef_construction=64) are conservative for < 100k vectors.",
		},
		Concepts: []notion.Concept{
			{
				Heading: "HNSW", Definition: "Hierarchical Navigable Small World — a layered proximity graph optimized for approximate k-NN over high-dimensional vectors.",
				Body: "Each new vector is inserted into log(N) upper layers; search greedily descends from the sparsest layer.\n\nRecall is a monotonic function of ef_search (query-time), not ef_construction (build-time).",
				CitedFrom: []string{uid},
			},
		},
		Diagrams: []notion.Diagram{{
			Type: "flowchart",
			Title: "HNSW search path",
			Mermaid: "flowchart TD\n    Q[Query vector] --> L2[Layer 2 entry]\n    L2 --> L1[Layer 1]\n    L1 --> L0[Layer 0 base]\n    L0 --> K[Return k nearest]",
		}},
	}
}

func sampleDebugSummary() notion.Summary {
	uid := "00000000-0000-0000-0000-000000000002"
	return notion.Summary{
		Title:       "Fix: CREATE EXTENSION vector fails on fresh embedded PG",
		SessionType: notion.Debug,
		Status:      "resolved",
		TLDR: []string{
			"CREATE EXTENSION vector failed with 'could not access file $libdir/vector'.",
			"Cause: pgvector .dylib wasn't dropped into <runtimeDir>/lib/postgresql/.",
			"Fix: run internal/pg installPgvector() after pg.Start() and before first CREATE EXTENSION.",
		},
		Attempts: []notion.DebugAttempt{{
			Number: 1, Description: "assumed pgvector was bundled with embedded-postgres",
			Expected: "CREATE EXTENSION vector; succeeds",
			Actual:   "ERROR: could not access file \"$libdir/vector\"",
			Learning: "embedded-postgres ships bare Postgres, no extensions",
			CitedFrom: []string{uid},
		}},
		RootCause: &notion.Cite{
			Text:      "vector.dylib was never copied into the extracted PG runtime's lib dir.",
			CitedFrom: []string{uid},
		},
		Resolution: &notion.Resolution{
			Steps: []string{"install pgvector .dylib into <runtimeDir>/lib/postgresql/", "install vector.control + vector--0.8.5.sql into <runtimeDir>/share/postgresql/extension/", "run CREATE EXTENSION vector"},
			Language: "go", CitedFrom: []string{uid},
		},
		Diagrams: []notion.Diagram{{
			Type: "timeline",
			Mermaid: "timeline\n    title Debug attempts\n    Step 1 : Ran CREATE EXTENSION vector → error\n    Step 2 : Located vector.dylib in Homebrew Cellar\n    Step 3 : Copied to runtimeDir/lib/postgresql/\n    Step 4 : Re-ran CREATE EXTENSION → success",
		}},
	}
}
