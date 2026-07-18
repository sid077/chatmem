# CLAUDE.md — chatmem developer reference

This file is auto-loaded into every Claude Code session opened in this repository. It is the source of truth for how the codebase is organized, what invariants must hold, and how to make changes safely. **Keep it in sync with the code — every functional change should update this file, `README.md`, and `~/.claude/skills/chatmem/SKILL.md` in the same commit.**

## What this project is

Cross-platform CLI + daemon that captures LLM chat history over MCP, stores it in embedded Postgres 18 + pgvector locally, and serves relevant context back to LLMs. Open-core (Apache-2.0). Only anonymous telemetry leaves the machine.

Design plan (larger-picture context): `~/.claude/plans/i-want-to-make-buzzing-conway.md`.

## Package map

| Path | Responsibility | Key entry points |
|------|-----------------|------------------|
| `cmd/chatmem` | Cobra CLI. One file per subcommand. | `main.go`, `init.go`, `daemon.go`, `mcp.go`, `telemetry.go` |
| `internal/pg` | Embedded Postgres lifecycle + pgvector install. | `New(cfg)` → `Start(ctx)`, `Pool()`, `Stop()` |
| `internal/store` | pgx-backed data access + schema. | `EnsureSchema`, `RecordMessage`, `SearchHistory`, `GetConversation` |
| `internal/mcp` | MCP tool registration. | `NewServer(store, version)` → `*sdk.Server` |
| `internal/telemetry` | Install id + opt-out gate. | `Load(dataHome)`, `SetEnabled`, `Client.Ping` |

Assets embedded via `//go:embed`:
- `internal/pg/assets/darwin_arm64/vector.dylib` (pgvector 0.8.5 from Homebrew)
- `internal/pg/assets/darwin_arm64/extension/{vector.control, vector--0.8.5.sql}`
- `internal/store/schema.sql`

## Invariants (do not break)

1. **Never write to stdout from a subprocess-facing process (`chatmem mcp`)**. The MCP protocol owns stdout. Log to stderr via `slog.NewTextHandler(os.Stderr, nil)`. `embedded-postgres`'s default logger is `os.Stdout` — override it via `cfg.LogWriter` (defaults to stderr in our wrapper).
2. **Set `SilenceUsage: true` and `SilenceErrors: true` on all cobra commands**. Cobra prints usage to stdout on error, which corrupts the MCP stream.
3. **Never store message content on our servers**. Telemetry only ever sends aggregate/anonymous numbers + install id. If you add a telemetry field, prove it's non-PII.
4. **Every `record_message` write does inserts across three tables in one transaction** (`conversations`, `messages`, `chunks`). Keep that atomicity or you'll get orphan chunks/messages.
5. **`gen_random_uuid()` is used in DDL defaults** — this requires Postgres 13+. We target PG18. Do not add a `pgcrypto` extension dependency.
6. **Search fetches `TopK * 3` and dedupes by `conversation_id`** (MMR-lite). Do not remove the dedupe.
7. **Postgres binary is a universal Mach-O fat binary via Zonky/Maven** (contains both x86_64 and arm64 slices). The runtime string reads `x86_64-apple-darwin24.6.0` even on arm64 — that is the *build* architecture, not the runtime. The extension loads native arm64 dylib successfully because the process is native arm64.

## Platform support matrix

| GOOS/GOARCH   | Status | Notes |
|---------------|--------|-------|
| `darwin/arm64` | ✅ working | Full assets in-repo |
| `darwin/amd64` | ⚠️ not built | Needs `vector.dylib` for postgresql@18 amd64 |
| `linux/amd64`  | ⚠️ not built | Needs `vector.so`; would source from Debian/Ubuntu package or self-compile |
| `linux/arm64`  | ⚠️ not built | Same as above |
| `windows/amd64`| ⚠️ not built | Needs `vector.dll`; `kardianos/service` for daemon autostart |

To add a platform:
1. Copy pgvector `.so`/`.dylib`/`.dll` + `extension/*` to `internal/pg/assets/<goos>_<goarch>/`.
2. Add the goos/goarch to `.goreleaser.yaml`'s `builds.goos` / `builds.goarch`.
3. `go build ./...` for a quick sanity check.

## Ports

| Port | Used by |
|------|---------|
| `54329` | Default for `chatmem daemon` and `chatmem mcp` |
| `54330–54332` | Ad-hoc test/dev instances |
| `54334` | `internal/store` tests |
| `54335` | `internal/mcp` tests |
| `54336` | `cmd/chatmem` e2e test |

Running any real `chatmem daemon/mcp` on the default port will collide with subsequent commands using the same port. Use `--port` overrides in dev.

## Common commands

```bash
# build + install locally
go build ./...
go build -o /tmp/chatmem ./cmd/chatmem

# run all tests (~30-40s cold, mostly PG startup)
go test ./... -count=1 -timeout=240s

# smoke test the CLI
/tmp/chatmem init                              # first-run bootstrap
/tmp/chatmem telemetry status                  # check telemetry state
/tmp/chatmem mcp --port 54329                  # serve stdio MCP

# refresh pgvector assets after `brew upgrade pgvector`
cp /opt/homebrew/Cellar/pgvector/*/lib/postgresql@18/vector.dylib \
   internal/pg/assets/darwin_arm64/vector.dylib
cp /opt/homebrew/Cellar/pgvector/*/share/postgresql@18/extension/{vector.control,vector--0.8.5.sql} \
   internal/pg/assets/darwin_arm64/extension/

# dependencies
go get <module>@latest
go mod tidy
```

## Where the data lives

| Kind | Default path | Env override |
|------|--------------|--------------|
| Persistent state | `~/.local/share/chatmem/` | `CHATMEM_HOME` |
| Cache (extracted PG binaries) | `~/Library/Caches/chatmem/` (mac) or `~/.cache/chatmem/` (linux) | `CHATMEM_CACHE` |

Data dir contents: `pgdata/` (Postgres data), `install_id`, `telemetry.json`.

## MCP tool contract

Three tools; see `internal/mcp/server.go` for the schemas and `README.md` for full field docs.

- `record_message` — writes a message + a single chunk with a zero-vector embedding (chunk exists so semantic search will Just Work when the ONNX embedder lands).
- `search_history` — full-text ranking with `ts_rank_cd`; MMR-lite dedupe; token-budget-aware truncation.
- `get_conversation` — cursor-paginated messages, ascending by `created_at`.

Client identity (`client_id`, `model`, `provider`) is always supplied by the caller — the daemon never infers. This keeps attribution clean when multiple LLM clients hit the same DB.

## Dependency policy

- **Prefer stdlib** — `log/slog`, `net/http`, `encoding/json`, `context`.
- Third-party libs must be actively maintained. Current set:
  - `github.com/spf13/cobra` — CLI framework
  - `github.com/jackc/pgx/v5` + `pgxpool` — Postgres driver
  - `github.com/fergusstrange/embedded-postgres` — PG lifecycle
  - `github.com/pgvector/pgvector-go` — vector type binding
  - `github.com/google/uuid` — UUIDs
  - `github.com/modelcontextprotocol/go-sdk` — MCP protocol
- Do not add a heavy ORM, config loader, or dependency-injection library. This is a small, imperative Go project.

## Doc sync convention

**Every functional change updates docs in the same commit:**

- **User-facing behavior change** (new tool, new command, changed default, breaking change) → update `README.md`.
- **Developer-facing convention change** (new package, new invariant, new gotcha, new port, new asset location) → update this `CLAUDE.md`.
- **Cross-session context change** (project state, architectural decision reversal, new platform support) → update `~/.claude/skills/chatmem/SKILL.md`.

If a change touches more than one category, update all of them. The CI review checklist expects at least one doc file in the diff for any change under `cmd/` or `internal/`.
