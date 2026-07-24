# Marketplace / Directory submissions

Copy-paste playbook to get chatmem listed everywhere it belongs.
Ordered easiest → highest-effort.

---

## 1. GitHub topics (✅ already done — verify at any time)

```bash
gh api repos/sid077/chatmem --jq '.topics'
# should include: mcp, mcp-server, model-context-protocol, claude-code,
#                 cursor, windsurf, postgres, pgvector, llm-memory, go
```

Auto-indexers that scrape by topic: Glama (glama.ai/mcp/servers), GitHub's
own search, PulseMCP's crawler. No further action.

---

## Status snapshot (2026-07-24)

| Directory | Status | Link |
|-----------|--------|------|
| GitHub topics | ✅ set on `sid077/chatmem` | — |
| `punkpeye/awesome-mcp-servers` PR | ✅ open | https://github.com/punkpeye/awesome-mcp-servers/pull/10819 |
| `wong2/awesome-mcp-servers` — via https://mcpservers.org/submit | ⏳ web form; wong2 doesn't accept PRs | https://mcpservers.org/submit |
| Smithery | ❌ skipped — see note below | — |
| Official MCP registry (registry.modelcontextprotocol.io) | ❌ skipped for v0.1.x — see note below | — |
| PulseMCP | ❌ blocked on official MCP registry publish (they auto-ingest from registry.modelcontextprotocol.io weekly; no direct submissions) | — |
| Glama | ⏳ auto-indexed via GitHub topics (24h) | check https://glama.ai/mcp/servers/sid077/chatmem |
| `.well-known/mcp/server-card.json` | ✅ served on gh-pages, kept in sync by release workflow | https://sid077.github.io/chatmem/.well-known/mcp/server-card.json |

### Why Smithery + official registry are skipped

Both registries currently register servers by either **hosting them** (HTTP MCP URL) or **spawning a package** in their sandbox (`npx`, `uvx`, `dnx`, `cargo install`, OCI pull, or MCPB download). chatmem ships as a **native binary via `brew` / `zypper` / `dnf` / `apt`**, which is a first-class distribution model for local-first CLIs but is not a first-class package type for either registry today.

Re-attempt when *any* of:

- Smithery adds registration for stdio + user-installed binaries with a server-card URL as the metadata source.
- The official registry adds a `registryType: brew | apt | rpm` package type — being tracked in `modelcontextprotocol/registry` issues.
- We ship a thin npm/pip wrapper (`@sid077/chatmem-mcp`) that downloads and execs the native binary. Registry then classifies us as npm/pypi. This is ~30 lines of JS but adds a permanent maintenance surface — worth doing only if the awesome-mcp / Glama / direct-install traffic clearly isn't enough.
- We ship an OCI image via GHCR. Contradicts "local Postgres per user" without a volume mount ceremony; not recommended for chatmem specifically.

For now the intended discovery path is: awesome-mcp lists → GitHub repo → README → `brew install --cask` / `zypper in`. Real users find and install chatmem the same way they find any modern CLI tool.

---

## 2. `punkpeye/awesome-mcp-servers` — Knowledge & Memory section

Fork → edit → PR.

```bash
# 1. Fork on GitHub (or via gh):
gh repo fork punkpeye/awesome-mcp-servers --clone --remote

# 2. Add the entry under the '### 🧠 Knowledge & Memory' section, alphabetized
#    by owner/repo:
```

The entry to paste (single line, keep alphabetical order in the section):

```markdown
- [sid077/chatmem](https://github.com/sid077/chatmem) 🏎️ 🏠 🍎 🐧 - Local LLM chat history for MCP clients (Claude Code, Cursor, Windsurf, aider). Stores every recorded conversation in an embedded Postgres 18 + pgvector database on your own machine and serves relevant snippets back via keyword search (semantic re-ranking on the way). Fully local — only anonymous usage counters ever leave. Install: `brew install --cask sid077/chatmem/chatmem` or `sudo zypper ar https://sid077.github.io/chatmem/chatmem.repo && sudo zypper in chatmem`.
```

PR title:

```
Add chatmem: local LLM chat history over MCP (Postgres + pgvector, self-hosted)
```

PR body:

```markdown
Adds chatmem to the Knowledge & Memory section.

**What it is:** a single-binary MCP server (Go) that captures LLM chat
turns from any MCP client (Claude Code, Cursor, Windsurf, aider) into
an embedded Postgres 18 + pgvector database on the user's own machine,
then serves relevant snippets back via `search_history`. Everything
stays local — only anonymous usage counters (install id, per-tool call
counts, latency percentiles) leave the machine.

**Tools exposed:** `record_message`, `search_history`, `get_conversation`.

**Distribution:** Homebrew cask (`sid077/homebrew-chatmem`), zypper/dnf
via a self-hosted GitHub Pages RPM repo, direct .deb + tarball on
each GitHub Release. macOS arm64 + Linux amd64/arm64 verified end-to-end.

**License:** Apache-2.0.
```

---

## 3. `wong2/awesome-mcp-servers` — submit via https://mcpservers.org/submit

**Note (2026-07-24):** wong2 disabled PRs and routes all submissions to the
mcpservers.org web form. A leftover fork/branch exists at
`sid077/awesome-mcp-servers-wong2:add-chatmem` — harmless, ignore.

Fill in the form at https://mcpservers.org/submit:

- **Server Name:** `chatmem`
- **Short Description:** `Local LLM chat history served over MCP. Embedded Postgres + pgvector, cross-tool memory, everything stays on your machine.`
- **Link:** `https://github.com/sid077/chatmem`
- **Category:** `Knowledge & Memory` (or the closest option)
- **Contact Email:** your email

Submit. Approval typically shows up in the wong2 list + mcpservers.org
directory within a few days.

---

## 4. Smithery — https://smithery.ai/new

Smithery auto-scans by spawning the server, which doesn't work for a
locally-installed binary like chatmem. Instead we advertise a
`.well-known/mcp/server-card.json` on our GitHub Pages so Smithery
reads the tool schemas from there.

**The server-card is already live** at
https://sid077.github.io/chatmem/.well-known/mcp/server-card.json
and the release workflow keeps it in sync on every tag push.

Submission steps at https://smithery.ai/new:

1. Sign in with GitHub.
2. Paste `https://github.com/sid077/chatmem` as the repo URL.
3. If Smithery still errors on the auto-scan, explicitly point it at the
   card URL above (some flows accept a "server card URL" override).
4. Category: **Memory / Knowledge**. Tags: `memory`, `local`, `postgres`, `pgvector`.
5. Submit.

Approval takes ~1 business day. After approval, the badge in the README
starts working and users get a one-click install button on
`https://smithery.ai/server/@sid077/chatmem`.

---

## 5. PulseMCP

**Status (2026-07-24):** PulseMCP no longer takes direct submissions. From
their submit page: *"We ingest entries from the Official MCP Registry daily
and process them weekly. If it has been a week since you published there,
or want to make other adjustments to your listing on pulsemcp.com, please
email us at hello@pulsemcp.com"*.

Chatmem is blocked here on the same reason as Smithery + the official
registry: no native-binary package type. Re-attempt path is identical to
Smithery (see step 4 note): ship an MCPB package or npm wrapper, publish
to registry.modelcontextprotocol.io, PulseMCP picks it up on the next
weekly ingest.

Manual editorial contact (`hello@pulsemcp.com`) is available for edits
if we ever do get listed.

---

## 6. Glama — auto-indexed

Already covered by the GitHub topics from step 1. Glama scrapes any
repo tagged `mcp-server` daily. Expect the listing at
`https://glama.ai/mcp/servers/sid077/chatmem` within 24h; nothing to
submit.

Once indexed, drop the Glama badge into README:

```markdown
[![glama badge](https://glama.ai/mcp/servers/sid077/chatmem/badges/score.svg)](https://glama.ai/mcp/servers/sid077/chatmem)
```

---

## 7. Later — modelcontextprotocol/servers (official, high bar)

Skip for now. The official list under `modelcontextprotocol/servers`
prefers "reference implementation" quality with signed builds and real
semantic search. Revisit after:

- macOS binary is Developer-ID signed + notarized (removes Gatekeeper prompt)
- ONNX MiniLM embedder ships (real semantic search, not keyword FTS)

Then open a PR against the `community-servers` section of that repo's
README with the same paragraph as step 2.
