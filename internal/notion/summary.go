package notion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// SessionType classifies a conversation so we pick the right page template.
type SessionType string

const (
	Study SessionType = "study"
	Debug SessionType = "debug"
	Mixed SessionType = "mixed"
)

// Summary is the structured JSON payload the LLM must produce and pass to
// synthesize_to_notion. See internal/notion/prompt.go for the human-readable
// version that gets sent to the LLM.
type Summary struct {
	Title        string          `json:"title"`
	SessionType  SessionType     `json:"session_type"`
	Status       string          `json:"status,omitempty"`         // debug only
	TLDR         []string        `json:"tldr"`
	Prereqs      []string        `json:"prerequisites,omitempty"`  // study
	Concepts     []Concept       `json:"concepts,omitempty"`       // study/mixed
	Attempts     []DebugAttempt  `json:"attempts,omitempty"`       // debug/mixed
	RootCause    *Cite           `json:"root_cause,omitempty"`     // debug
	Resolution   *Resolution     `json:"resolution,omitempty"`     // debug
	Prevention   []string        `json:"prevention,omitempty"`     // debug
	Insights     []Cite          `json:"insights,omitempty"`       // study
	Diagrams     []Diagram       `json:"diagrams"`                 // required when applicable
	CodeBlocks   []CodeSample    `json:"code_blocks,omitempty"`
	References   []Reference     `json:"references,omitempty"`
	FurtherStudy []string        `json:"further_study,omitempty"`  // study
	OpenQs       []string        `json:"open_questions,omitempty"`
}

type Concept struct {
	Heading       string   `json:"heading"`
	Definition    string   `json:"definition"`               // 1-2 sentences, callout
	Body          string   `json:"body"`                     // markdown paragraph
	Example       string   `json:"example,omitempty"`
	WhyItMatters  string   `json:"why_it_matters,omitempty"`
	CitedFrom     []string `json:"cited_from"`               // message UUIDs
}

type DebugAttempt struct {
	Number      int      `json:"number"`
	Description string   `json:"description"`
	Command     string   `json:"command,omitempty"`
	Expected    string   `json:"expected"`
	Actual      string   `json:"actual"`
	Learning    string   `json:"learning"`
	CitedFrom   []string `json:"cited_from"`
}

type Cite struct {
	Text      string   `json:"text"`
	CitedFrom []string `json:"cited_from"`
}

type Resolution struct {
	Steps    []string `json:"steps"`
	Command  string   `json:"command,omitempty"`
	Language string   `json:"language,omitempty"`
	Verify   string   `json:"verify,omitempty"`
	CitedFrom []string `json:"cited_from"`
}

type Diagram struct {
	Type      string   `json:"type"`      // flowchart | sequenceDiagram | stateDiagram-v2 | timeline | erDiagram | classDiagram
	Title     string   `json:"title,omitempty"`
	Mermaid   string   `json:"mermaid"`
	CitedFrom []string `json:"cited_from,omitempty"`
}

type CodeSample struct {
	Language  string   `json:"language"`
	Content   string   `json:"content"`
	Purpose   string   `json:"purpose,omitempty"`
	CitedFrom []string `json:"cited_from,omitempty"`
}

type Reference struct {
	URL     string `json:"url"`
	Anchor  string `json:"anchor,omitempty"`
	Purpose string `json:"purpose,omitempty"`
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// architectureHint keywords that force a diagram requirement on study pages
// when concepts.length >= 3 doesn't already trigger it.
var architectureKeywords = []string{
	"system", "flow", "protocol", "pipeline", "state machine", "architecture",
	"lifecycle", "handshake", "transport", "layer", "topology", "graph",
}

// Validate enforces the quality contract before we write to Notion.
// The caller is expected to have loaded the set of valid message UUIDs
// from the store; validUUIDs is that set (map[uuid]bool).
//
// Returns a joined error listing every violation so the LLM can fix them
// in one retry.
func (s *Summary) Validate(validUUIDs map[string]bool) error {
	var problems []string

	// Basic sanity
	if strings.TrimSpace(s.Title) == "" {
		problems = append(problems, "title is required")
	} else if len(s.Title) > 200 {
		problems = append(problems, fmt.Sprintf("title too long (%d chars, max 200)", len(s.Title)))
	}
	if s.SessionType != Study && s.SessionType != Debug && s.SessionType != Mixed {
		problems = append(problems, fmt.Sprintf(`session_type must be "study" | "debug" | "mixed" (got %q)`, s.SessionType))
	}
	if len(s.TLDR) == 0 {
		problems = append(problems, "tldr must have at least 1 bullet")
	} else if len(s.TLDR) > 8 {
		problems = append(problems, fmt.Sprintf("tldr has %d bullets (max 8)", len(s.TLDR)))
	}

	// Session-type-specific requirements
	needsDiagram := false
	switch s.SessionType {
	case Debug:
		if len(s.Attempts) == 0 {
			problems = append(problems, "debug sessions must include at least 1 attempt")
		}
		if s.RootCause == nil || strings.TrimSpace(s.RootCause.Text) == "" {
			problems = append(problems, "debug sessions must include a root_cause")
		}
		if !hasDiagramType(s.Diagrams, "timeline") {
			problems = append(problems, `debug sessions must include a "timeline" mermaid diagram`)
		}
		needsDiagram = true
	case Study, Mixed:
		if len(s.Concepts) == 0 {
			problems = append(problems, "study/mixed sessions must include at least 1 concept")
		}
		if len(s.Concepts) >= 3 || anyConceptMentionsArchitecture(s.Concepts) {
			needsDiagram = true
		}
	}
	if needsDiagram && len(s.Diagrams) == 0 {
		problems = append(problems,
			"conversation requires at least one Mermaid diagram (arch/flow/state/timeline) — add a Diagrams entry")
	}

	if s.SessionType == Debug && s.Status != "" {
		switch s.Status {
		case "resolved", "partial", "unresolved":
		default:
			problems = append(problems, fmt.Sprintf(`status must be "resolved" | "partial" | "unresolved" (got %q)`, s.Status))
		}
	}

	// Concept validation
	for i, c := range s.Concepts {
		if strings.TrimSpace(c.Heading) == "" {
			problems = append(problems, fmt.Sprintf("concepts[%d].heading is empty", i))
		}
		if strings.TrimSpace(c.Definition) == "" {
			problems = append(problems, fmt.Sprintf("concepts[%d] (%q) missing definition", i, c.Heading))
		}
		if err := checkCites(fmt.Sprintf("concepts[%d]", i), c.CitedFrom, validUUIDs); err != "" {
			problems = append(problems, err)
		}
	}

	for i, a := range s.Attempts {
		if strings.TrimSpace(a.Description) == "" {
			problems = append(problems, fmt.Sprintf("attempts[%d].description is empty", i))
		}
		if a.Number == 0 {
			problems = append(problems, fmt.Sprintf("attempts[%d].number must be >= 1", i))
		}
		if err := checkCites(fmt.Sprintf("attempts[%d]", i), a.CitedFrom, validUUIDs); err != "" {
			problems = append(problems, err)
		}
	}

	for i, d := range s.Diagrams {
		if strings.TrimSpace(d.Mermaid) == "" {
			problems = append(problems, fmt.Sprintf("diagrams[%d].mermaid is empty", i))
		}
	}

	if s.RootCause != nil {
		if err := checkCites("root_cause", s.RootCause.CitedFrom, validUUIDs); err != "" {
			problems = append(problems, err)
		}
	}
	if s.Resolution != nil {
		if err := checkCites("resolution", s.Resolution.CitedFrom, validUUIDs); err != "" {
			problems = append(problems, err)
		}
	}
	for i, ins := range s.Insights {
		if err := checkCites(fmt.Sprintf("insights[%d]", i), ins.CitedFrom, validUUIDs); err != "" {
			problems = append(problems, err)
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("summary validation failed:\n  - %s", strings.Join(problems, "\n  - "))
}

// Hash returns a stable content fingerprint so re-synthesis can skip
// no-op writes (identical hash → skip Notion API).
func (s *Summary) Hash() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hasDiagramType(ds []Diagram, t string) bool {
	for _, d := range ds {
		if strings.EqualFold(d.Type, t) {
			return true
		}
	}
	return false
}

func anyConceptMentionsArchitecture(cs []Concept) bool {
	for _, c := range cs {
		low := strings.ToLower(c.Heading)
		for _, kw := range architectureKeywords {
			if strings.Contains(low, kw) {
				return true
			}
		}
	}
	return false
}

func checkCites(label string, cites []string, valid map[string]bool) string {
	if len(cites) == 0 {
		return fmt.Sprintf("%s must cite at least one message uuid in cited_from", label)
	}
	for _, id := range cites {
		if !uuidRE.MatchString(id) {
			return fmt.Sprintf("%s cited_from contains invalid uuid %q", label, id)
		}
		if valid != nil && !valid[strings.ToLower(id)] {
			return fmt.Sprintf("%s cited_from uuid %q is not a message in this conversation", label, id)
		}
	}
	return ""
}
