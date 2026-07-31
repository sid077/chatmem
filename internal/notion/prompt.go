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

// FactRecord is the caller-facing version of internal/store.Fact. Repeated
// here to avoid a store→notion import cycle.
type FactRecord struct {
	MessageID  string
	Category   string
	Text       string
	Importance string
}

// ExtractionInput is what get_extraction_prompt returns to the LLM.
type ExtractionInput struct {
	ConversationID string
	Model          string
	Provider       string
	ClientID       string
	Messages       []PromptMessage // ONLY unextracted messages, chunk-limited
	TotalMessages  int
	AlreadyExtractedFacts int
	Remaining      int // # messages still unextracted after this chunk
}

// BuildExtractionPrompt returns the plain-text brief for the extraction
// phase. The LLM emits facts via record_facts; extraction is chunked so
// long conversations don't overflow the LLM's context.
func BuildExtractionPrompt(in ExtractionInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# chatmem: extract atomic facts (chunk of %d unextracted messages)\n\n", len(in.Messages))
	fmt.Fprintf(&b, "Conversation: %s (model=%s / provider=%s / client=%s)\n", in.ConversationID, in.Model, in.Provider, in.ClientID)
	fmt.Fprintf(&b, "This chunk: %d messages · Total in conversation: %d · Facts already recorded: %d · After this chunk, remaining unextracted: %d.\n\n",
		len(in.Messages), in.TotalMessages, in.AlreadyExtractedFacts, in.Remaining)

	b.WriteString(`## What to do

For each message in the transcript below, extract every atomic fact worth
remembering. A "fact" is a single specific piece of information: a concept
definition, a decision made, a command run, an error observed, a URL
referenced, an open question, a code snippet's purpose, a hint for a
diagram. Aim for **completeness over brevity** — this is the extraction
phase; compression happens in the next phase (synthesis).

Then call the ` + "`record_facts`" + ` MCP tool with:
  {
    "conversation_id": "<top of this brief>",
    "facts": [
      { "message_id": "<uuid from the transcript>",
        "category": "concept | decision | command | error | reference | question | code | diagram-hint | insight",
        "text": "the atomic fact, verbatim quote when it's a command / error / exact number",
        "importance": "critical | normal | trivial" },
      ...
    ]
  }

If there are more unextracted messages after this chunk (see 'Remaining'
above), the record_facts response will tell you — call ` + "`get_extraction_prompt`" + `
again to fetch the next chunk. Do this until Remaining reaches 0. THEN
proceed to get_synthesis_prompt.

## Rules

1. **Extract from EVERY message.** If a message really has no useful content
   (empty greetings, "ok thanks", pure noise), emit a single fact with
   importance=trivial so chatmem knows you saw it.
2. **Multiple facts per message are fine and encouraged** for substantive
   messages. Do not compress here.
3. **Verbatim quotes for anything factual**: commands, error strings, exact
   numbers, code identifiers, URLs. Paraphrasing loses precision the reader
   will need.
4. **Categories are strict**: pick one of the allowed values per fact.
   - concept: an idea, definition, principle, mechanism
   - decision: a choice made in the conversation
   - command: a shell/tool/SQL command mentioned or run
   - error: an error message or symptom
   - reference: a URL, doc name, package name, standard
   - question: a question the user or assistant raised
   - code: a code snippet worth capturing
   - diagram-hint: something that suggests a visual (state machine, flow, timeline)
   - insight: a non-obvious realization or correction
5. **Importance**:
   - critical: without this, the note is wrong or incomplete
   - normal: default; the reader would want to know this
   - trivial: filler, greetings, meta-chatter; chatmem excludes these from coverage requirements

## Transcript chunk

`)
	for _, m := range in.Messages {
		fmt.Fprintf(&b, "--- [%s] role=%s @ %s ---\n%s\n\n",
			m.ID, m.Role, m.CreatedAt.Format("2006-01-02 15:04:05"), m.Content)
	}
	b.WriteString("\n## Now\n\nEmit facts via record_facts. Do not skip any message in this chunk.\n")
	return b.String()
}

// BuildSynthesisPromptWithFacts is the v0.3.0 replacement for
// BuildSynthesisPrompt. It includes both the transcript AND the extracted
// facts, so the LLM composes the Summary from concrete building blocks
// rather than reading the transcript again.
//
// If facts is empty, this degrades to the v0.2.x prompt shape (still works,
// just without the coverage-guarantee advantage).
func BuildSynthesisPromptWithFacts(c PromptConversation, facts []FactRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# chatmem synthesis brief for conversation %s\n\n", c.ConversationID)
	fmt.Fprintf(&b, "Model=%s / provider=%s / client=%s · %d messages · %d extracted facts.\n\n",
		c.Model, c.Provider, c.ClientID, len(c.Messages), len(facts))

	if len(facts) > 0 {
		b.WriteString(`You extracted the facts below in the previous phase. **This is your inventory.** Every non-trivia fact must appear somewhere in the Summary — either as a Concept body, an Attempt line, a Root Cause, an Insight, a Code block, a Reference, or an Open Question. If you can't fit a fact anywhere, that's a sign the Summary is under-covered.

The synthesize_to_notion tool WILL REFUSE to write to Notion if coverage is below 95% (measured as messages-with-non-trivia-facts that are cited via cited_from). If that happens, the error lists which message uuids were missed — extend the Summary and call synthesize_to_notion again.

`)
	} else {
		b.WriteString(`You did NOT extract facts in a previous phase. Coverage will be measured against ALL messages — every message uuid must appear in some cited_from list. If that's too strict, cancel and call get_extraction_prompt first.

`)
	}

	b.WriteString(`## What to do

1. Read the facts inventory and the transcript below.
2. Classify session_type as "study" | "debug" | "mixed".
3. Draft a Summary JSON matching the schema in this brief.
4. Call ` + "`synthesize_to_notion`" + ` with:
   { "conversation_id": "<the id at the top>",
     "summary": <your Summary object> }
5. If coverage fails, read the returned "missed_message_ids" list and add
   coverage for those messages (either by citing them in an existing
   section or adding a new section). Then call synthesize_to_notion again.

## Session-type signals

- **debug**: presence of error messages, stack traces, shell commands, iteration on a broken thing.
- **study**: question-and-answer, conceptual definitions, worked examples, no shell commands.
- **mixed**: both.

## Quality rules

1. **Concept-first**, not turn-first.
2. **Cite every non-trivial claim** with source msg uuid in cited_from.
3. **Include a Mermaid diagram** when applicable:
   - debug → REQUIRED timeline diagram
   - study → required when concepts ≥ 3 OR any concept mentions architecture/flow/protocol/pipeline/state/lifecycle/handshake
   - use flowchart / sequenceDiagram / stateDiagram-v2 / timeline / erDiagram / classDiagram
4. Concept structure: heading + 1-2 sentence definition + longer body + optional example + optional why-it-matters + citations.
5. Debug structure: numbered Attempts (each with command + expected + actual + learning) + Root Cause + Resolution steps + Prevention.
6. Corrections: capture the FINAL truth in the body; record the correction itself as an Insight ("initial X → actually Y because Z").
7. **No invented content.** If the conversation didn't cover something, omit that section.
8. Verbatim quotes for factual bits (commands, errors, exact numbers).
9. Title: 4-10 words, specific, searchable.
10. TLDR: 3-5 bullets a reader can scan in 15s.

## Summary JSON schema

` + "```json" + `
{
  "title":         "string, 4-10 words",
  "session_type":  "study" | "debug" | "mixed",
  "status":        "resolved" | "partial" | "unresolved",   // DEBUG ONLY
  "tldr":          ["3-5 short bullets"],
  "prerequisites": ["optional; assumed knowledge for study pages"],
  "concepts": [
    {
      "heading":        "Concept name",
      "definition":     "1-2 sentences; blue-callout",
      "body":           "Longer explanation, markdown paragraphs.",
      "example":        "Optional example",
      "why_it_matters": "Optional one-liner",
      "cited_from":     ["<msg uuid>", ...]
    }
  ],
  "attempts": [
    {
      "number":      1,
      "description": "What I tried",
      "command":     "verbatim, optional",
      "expected":    "expected outcome",
      "actual":      "actual outcome",
      "learning":    "what I concluded",
      "cited_from":  ["<uuid>", ...]
    }
  ],
  "root_cause": { "text": "...", "cited_from": ["<uuid>"] },
  "resolution": { "steps": ["step 1", ...], "command": "...", "language": "bash", "verify": "...", "cited_from": ["<uuid>"] },
  "prevention":   ["..."],
  "insights":     [ { "text": "...", "cited_from": ["<uuid>"] } ],
  "diagrams": [
    { "type": "flowchart|sequenceDiagram|stateDiagram-v2|timeline|erDiagram|classDiagram",
      "title": "optional heading", "mermaid": "raw source, no fences",
      "cited_from": ["<uuid>"] }
  ],
  "code_blocks":  [ { "language": "go", "content": "...", "purpose": "...", "cited_from": [...] } ],
  "references":   [ { "url": "https://...", "anchor": "text", "purpose": "..." } ],
  "further_study":  ["..."],
  "open_questions": ["..."]
}
` + "```" + `

`)

	// Facts inventory
	if len(facts) > 0 {
		b.WriteString("## Facts inventory (from extraction phase)\n\n")
		var lastMsg string
		for _, f := range facts {
			if f.MessageID != lastMsg {
				fmt.Fprintf(&b, "\n[msg %s]\n", f.MessageID)
				lastMsg = f.MessageID
			}
			fmt.Fprintf(&b, "  - [%s / %s] %s\n", f.Category, f.Importance, f.Text)
		}
		b.WriteString("\n")
	}

	// Transcript
	b.WriteString("## Transcript\n\n")
	for _, m := range c.Messages {
		fmt.Fprintf(&b, "--- [%s] role=%s @ %s ---\n%s\n\n",
			m.ID, m.Role, m.CreatedAt.Format("2006-01-02 15:04:05"), m.Content)
	}
	b.WriteString("\n## Ready\n\nCompose the Summary JSON and call `synthesize_to_notion`.\n")
	return b.String()
}
