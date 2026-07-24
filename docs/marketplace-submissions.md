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
| `wong2/awesome-mcp-servers` PR | ⚠️ branch pushed, click compare URL to open | https://github.com/wong2/awesome-mcp-servers/compare/main...sid077:awesome-mcp-servers-wong2:add-chatmem?expand=1 |
| Smithery | ⏳ needs user OAuth submission | https://smithery.ai/new |
| PulseMCP | ⏳ needs user form submission | https://www.pulsemcp.com/submit |
| Glama | ⏳ auto-indexed via GitHub topics (24h) | check https://glama.ai/mcp/servers/sid077/chatmem |

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

## 3. `wong2/awesome-mcp-servers` — same entry, different repo

```bash
gh repo fork wong2/awesome-mcp-servers --clone --remote
```

That list uses a slightly simpler format (no OS icons); use this line
(check the target section first — usually "Databases" or "Memory"):

```markdown
- [chatmem](https://github.com/sid077/chatmem) - Local LLM chat history served over MCP. Embedded Postgres + pgvector, cross-tool memory, everything stays on your machine.
```

Same PR title + body as above.

---

## 4. Smithery — https://smithery.ai/new

Once the PR at step 2 is merged AND `smithery.yaml` is in the repo root
(it is — see `/smithery.yaml`), submit at https://smithery.ai/new:

1. Sign in with GitHub.
2. Paste `https://github.com/sid077/chatmem` as the repo URL.
3. Smithery auto-detects `smithery.yaml`. Accept the auto-filled fields.
4. Category: **Memory / Knowledge**. Tags: `memory`, `local`, `postgres`, `pgvector`.
5. Submit.

Approval takes ~1 business day. After approval, the badge in the README
starts working and users get a one-click install button on
`https://smithery.ai/server/@sid077/chatmem`.

---

## 5. PulseMCP — https://www.pulsemcp.com/submit

Form-based. Fill in:

- **Name:** chatmem
- **GitHub URL:** https://github.com/sid077/chatmem
- **Category:** Memory / Knowledge Management
- **Description (short):** Local LLM chat history served over MCP. Embedded Postgres + pgvector on your own machine, no data ever leaves.
- **Description (long):** paste the "What it is" paragraph from step 2.
- **Install command:** `brew install --cask sid077/chatmem/chatmem` (mac) / `sudo zypper ar https://sid077.github.io/chatmem/chatmem.repo && sudo zypper in chatmem` (opensuse) / see releases for other distros
- **License:** Apache-2.0
- **Author / contact:** siddhant.d777@gmail.com

Editorial review — usually 2–5 days to appear.

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
