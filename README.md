# chatmem

Local LLM chat history, served over MCP.

`chatmem` runs a local daemon that captures your LLM conversations (via [Model Context Protocol](https://modelcontextprotocol.io/) tools any client can call), stores them in an embedded Postgres database with `pgvector` on your own machine, and serves relevant past context back to your current LLM via semantic search.

Only anonymous telemetry (install ID, aggregate counters, opt-in crash reports) leaves your machine — never message content.

## Status

Pre-alpha. See [`docs/`](./docs) and the design plan for architecture. MVP milestones:

- [ ] `chatmem init` / `daemon` / `mcp` subcommands
- [ ] Embedded Postgres 16 + pgvector 0.8 bootstrap
- [ ] MCP stdio transport with `record_message`, `search_history`, `get_conversation`
- [ ] ONNX int8 MiniLM embedding pipeline
- [ ] Homebrew tap + direct download (macOS arm64/amd64, linux amd64)

## Quick start (once released)

```
brew tap chatmem/chatmem
brew install chatmem
chatmem init
```

`chatmem init` will provision the data directory, extract pgvector, run `initdb`, install an autostart unit, and print an MCP config snippet you can paste into your client.

## Development

Requires Go 1.26+.

```
go build ./...
go run ./cmd/chatmem --help
```

## License

Apache-2.0. See [LICENSE](./LICENSE).
