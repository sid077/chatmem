package notion

import (
	"fmt"
	"strings"
	"time"
)

// PromptMessage is one turn in the conversation that the LLM will summarize.
type PromptMessage struct {
	ID        string
	Role      string
	Content   string
	CreatedAt time.Time
}

// PromptConversation is the input to BuildSynthesisPrompt — everything the LLM
// needs to compose a Summary payload for one conversation.
type PromptConversation struct {
	ConversationID string
	Model          string
	Provider       string
	ClientID       string
	Messages       []PromptMessage
}

// BuildSynthesisPrompt produces the plain-text string returned by the
// `get_synthesis_prompt` MCP tool. The LLM reads this, decides the session
// type, drafts a Summary JSON matching the schema, and calls
// `synthesize_to_notion(conversation_id, summary)` with it.
//
// The prompt is intentionally verbose: the quality of the resulting Notion
// page depends almost entirely on how thoroughly we instruct the LLM here.
func BuildSynthesisPrompt(c PromptConversation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# chatmem synthesis brief for conversation %s\n\n", c.ConversationID)
	fmt.Fprintf(&b, "You are producing a **long-lived study/reference Notion page** for a conversation the user just had with an LLM (model=%s, provider=%s, client=%s). The page will be re-read weeks later to revise concepts or replay a debugging session. Quality matters more than brevity.\n\n",
		c.Model, c.Provider, c.ClientID)

	b.WriteString(`## What to do

1. Read the full transcript below.
2. Classify session_type as "study" | "debug" | "mixed" using the signals below.
3. Draft a Summary JSON payload matching the schema in this brief.
4. Call the ` + "`synthesize_to_notion`" + ` MCP tool with:
   { "conversation_id": "<the id at the top of this brief>",
     "summary": <your Summary object> }

## Session-type signals

- **debug**: presence of error messages, stack traces, shell commands, "that didn't work" iteration, file paths, config values, a specific broken thing being made to work.
- **study**: question-and-answer flow, "what is / explain / how does", conceptual definitions, worked examples, no shell commands or errors.
- **mixed**: both patterns present in one conversation.

## Quality rules (the page WILL be rejected if you violate these)

1. **Concept-first, not turn-first.** Reorganize by what was learned, not who said what.
2. **Every non-trivial claim MUST cite the message UUID it came from** via ` + "`cited_from: [\"<uuid>\", ...]`" + `. UUIDs are the ones tagged before each message in the transcript below.
3. **Include a Mermaid diagram** if:
   - session_type is "debug" (add a ` + "`timeline`" + ` diagram of your attempts — REQUIRED for debug)
   - session_type is "study" AND concepts.length >= 3 OR any concept mentions architecture/flow/protocol/pipeline/state/lifecycle/handshake
   - use flowchart / sequenceDiagram / stateDiagram-v2 / timeline / erDiagram / classDiagram as fits
4. **Concept structure**: each Concept needs (heading, 1-2 sentence definition, longer body, optional example, optional why-it-matters).
5. **Debug structure**: each Attempt is a numbered step with description + command + expected + actual + learning. Then a single root_cause. Then a resolution with reproducible steps and a verify hint.
6. **Corrections**: if the conversation later corrected an earlier claim, capture the FINAL truth. Use insights[] to record the correction itself — "initial assumption was X; the reality is Y because Z". Do not present the wrong version as truth.
7. **No invented content.** If the conversation didn't cover something, omit that section. Empty is fine.
8. **Verbatim quotes for anything factual.** Paraphrase for narrative, quote for facts (code, error strings, exact commands, precise numbers).
9. **Title**: a specific, searchable noun phrase (~4-10 words). "Debugging" or "Notes on X" are bad; "HNSW cosine index build-time trade-offs" is good.
10. **TLDR**: 3-5 bullets that let a reader decide in 15 seconds whether to keep reading.

## Summary JSON schema

` + "```json" + `
{
  "title":         "string, 4-10 words, searchable",
  "session_type":  "study" | "debug" | "mixed",
  "status":        "resolved" | "partial" | "unresolved",   // DEBUG ONLY

  "tldr":          ["3-5 short bullets"],

  "prerequisites": ["optional; assumed knowledge for study pages"],

  "concepts": [                                             // study/mixed
    {
      "heading":        "Concept name",
      "definition":     "1-2 sentences; will be rendered as a blue callout",
      "body":           "Longer explanation. Markdown paragraphs (\\n\\n). No headings inside.",
      "example":        "Optional concrete example",
      "why_it_matters": "Optional one-line 'so what'",
      "cited_from":     ["<msg uuid>", ...]
    }
  ],

  "attempts": [                                             // debug/mixed
    {
      "number":      1,
      "description": "What I tried, one line",
      "command":     "verbatim command / snippet, optional",
      "expected":    "what I thought would happen",
      "actual":      "what actually happened",
      "learning":    "what I concluded from this attempt",
      "cited_from":  ["<msg uuid>", ...]
    }
  ],

  "root_cause": {                                           // debug/mixed
    "text":       "The actual reason it broke, in prose.",
    "cited_from": ["<msg uuid>", ...]
  },

  "resolution": {                                           // debug/mixed
    "steps":      ["numbered steps to fix"],
    "command":    "optional exact command/diff",
    "language":   "bash | yaml | go | ... (default bash)",
    "verify":     "how to confirm the fix worked",
    "cited_from": ["<msg uuid>", ...]
  },

  "prevention":   ["how to avoid re-hitting this"],         // debug

  "insights": [                                             // study
    { "text": "Non-obvious insight or correction", "cited_from": ["<uuid>"] }
  ],

  "diagrams": [                                             // required when applicable
    {
      "type":      "flowchart" | "sequenceDiagram" | "stateDiagram-v2" | "timeline" | "erDiagram" | "classDiagram",
      "title":     "optional heading above the diagram",
      "mermaid":   "raw Mermaid source; no fences",
      "cited_from": ["<uuid>", ...]
    }
  ],

  "code_blocks": [
    { "language": "go | bash | sql | ...", "content": "code", "purpose": "one-line why", "cited_from": [...] }
  ],

  "references": [
    { "url": "https://...", "anchor": "short text", "purpose": "why it's relevant" }
  ],

  "further_study":   ["what to read next"],                 // study
  "open_questions":  ["stuff unresolved in this conversation"]
}
` + "```" + `

## Transcript

The transcript below is the ground truth. Each message is tagged with its UUID; use those exact UUIDs in ` + "`cited_from`" + `.

`)

	for _, m := range c.Messages {
		fmt.Fprintf(&b, "--- [%s] role=%s @ %s ---\n%s\n\n",
			m.ID, m.Role, m.CreatedAt.Format("2006-01-02 15:04:05"), m.Content)
	}

	b.WriteString("\n## Ready\n\nCompose the Summary JSON now and call `synthesize_to_notion`.\n")
	return b.String()
}

// ValidUUIDSet builds the map[string]bool used by Summary.Validate to
// verify each cited_from UUID actually appears in this conversation.
func ValidUUIDSet(msgs []PromptMessage) map[string]bool {
	out := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		out[strings.ToLower(m.ID)] = true
	}
	return out
}
