# chatmem

**Local LLM chat history, served over MCP.**

`chatmem` is a single-binary utility that captures your LLM conversations via [Model Context Protocol](https://modelcontextprotocol.io/) tools any client can call (Claude Code, Cursor, aider, custom SDKs), stores everything in an **embedded Postgres 18** database with **pgvector** on your machine, and serves relevant past context back to any LLM on demand.

Only anonymous telemetry (install ID, aggregate counters, opt-in crash reports) leaves your machine — **message content never does**.

---

## Table of contents

- [Status](#status)
- [Quickstart](#quickstart)
- [MCP tools reference](#mcp-tools-reference)
- [Commands](#commands)
- [Data & config paths](#data--config-paths)
- [Telemetry](#telemetry)
- [Architecture](#architecture)
- [Repository layout](#repository-layout)
- [Building from source](#building-from-source)
- [Testing](#testing)
- [Development workflow](#development-workflow)
- [Troubleshooting](#troubleshooting)
- [Uninstall](#uninstall)
- [Roadmap](#roadmap)
- [License](#license)

---

## Status

Pre-alpha (`v0.0.1-dev`). Fully working platforms:

| Platform        | pgvector | Distribution                                 | Verified |
|-----------------|----------|-----------------------------------------------|----------|
| `darwin/arm64`  | 0.8.5    | Homebrew tap                                  | Live use |
| `linux/amd64`   | 0.8.3    | RPM (zypper/dnf) + DEB + tarball              | Debian 12 container + openSUSE Leap 15.6 container |
| `linux/arm64`   | 0.8.3    | RPM (zypper/dnf) + DEB + tarball              | Debian 12 container + openSUSE Leap 15.6 container |
| `darwin/amd64`  | —        | —                                             | Needs an Intel-Mac pgvector.dylib |
| `windows/amd64` | —        | —                                             | Needs a pgvector.dll + service story |

The version skew between macOS and Linux is intentional (Homebrew ships 0.8.5, official Postgres apt ships 0.8.3 for PG18). Both are wire-compatible for our use — same operators, same HNSW support.

Working today:

| Command | Purpose |
|---------|---------|
| `chatmem init` | Provision the local database, apply schema, print MCP client config. |
| `chatmem mcp` | Self-contained stdio MCP server (starts + manages embedded Postgres). |
| `chatmem daemon` | Long-lived Postgres process (foundation for the future daemon+shim architecture). |
| `chatmem telemetry {enable,disable,status}` | Manage anonymous telemetry; honors `CHATMEM_TELEMETRY=0`. |

## Quickstart

```bash
# --- macOS or Linuxbrew (Homebrew tap — ships as a cask) ---
brew tap sid077/chatmem
brew install --cask chatmem

# --- openSUSE / SUSE (zypper self-hosted repo) ---
sudo zypper ar https://sid077.github.io/chatmem/chatmem.repo
sudo zypper --gpg-auto-import-keys refresh
sudo zypper in chatmem

# --- Fedora / RHEL (dnf, same repo) ---
sudo dnf config-manager --add-repo https://sid077.github.io/chatmem/chatmem.repo
sudo dnf install chatmem

# --- Debian / Ubuntu (direct .deb download; APT repo TBD) ---
# curl -sSLo /tmp/chatmem.deb https://github.com/sid077/chatmem/releases/latest/download/chatmem_<ver>_<arch>.deb
# sudo apt install /tmp/chatmem.deb

# --- direct download (any Linux) ---
# curl -sSL https://github.com/sid077/chatmem/releases/latest/download/chatmem_Linux_x86_64.tar.gz \
#     | tar -xz && sudo mv chatmem /usr/local/bin/

# 2) bootstrap the local database — prints the JSON snippet to paste into your MCP client
chatmem init

# 3) paste into ~/.claude/mcp.json (or ~/.cursor/mcp.json), restart the client

# 4) verify tools appear
#    In Claude Code, ask: "list your MCP tools" — you should see
#    record_message, search_history, get_conversation from the 'chatmem' server.
```

Example MCP client config (`init` prints this with your actual binary path):

```json
{
  "mcpServers": {
    "chatmem": {
      "command": "/opt/homebrew/bin/chatmem",
      "args": ["mcp"]
    }
  }
}
```

## MCP tools reference

All three tools are registered by [`internal/mcp/server.go`](./internal/mcp/server.go). Every write conveys `model`/`provider`/`client_id` explicitly — the daemon never infers them — so multiple LLM clients writing into the same database stay cleanly attributed.

### `record_message`

Store a single chat message. Opens a new conversation when `conversation_id` is empty (then `model`/`provider`/`client_id` are required).

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `conversation_id` | UUID string | if continuing a conversation | Omit to open a new one |
| `role` | `user` \| `assistant` \| `system` \| `tool` | yes | |
| `content` | string | yes | Message text |
| `model` | string | when opening a new conversation | e.g. `claude-opus-4-7` |
| `provider` | string | when opening a new conversation | e.g. `anthropic`, `openai` |
| `client_id` | string | when opening a new conversation | e.g. `claude-code`, `cursor`, `aider` |
| `token_count` | int | no | Optional metadata |

Returns:
```json
{ "message_id": "<uuid>", "conversation_id": "<uuid>" }
```

### `search_history`

Search stored chat history. **MVP uses Postgres full-text ranking** (`to_tsvector` + `plainto_tsquery`); semantic re-ranking via ONNX embeddings is the next milestone. Returns one hit per conversation (MMR-lite), packed to `token_budget`.

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `query` | string | yes | Free-text query |
| `top_k` | int | no | Default 10, max 100 |
| `token_budget` | int | no | Total snippet tokens (default 4000) |
| `model` | string | no | Filter by model id |
| `client_id` | string | no | Filter by client id |
| `since` | RFC3339 string | no | Lower bound on `created_at` |
| `until` | RFC3339 string | no | Upper bound on `created_at` |
| `conversation_ids` | array of UUIDs | no | Restrict to these conversations |

Returns both a rendered text block (visible to any MCP client) and a structured payload for programmatic use:

Rendered text (`Content`):
```
2 hit(s) for "kafka retention"

── hit 1 ──
role:         user
conversation: 8a2f…
message:      def6…
created:      2026-07-20T10:15:32Z
score:        0.2341
snippet:
kafka retention is set at 7 days for the ingest topic…
```

Structured (`StructuredContent`):
```json
{
  "hits": [
    { "message_id": "<uuid>", "conversation_id": "<uuid>", "role": "user",
      "snippet": "...", "score": 0.147, "created_at": "2026-07-20T10:15:32Z" }
  ]
}
```

(Before v0.0.2 `Content` was a bare hit count — Windsurf/Cascade and any client that ignores `StructuredContent` would show no snippet text.)

### `get_conversation`

Fetch a conversation and its messages, ordered by `created_at` ascending. Cursor-paginated via `after`.

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `conversation_id` | UUID string | yes | |
| `limit` | int | no | Default 100, max 500 |
| `after` | RFC3339 string | no | Return messages strictly after this timestamp |

Returns both a rendered text block and a structured payload.

Rendered text (`Content`):
```
conversation 8a2f…
model:    anthropic / claude-opus-4-7
client:   claude-code
started:  2026-07-20T10:15:32Z
updated:  2026-07-20T10:20:15Z
messages: 3

── user @ 2026-07-20T10:15:32Z ──
hello from chatmem

── assistant @ 2026-07-20T10:15:35Z ──
hi back
```

Structured (`StructuredContent`):
```json
{
  "conversation": {
    "id": "<uuid>", "client_id": "...", "model": "...", "provider": "...",
    "title": null, "started_at": "...", "updated_at": "..."
  },
  "messages": [
    { "id": "<uuid>", "role": "user", "content": "...", "token_count": 0, "created_at": "..." }
  ],
  "next_after": null
}
```

## Commands

Every subcommand has `--help`. Run `chatmem` with no arguments to see the top-level list.

### `chatmem init`

Provisions the persistent data directory, extracts embedded Postgres, installs pgvector into the runtime, and applies the schema. On completion prints a ready-to-paste MCP JSON snippet with the actual binary path (`os.Executable()`).

Safe to re-run — all provisioning steps are idempotent.

### `chatmem mcp [--port <n>]`

Runs a stdio MCP server for a single client. Starts embedded Postgres for the process lifetime and stops it cleanly on stdin close or SIGTERM.

Concurrent MCP clients cannot share one embedded Postgres in the MVP — each `chatmem mcp` process opens its own PG on the port supplied. See [Roadmap](#roadmap) for the daemon+shim architecture.

### `chatmem daemon [--port <n>]`

Runs the long-lived Postgres foundation for the future daemon+shim architecture. Not required for MVP usage. Ends cleanly on SIGINT/SIGTERM.

### `chatmem telemetry {enable|disable|status}`

Manage the anonymous telemetry setting. `status` prints the effective state, the precedence source (`env` | `config` | `default`), and the install id.

## Data & config paths

| Kind          | macOS                                    | Linux                                    | Env override      |
|---------------|------------------------------------------|------------------------------------------|-------------------|
| Data          | `~/.local/share/chatmem/`                | `~/.local/share/chatmem/`                | `CHATMEM_HOME`    |
| Cache         | `~/Library/Caches/chatmem/`              | `~/.cache/chatmem/`                      | `CHATMEM_CACHE`   |
| Install id    | `<data>/install_id`                      | `<data>/install_id`                      | —                 |
| Telemetry cfg | `<data>/telemetry.json`                  | `<data>/telemetry.json`                  | —                 |
| Postgres data | `<data>/pgdata/`                         | `<data>/pgdata/`                         | —                 |
| Postgres runtime | `<cache>/pg-runtime/`                 | `<cache>/pg-runtime/`                    | —                 |

## Telemetry

If enabled, `chatmem` sends anonymous pings (install id + aggregate counters + latency histograms) — **no message content ever leaves the machine**. The ingest endpoint is not live yet in MVP; the client honors opt-out immediately so nothing is transmitted regardless.

Precedence (highest wins):
1. `CHATMEM_TELEMETRY=0` (also accepts `false`, `off`)
2. `<data>/telemetry.json` (`{"enabled": false}`)
3. `chatmem telemetry disable`
4. Default: enabled

Check current state: `chatmem telemetry status`.

## Architecture

```
LLM client (Claude Code, Cursor, aider, custom)
        │
        ▼  MCP over stdio
   chatmem mcp
        │
        ▼
  embedded Postgres 18 + pgvector 0.8.5
        │
        ▼
  chunks (with vector(384) column, HNSW cosine index)
  messages (btree on conv_id + created_at)
  conversations (append-only event log ready for future sync)
```

- **Language**: Go 1.26, single static binary (CGO off).
- **Storage**: `fergusstrange/embedded-postgres` v1.34 driving Postgres 18.3 in a per-user data dir. Cold start ~600 ms on M-series (~7 s on the very first run because of `initdb`).
- **Vector column**: `vector(384)` on the `chunks` table with an HNSW cosine index (`m=16, ef_construction=64`), primed for semantic search. Values are zero-vectors until the ONNX embedder lands.
- **Search (MVP)**: Postgres full-text — `to_tsvector('english', content)` + `plainto_tsquery`, GIN index (`chunks_tsv_idx`). Ranked with `ts_rank_cd`.
- **MCP**: official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) v1.6.1. Stdio transport.
- **CLI**: `spf13/cobra` v1.10.

The pgvector `.dylib` for `darwin_arm64` is committed under `internal/pg/assets/darwin_arm64/` and shipped inside the Go binary via `//go:embed`. On first `Start()`, `internal/pg` copies the dylib into `<runtimeDir>/lib/postgresql/` and the control/SQL files into `<runtimeDir>/share/postgresql/extension/` — then `CREATE EXTENSION vector` works out of the box.

## Repository layout

```
chatmem/
├── cmd/chatmem/                # cobra CLI entrypoint + subcommands
│   ├── main.go
│   ├── init.go                 # chatmem init
│   ├── daemon.go               # chatmem daemon (+ dataHome / cacheHome helpers)
│   ├── mcp.go                  # chatmem mcp (stdio MCP server)
│   ├── telemetry.go            # chatmem telemetry {enable,disable,status}
│   └── mcp_e2e_test.go         # spawns built binary, drives stdio MCP as a real client
├── internal/
│   ├── pg/                     # embedded-postgres wrapper + pgvector install
│   │   ├── embedded.go
│   │   └── assets/
│   │       ├── darwin_arm64/vector.dylib + extension/{vector.control,vector--0.8.5.sql}
│   │       ├── linux_amd64/vector.so     + extension/{vector.control,vector--0.8.3.sql}
│   │       └── linux_arm64/vector.so     + extension/{vector.control,vector--0.8.3.sql}
│   ├── store/                  # schema + pgx-backed data access
│   │   ├── schema.sql
│   │   ├── store.go            # EnsureSchema, RecordMessage, SearchHistory, GetConversation
│   │   └── store_test.go
│   ├── mcp/                    # MCP tool registration
│   │   ├── server.go           # NewServer, register{RecordMessage,SearchHistory,GetConversation}
│   │   └── server_test.go      # in-process MCP round-trip
│   └── telemetry/              # install_id + opt-out gate
│       └── telemetry.go
├── .goreleaser.yaml            # cross-platform build + Homebrew tap + rpm/deb via nfpm
├── .github/workflows/release.yml  # tag push → goreleaser + gh-pages RPM repo publish
├── scripts/build-rpm-repo.sh   # assemble zypper/dnf repo tree locally (uses createrepo_c via docker)
├── README.md
├── CLAUDE.md                   # in-repo dev docs, auto-loaded by Claude Code
└── LICENSE                     # Apache-2.0
```

## Building from source

Requires **Go 1.26+** and, for now, Homebrew's pgvector 0.8.5 (only if you want to refresh the vendored dylib — the committed copy is enough to build).

```bash
git clone https://github.com/sid077/chatmem
cd chatmem
go build ./...
```

To refresh the vendored pgvector artifacts:

```bash
# darwin (from Homebrew) — refreshes internal/pg/assets/darwin_arm64/
brew install pgvector
cp /opt/homebrew/Cellar/pgvector/0.8.5/lib/postgresql@18/vector.dylib \
   internal/pg/assets/darwin_arm64/vector.dylib
cp /opt/homebrew/Cellar/pgvector/0.8.5/share/postgresql@18/extension/{vector.control,vector--0.8.5.sql} \
   internal/pg/assets/darwin_arm64/extension/

# linux amd64/arm64 (from official PostgreSQL apt, pgdg11+1 for glibc 2.31 baseline)
for arch in amd64 arm64; do
  curl -sSLo /tmp/pgv-$arch.deb \
    "https://apt.postgresql.org/pub/repos/apt/pool/main/p/pgvector/postgresql-18-pgvector_0.8.3-1.pgdg11+1_${arch}.deb"
  tmp=$(mktemp -d); cd "$tmp"; ar x /tmp/pgv-$arch.deb; tar -xf data.tar.xz
  cp "$tmp/usr/lib/postgresql/18/lib/vector.so" internal/pg/assets/linux_${arch}/vector.so
  cp "$tmp/usr/share/postgresql/18/extension/"{vector.control,vector--0.8.3.sql} \
     internal/pg/assets/linux_${arch}/extension/
done
```

The Linux `.so` is built against Debian 11's glibc 2.31 for maximum runtime compatibility — anything with glibc ≥ 2.31 works (Debian 11+, Ubuntu 22.04+, RHEL 9+, Alpine with `libc6-compat`, etc.).

## Testing

Every test that touches storage spins up a real embedded Postgres — expect ~7–10 s per test package on cold cache.

```bash
# unit + integration
go test ./... -count=1 -timeout=240s

# just the storage layer round-trip
go test ./internal/store -count=1 -v

# just the in-process MCP round-trip
go test ./internal/mcp -count=1 -v

# end-to-end: spawn the built binary as a subprocess, drive stdio MCP
go test ./cmd/chatmem -run TestBinaryStdioMCP -count=1 -v
```

Tests use distinct hard-coded ports (`54334`, `54335`, `54336`) — run one test package at a time if you have a real chatmem daemon running on `54329`.

## Development workflow

1. **Edit** — code lives under `cmd/chatmem` and `internal/`.
2. **Test** — `go test ./...` after every change; add a test alongside any new store/MCP behavior.
3. **Update the docs on every functional change**:
   - `README.md` for user-facing changes (new tool, new command, changed defaults).
   - `CLAUDE.md` for developer-facing changes (new package, new invariant, new gotcha).
   - `~/.claude/skills/chatmem/SKILL.md` for cross-session context (kept in sync automatically).
4. **Commit** — one focused change per commit, imperative subject line.
5. **`go mod tidy`** if dependencies changed.

## Troubleshooting

**On macOS after `brew install --cask chatmem`: binary killed with `zsh: killed chatmem`** — Gatekeeper quarantined the unsigned cask binary. Strip the attribute:

```bash
xattr -d com.apple.quarantine "$(brew --prefix)/bin/chatmem"
```

The real fix is signing + notarizing the darwin binary with an Apple Developer ID (planned for v0.0.2).

**`chatmem cannot run as root`** — on Linux, Postgres refuses to run under uid 0 and chatmem now refuses too, up-front. Re-run as an unprivileged user: `su - <username> -c 'chatmem init'` (or `sudo -u <username> chatmem init`).

**`chatmem mcp` client sees "invalid character 'T' looking for beginning of value"** — the MCP protocol runs over stdout; something is writing non-JSON there. Most likely `embedded-postgres` was configured with `logger: os.Stdout` instead of `os.Stderr` (default here is `os.Stderr`; check `internal/pg/embedded.go` if you customized).

**`no embedded pgvector assets for <goos>/<goarch>`** — you are running on an unsupported platform. Copy a matching prebuilt pgvector into `internal/pg/assets/<goos>_<goarch>/` (see [Building from source](#building-from-source) for the file layout) and rebuild.

**First `chatmem init` takes ~15–20 s** — that is `initdb` for a brand-new data directory plus Postgres binary extraction. Subsequent runs (warm caches) are ~1 s.

**Port `54329` already in use** — either another `chatmem daemon`/`mcp` process is running (kill it) or something else grabbed the port. Use `--port` to override.

**`CREATE EXTENSION vector` fails with `could not access file "$libdir/vector"`** — the pgvector `.dylib` didn't get copied into `<runtimeDir>/lib/postgresql/`. Check `internal/pg/embedded.go`'s `installPgvector` output; usually a wiped-out runtime dir mid-run.

## Uninstall

```bash
brew uninstall chatmem                # or delete the binary you built
rm -rf ~/.local/share/chatmem         # data (delete only if you're sure)
rm -rf ~/Library/Caches/chatmem       # cache (macOS) — safe to delete anytime
rm -rf ~/.cache/chatmem               # cache (linux) — safe to delete anytime
rm -f  ~/.claude/mcp.json             # or hand-remove the chatmem entry
```

## Roadmap

**Next up (still MVP-scope):**
- ONNX MiniLM int8 embedder → upgrade `search_history` from keyword to semantic.
- Remaining platforms: `darwin/amd64` (Intel Mac) and `windows/amd64`.
- Cloudflare Worker ingest endpoint for real telemetry pings.
- `chatmem daemon` HTTP MCP + `chatmem mcp` stdio-to-HTTP shim so multiple MCP clients can share one Postgres.

**Post-MVP (v1.0):**
- Optional E2E-encrypted sync (hosted, open-core).
- macOS notarization + signed `.pkg`.
- Windows Service, launchd/systemd autostart.
- Python + TypeScript SDK wrappers around the MCP tools.
- Distribution to apt, dnf/COPR, zypper/OBS, AUR, winget, scoop.
- Docs site.

## Publishing a release

Tag-triggered — the `release` GitHub Actions workflow does everything.

```bash
git tag -a v0.0.1 -m "v0.0.1"
git push origin v0.0.1
```

On tag push the workflow:

1. Runs `goreleaser release --clean` — cross-compiles darwin/arm64 + linux/amd64 + linux/arm64, packages `.tar.gz` + `.rpm` + `.deb`, uploads the archives to the GitHub Release, and pushes the updated Homebrew formula into `sid077/homebrew-chatmem`.
2. Runs `createrepo_c` on the `.rpm`s to build a zypper/dnf-compatible repo tree.
3. Pushes the repo tree to the `gh-pages` branch of this repository.

Users then get updates via `zypper refresh` / `brew upgrade` / `dnf update` without any further action from you.

To dry-run locally before tagging:

```bash
goreleaser release --snapshot --clean --skip=publish   # produces dist/*
scripts/build-rpm-repo.sh                              # produces dist/rpm-repo/ (needs Docker for createrepo_c)
```

Verify with an openSUSE container:

```bash
cd dist/rpm-repo && python3 -m http.server 8765 &
docker run --rm --platform linux/arm64 --add-host=host.docker.internal:host-gateway \
  opensuse/leap:15.6 bash -c '
    echo -e "[chatmem]\nbaseurl=http://host.docker.internal:8765/\$basearch/\nenabled=1\ngpgcheck=0" > /etc/zypp/repos.d/chatmem.repo
    zypper --non-interactive refresh chatmem
    zypper --non-interactive install chatmem
    chatmem --version'
```

### GPG signing (recommended for production)

MVP ships unsigned (`gpgcheck=0` in the `.repo` file). To sign:

1. Generate a GPG key: `gpg --gen-key`.
2. Export the public key: `gpg --armor --export you@example.com > chatmem.gpg`.
3. In `.goreleaser.yaml`, add `signs:` for the rpms with your key id.
4. Copy `chatmem.gpg` alongside the `.repo` file in `dist/rpm-repo/` and change `gpgcheck=1` + `gpgkey=<URL>/chatmem.gpg`.

## License

Apache-2.0. See [LICENSE](./LICENSE).
