# CLAUDE.md — chatmem developer reference

This file is auto-loaded into every Claude Code session opened in this repository. It is the source of truth for how the codebase is organized, what invariants must hold, and how to make changes safely. **Keep it in sync with the code — every functional change should update this file, `README.md`, and `~/.claude/skills/chatmem/SKILL.md` in the same commit.**

## What this project is

Cross-platform CLI + daemon that captures LLM chat history over MCP, stores it in embedded Postgres 18 + pgvector locally, and serves relevant context back to LLMs. Open-core (Apache-2.0). Only anonymous telemetry leaves the machine.

Design plan (larger-picture context): `~/.claude/plans/i-want-to-make-buzzing-conway.md`.

## Package map

| Path | Responsibility | Key entry points |
|------|-----------------|------------------|
| `cmd/chatmem` | Cobra CLI. One file per subcommand. `daemon.go` also hosts `requireNonRoot()` + `preflight()` (HOME sanity) + `dataHome()` + `cacheHome()` shared helpers. `notion.go` hosts `openStore()` — the "attach to running chatmem PG if present, else start own" helper used by CLI commands that read the store outside the mcp/daemon lifecycles. `service.go` hosts the OS-native launchd (macOS) / systemd --user (Linux) service install + install/uninstall/start/stop/restart/status commands. | `main.go`, `init.go`, `daemon.go`, `mcp.go`, `telemetry.go`, `doctor.go`, `notion.go`, `import.go`, `service.go` |
| `internal/pg` | Embedded Postgres lifecycle + pgvector install. | `New(cfg)` → `Start(ctx)`, `Pool()`, `Stop()` |
| `internal/store` | pgx-backed data access + schema. | `EnsureSchema`, `RecordMessage`, `SearchHistory`, `GetConversation` |
| `internal/mcp` | MCP tool registration. | `NewServer(store, version)` → `*sdk.Server` |
| `internal/telemetry` | Install id + opt-out gate + in-process aggregator + flush loop. | `Load(dataHome)`, `SetEnabled`, `NewAggregator()`, `NewClient(state, dataHome, log, opts).Start(ctx)` |
| `internal/notion` | Notion synthesis: config, HTTP client, block builders, Summary schema + validation, study/debug page rendering, pending-write persistence. | `NewWriter(dataHome, "")` → `w.Synthesize(ctx, in)`; `BuildSynthesisPrompt(conv)` for the LLM brief. |
| `server/telemetry-worker` | Cloudflare Worker + D1 that receives `POST /v1/ping`. Deployed independently via `wrangler`. | `server/telemetry-worker/src/index.ts`, `schema.sql`, `wrangler.toml` |
| `smithery.yaml` | Kept for future re-attempt. Smithery today only accepts hosted URLs or spawnable packages; native-binary MCP servers like chatmem are not registerable there. File explains this in its header. | Root `smithery.yaml` |
| `docs/marketplace-submissions.md` | Playbook for getting listed on awesome-mcp-servers, Smithery, PulseMCP, Glama. Keep updated as marketplaces evolve. | `docs/marketplace-submissions.md` |
| `.well-known/mcp/server-card.json` | MCP server-card advertising the tool schemas without needing to spawn the binary. Copied into gh-pages by the release workflow. Consumed by Smithery (locally-installed binaries can't be auto-scanned) and the official MCP registry. Update alongside `internal/mcp/server.go` when tools change. | `.well-known/mcp/server-card.json` |
| `scripts/build-rpm-repo.sh` | Assembles a zypper/dnf-compatible repo tree from `dist/*.rpm`. Runs `createrepo_c` inside a `fedora:41` docker container so macOS hosts don't need it installed. Default `BASE_URL` = `https://sid077.github.io/chatmem`. | `scripts/build-rpm-repo.sh [BASE_URL]` |
| `.github/workflows/release.yml` | Tag-triggered release: `goreleaser release --clean` → assemble RPM repo tree with `createrepo_c` (native, not docker, in CI) → push to `gh-pages` branch. | `git push origin vX.Y.Z` |

Assets embedded via `//go:embed`:
- `internal/pg/assets/darwin_arm64/vector.dylib` (pgvector **0.8.5**, from Homebrew)
- `internal/pg/assets/darwin_arm64/extension/{vector.control, vector--0.8.5.sql}`
- `internal/pg/assets/linux_amd64/vector.so` (pgvector **0.8.3**, from apt.postgresql.org `pgdg11+1` — glibc 2.31 baseline)
- `internal/pg/assets/linux_amd64/extension/{vector.control, vector--0.8.3.sql}`
- `internal/pg/assets/linux_arm64/vector.so` (pgvector **0.8.3**, same source, arm64)
- `internal/pg/assets/linux_arm64/extension/{vector.control, vector--0.8.3.sql}`
- `internal/store/schema.sql`

The pgvector version skew between darwin (0.8.5) and linux (0.8.3) is intentional — the upstream Debian package for PG18 only goes up to 0.8.3 at time of writing. Both versions support `vector(384)`, HNSW, and the operators we use.

## Invariants (do not break)

1. **Never write to stdout from a subprocess-facing process (`chatmem mcp`)**. The MCP protocol owns stdout. Log to stderr via `slog.NewTextHandler(os.Stderr, nil)`. `embedded-postgres`'s default logger is `os.Stdout` — override it via `cfg.LogWriter` (defaults to stderr in our wrapper).
2. **Set `SilenceUsage: true` and `SilenceErrors: true` on all cobra commands**. Cobra prints usage to stdout on error, which corrupts the MCP stream.
3. **Never store message content on our servers**. Telemetry only ever sends aggregate/anonymous numbers + install id. If you add a telemetry field, prove it's non-PII.
4. **Every `record_message` write does inserts across three tables in one transaction** (`conversations`, `messages`, `chunks`). Keep that atomicity or you'll get orphan chunks/messages.
5. **`gen_random_uuid()` is used in DDL defaults** — this requires Postgres 13+. We target PG18. Do not add a `pgcrypto` extension dependency.
6. **Search fetches `TopK * 3` and dedupes by `conversation_id`** (MMR-lite). Do not remove the dedupe.
7. **Postgres binary is a universal Mach-O fat binary via Zonky/Maven** (contains both x86_64 and arm64 slices). The runtime string reads `x86_64-apple-darwin24.6.0` even on arm64 — that is the *build* architecture, not the runtime. The extension loads native arm64 dylib successfully because the process is native arm64.
8. **`chatmem cannot run as root` on Linux/macOS.** All three long-running subcommands (`init`, `daemon`, `mcp`) call `requireNonRoot()` before doing anything, because Postgres refuses to run under uid 0 and its own error message is unhelpful. Do not remove that guard.
9. **Linux pgvector `.so` must be built against glibc 2.31 or older.** We use the `pgdg11+1` variant of the official apt package for that reason — anything newer bumps the glibc floor and breaks users on older distros. If you refresh the artifacts, keep using `pgdg11+1`.
10. **RPM/DEB packages declare `glibc >= 2.31` / `libc6 (>= 2.31)`.** That dependency must always match the pgvector `.so`'s glibc floor. If someone rebuilds pgvector against a newer base, bump both in `.goreleaser.yaml`'s `nfpms.overrides`.
11. **The gh-pages branch is auto-managed by the release workflow.** Never hand-edit it. It gets rewritten on every tag push (`keep_files: false` in the workflow), so contents are strictly the current release's RPM repo tree.
12. **Homebrew distribution is a cask, not a formula.** `.goreleaser.yaml` uses `homebrew_casks:` because `brews:` was soft-deprecated in goreleaser v2.10. User-facing install command is `brew install --cask chatmem`. Cask files write to `homebrew-chatmem/Casks/chatmem.rb` (not `Formula/`).
13. **macOS binary is unsigned as of v0.0.1** — Homebrew Cask stamps `com.apple.quarantine` on it and Gatekeeper kills first invocation. Documented workaround: `xattr -d com.apple.quarantine $(brew --prefix)/bin/chatmem`. Real fix requires Apple Developer ID + notarization; add a `signs:` block to `.goreleaser.yaml` with `AC_USERNAME` + team id secrets when we have them.
14. **Every MCP tool must render its full return in `Content`, not just a summary.** Many MCP clients (Windsurf Cascade, some others) show `Content` to the LLM and ignore `StructuredContent`. If a tool only emits "N hits" or "N messages" in `Content`, the LLM sees nothing useful. The `renderSearchHits` and `renderConversation` helpers in `internal/mcp/server.go` are the pattern — copy them for any new query-style tool. `record_message` is exempt because its useful return IS the summary (write confirmation).
15. **Telemetry payloads must never carry message content, query strings, filenames, or prompts.** The `internal/telemetry.Payload` shape is deliberately structural: counters, distributions, latency stats, install id, version. If someone adds a new field, prove it's non-PII before merging. The client is compiled with the assumption that anything in Payload is safe to persist to `<data>/pending/*.json` and POST to a public endpoint.
16. **MCP tool handlers must guard `agg != nil` before calling Record*.** Tests pass `nil` for the aggregator to keep them fast + isolated; skipping the nil check would panic in `internal/mcp` tests.
17. **The telemetry ingest URL is baked into release binaries via `-ldflags -X`** targeting `internal/telemetry.defaultIngestURL`. The literal string lives in `.goreleaser.yaml` (public info — the endpoint accepts any anonymous POST anyway). Dev builds (`go build ./...`) have an empty default, so they run in local-only mode unless `CHATMEM_TELEMETRY_URL` is set. Env var always wins over the baked default so users can override.
18. **When you add or change an MCP tool, update `.well-known/mcp/server-card.json` in the same commit** as `internal/mcp/server.go`. That JSON is what Smithery + the official MCP registry read to discover our tools without spawning the binary. If they drift, the registry entry lies about our capabilities.
19. **Every subcommand that touches state calls `requireNonRoot()` THEN `preflight()` at the top.** Preflight catches the two big footguns Postgres gives cryptic errors for: `sudo -E` preserving `HOME=/root` while dropping to non-root uid, and `HOME` unset entirely. Do not skip these — Postgres's own errors for these cases are unactionable.
20. **`internal/pg.Config.CacheDir` overrides embedded-postgres's tarball cache.** Default is `<cacheHome>/binary-cache` so all regeneratable state lives under one predictable root. Without this override, embedded-postgres writes `.embedded-postgres-go/` into HOME (or worse, CWD if HOME is unwritable). Do not remove.
21. **`internal/mcp.NewServer(Deps{...})` — always add features via the Deps struct.** Positional arguments broke twice; the struct keeps forward-compat. `Deps.NotionWriter == nil` means notion tools return "not configured", not panic — every new feature must degrade cleanly when its dep is nil.
22. **When you change the MCP tool set, update `.well-known/mcp/server-card.json` AND the notion prompt schema in the same commit.** The server-card is what registries advertise; the notion prompt is what the LLM follows. Drift breaks discovery + synthesis quality.
23. **Notion synthesis is opt-in per install.** If `notion.json` doesn't exist, chatmem behaves exactly as it did before the feature landed. All three notion MCP tools return "not configured" errors instead of panicking. Do not require Notion for any core workflow.
24. **MCP tool args must be typed, never `json.RawMessage` / `[]byte`.** The SDK's JSON-Schema reflector renders `[]byte` as `null | array<integer max 255>`, which makes the tool impossible to call from JSON. v0.2.0's `synthesize_to_notion` shipped with this bug (fixed in v0.2.1 by using `notion.Summary` directly for the field). If you want "unstructured" args, use `map[string]any` or a named struct — never bytes. Regression covered by `TestSynthesizeToolAcceptsSummaryObject` in `internal/mcp/`.
25. **Optional Summary fields must have `,omitempty` on their json tag.** The SDK's schema reflector treats string fields without omitempty as `required`. Missing omitempty on `Diagram.Title` was v0.2.1's second bug (fixed same tag). When adding sub-types to `notion.Summary`, check each field's optionality against the prompt-schema doc in `internal/notion/prompt.go` and match tags accordingly.
26. **Return coverage failures via `IsError: true`, not a Go `error`.** The MCP SDK drops Content when a handler returns a non-nil error. The coverage gate needs the LLM to see the full missed-uuid list, so it returns `&CallToolResult{IsError: true, Content: [...]}, nil`. Same pattern for any recoverable "please retry with fixes" flow.
27. **Coverage baseline: if `synth_facts` has any rows for a conversation, coverage is measured against messages-with-non-trivia-facts. Otherwise, it's measured against all messages.** This makes the extraction phase incentive-aligned (extract → get lower coverage bar; skip extraction → must cite every message). Never change this without also updating the prompt so the LLM understands the trade.
28. **`internal/pg.Start()` acquires a `<runtimeDir>.lock` before extracting.** Concurrent chatmem starts (common when an MCP client restarts the server) used to race and leave a half-extracted `pg-runtime` — the second process would crash on a `rename … no such file or directory`. Serialized under `gofrs/flock`. Also cleans up leftover `temp_*/` dirs from a previous crashed extract. Do not remove.
29. **`chatmem mcp` attaches to a running Postgres if one is listening on the port before starting its own.** `tryAttachPostgres` in `cmd/chatmem/mcp.go` does a 2 s ping. If it succeeds, we don't own the PG lifecycle (`stopPG = func() { pool.Close() }`). This is what makes `chatmem install` valuable — installed daemon keeps PG warm, MCP invocations attach instantly.

## Platform support matrix

| GOOS/GOARCH    | Status         | Notes |
|----------------|----------------|-------|
| `darwin/arm64` | ✅ working     | pgvector 0.8.5 from Homebrew, live use |
| `linux/amd64`  | ✅ working     | pgvector 0.8.3 from apt.postgresql.org (pgdg11+1); verified in Debian 12 container |
| `linux/arm64`  | ✅ working     | pgvector 0.8.3 from apt.postgresql.org (pgdg11+1); verified in Debian 12 container |
| `darwin/amd64` | ⚠️ not built   | Needs a `vector.dylib` for postgresql@18 amd64 (Homebrew Intel bottle or self-compile) |
| `windows/amd64`| ⚠️ not built   | Needs `vector.dll`; `kardianos/service` for daemon autostart |

To add a platform:
1. Copy pgvector `.so`/`.dylib`/`.dll` + `extension/{vector.control, vector--<ver>.sql}` to `internal/pg/assets/<goos>_<goarch>/`. Version number in the SQL filename does not have to match darwin — the `installPgvector` code copies whatever is in `extension/`.
2. Add the goos/goarch to `.goreleaser.yaml`'s `builds.goos` / `builds.goarch` (and update the `ignore:` list if needed).
3. Cross-compile and verify: `GOOS=<x> GOARCH=<y> CGO_ENABLED=0 go build -o /tmp/chatmem-<x>-<y> ./cmd/chatmem`.
4. If possible, run the binary in a container/VM for that platform and verify `chatmem init` succeeds + `CREATE EXTENSION vector` works.

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

# refresh darwin pgvector assets after `brew upgrade pgvector`
cp /opt/homebrew/Cellar/pgvector/*/lib/postgresql@18/vector.dylib \
   internal/pg/assets/darwin_arm64/vector.dylib
cp /opt/homebrew/Cellar/pgvector/*/share/postgresql@18/extension/{vector.control,vector--0.8.5.sql} \
   internal/pg/assets/darwin_arm64/extension/

# refresh linux pgvector assets (both archs) from apt.postgresql.org
for arch in amd64 arm64; do
  curl -sSLo /tmp/pgv-$arch.deb \
    "https://apt.postgresql.org/pub/repos/apt/pool/main/p/pgvector/postgresql-18-pgvector_0.8.3-1.pgdg11+1_${arch}.deb"
  tmp=$(mktemp -d); (cd "$tmp" && ar x /tmp/pgv-$arch.deb && tar -xf data.tar.xz)
  cp "$tmp/usr/lib/postgresql/18/lib/vector.so" internal/pg/assets/linux_${arch}/vector.so
  cp "$tmp/usr/share/postgresql/18/extension/"{vector.control,vector--0.8.3.sql} \
     internal/pg/assets/linux_${arch}/extension/
done

# cross-compile all supported platforms in one go
for target in darwin/arm64 linux/amd64 linux/arm64; do
  GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go build \
    -o /tmp/chatmem-${target/\//-} ./cmd/chatmem
done

# verify a linux binary in a container (arm64 host)
docker run --rm --platform linux/arm64 \
  -v /tmp/chatmem-linux-arm64:/usr/local/bin/chatmem:ro debian:12-slim \
  bash -c 'useradd -m u && su - u -c "/usr/local/bin/chatmem init"'

# dry-run the full release locally (no publish, no tag)
goreleaser release --snapshot --clean --skip=publish
scripts/build-rpm-repo.sh
# then serve dist/rpm-repo/ and test zypper install in opensuse/leap:15.6

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
