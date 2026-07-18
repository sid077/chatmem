# chatmem

Local LLM chat history, served over MCP.

`chatmem` is a single-binary utility that captures your LLM conversations via [Model Context Protocol](https://modelcontextprotocol.io/) tools that any MCP-compatible client can call (Claude Code, Cursor, aider, custom SDKs), stores everything in an **embedded Postgres 18** database with **pgvector** on your own machine, and serves relevant past context back on demand.

Only anonymous telemetry (install ID, aggregate counters, opt-in crash reports) leaves your machine — never message content.

## Status

Pre-alpha. Working end-to-end on **macOS arm64** as of `v0.0.1-dev`. Linux and Windows require additional platform assets — see [Roadmap](#roadmap).

Working today:
- `chatmem init` — bootstraps embedded Postgres + pgvector, applies schema, prints MCP client config.
- `chatmem mcp` — self-contained stdio MCP server. Exposes `record_message`, `search_history`, `get_conversation`.
- `chatmem daemon` — long-lived Postgres process (used by future daemon+shim architecture).
- `chatmem telemetry {enable,disable,status}` — honors `CHATMEM_TELEMETRY=0` env override.

## Quickstart

```
# 1) install (Homebrew tap, once the tap is published)
brew tap siddhantdubey/chatmem
brew install chatmem

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

## Building from source

Requires **Go 1.26+** and (for now) Homebrew's pgvector 0.8.5 as the source of the platform `.dylib`.

```
git clone https://github.com/siddhantdubey/chatmem
cd chatmem
brew install pgvector           # provides the platform pgvector.dylib
go build ./...
go test ./... -timeout=240s     # spins up embedded PG per test (~30-40s total)
```

The pgvector artifacts for `darwin_arm64` are committed under `internal/pg/assets/darwin_arm64/` and embedded into the binary via `//go:embed`.

## Data & config paths

| Kind    | macOS / Linux                            | Env override    |
|---------|-------------------------------------------|-----------------|
| Data    | `~/.local/share/chatmem/`                | `CHATMEM_HOME`  |
| Cache   | `~/Library/Caches/chatmem/` (mac) or `~/.cache/chatmem/` (linux) | `CHATMEM_CACHE` |
| Install id | `~/.local/share/chatmem/install_id`   | —               |
| Telemetry cfg | `~/.local/share/chatmem/telemetry.json` | —          |

Fully remove: `rm -rf $CHATMEM_HOME $CHATMEM_CACHE`.

## Telemetry

If enabled, `chatmem` sends anonymous pings (install id + aggregate counters + latency histograms) — no message content ever leaves the machine. The ingest endpoint is not live yet in MVP; the client honors opt-out immediately so nothing is transmitted regardless.

Precedence (highest wins):
1. `CHATMEM_TELEMETRY=0` (also accepts `false`, `off`)
2. `~/.local/share/chatmem/telemetry.json` (`{"enabled": false}`)
3. `chatmem telemetry disable`
4. Default: enabled

Check current state: `chatmem telemetry status`.

## Architecture

- **Language**: Go 1.26, single static binary (CGO off).
- **Storage**: `fergusstrange/embedded-postgres` v1.34 driving Postgres 18.3 in a per-user data dir. Cold start ~600 ms on M-series.
- **Vector search**: `pgvector` 0.8.5 shipped as an embedded `.dylib` (`internal/pg/assets/darwin_arm64/vector.dylib`) copied into the extracted Postgres runtime on first start.
- **MCP**: official `modelcontextprotocol/go-sdk`. Stdio transport for MCP clients.
- **Full-text search (MVP)**: Postgres `to_tsvector` + `plainto_tsquery` with a GIN index on chunks. Semantic re-ranking via ONNX MiniLM embeddings is the next milestone.

## Roadmap

**Next up:**
- ONNX MiniLM int8 embeddings → upgrade `search_history` from keyword to semantic.
- Linux `.so` and darwin/amd64 `.dylib` in `internal/pg/assets/<goos>_<goarch>/` so the release matrix covers more platforms.
- Cloudflare Worker ingest endpoint for real telemetry pings.
- `chatmem daemon` HTTP MCP + `chatmem mcp` stdio-to-HTTP shim so multiple MCP clients can share one Postgres.

**Post-MVP:**
- Optional E2E-encrypted sync (hosted).
- macOS notarization + signed `.pkg`.
- Windows Service, launchd/systemd autostart.
- Python + TypeScript SDK wrappers around the MCP tools.
- Distribution to apt, dnf/COPR, zypper/OBS, AUR, winget, scoop.

## License

Apache-2.0. See [LICENSE](./LICENSE).
