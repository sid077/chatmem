package notion

import (
	"fmt"
	"strings"
	"time"
)

// RenderMeta is the identity + provenance info chatmem stitches onto
// every page's header callout. Kept separate from Summary so the LLM
// doesn't have to know or invent these values.
type RenderMeta struct {
	ConversationID string
	Model          string
	Provider       string
	ClientID       string
	SynthesizedAt  time.Time
	Version        int // 1 for first synth, N+1 for each re-synth
}

// Transcript is the verbatim message log we append at the bottom of every
// page in collapsed toggles. Kept separate so the LLM doesn't include it
// in the Summary (they'd waste tokens quoting themselves).
type Transcript struct {
	Turns []TranscriptTurn
}
type TranscriptTurn struct {
	MessageID string
	Role      string
	Content   string
	CreatedAt time.Time
}

// Render produces the ordered []Block for a page given a Summary + meta + transcript.
func Render(s Summary, meta RenderMeta, tx Transcript) []Block {
	var b []Block

	// Metadata callout: greyish, always at top
	b = append(b, buildMetadataCallout(s, meta))
	b = append(b, Divider())

	// Session-type-specific body
	switch s.SessionType {
	case Debug:
		b = append(b, renderDebug(s)...)
	case Study:
		b = append(b, renderStudy(s)...)
	case Mixed:
		b = append(b, renderStudy(s)...)
		b = append(b, Divider())
		b = append(b, renderDebug(s)...)
	}

	// Shared trailer sections
	if len(s.References) > 0 {
		b = append(b, H2("References"))
		for _, r := range s.References {
			label := r.Anchor
			if label == "" {
				label = r.URL
			}
			runs := []any{RTLink(label, r.URL)}
			if r.Purpose != "" {
				runs = append(runs, RT(" — "+r.Purpose))
			}
			b = append(b, Bullet(runs...))
		}
	}

	if len(s.OpenQs) > 0 {
		b = append(b, H2("Open Questions"))
		for _, q := range s.OpenQs {
			b = append(b, BulletText(q))
		}
	}

	// Full Transcript — always last, collapsed
	if len(tx.Turns) > 0 {
		b = append(b, Divider())
		b = append(b, H2("Full Transcript"))
		b = append(b, ParaText(fmt.Sprintf("%d turn(s) — chatmem conversation %s. Expand any turn to read the original message content verbatim.",
			len(tx.Turns), meta.ConversationID)))
		for _, t := range tx.Turns {
			summary := fmt.Sprintf("%s @ %s", t.Role, t.CreatedAt.Format("2006-01-02 15:04:05"))
			body := paragraphsFromText(t.Content)
			b = append(b, Toggle(summary, body))
		}
	}

	return b
}

func buildMetadataCallout(s Summary, meta RenderMeta) Block {
	lines := []string{
		fmt.Sprintf("Model: %s / %s · Client: %s", meta.Provider, meta.Model, meta.ClientID),
		fmt.Sprintf("Conversation: %s", meta.ConversationID),
		fmt.Sprintf("Synthesized: %s · Version %d · Session type: %s",
			meta.SynthesizedAt.Format("2006-01-02 15:04 MST"),
			meta.Version, s.SessionType),
	}
	if s.SessionType == Debug && s.Status != "" {
		lines = append(lines, "Status: "+s.Status)
	}
	return Callout("📋", strings.Join(lines, "\n"), "gray_background")
}

func renderStudy(s Summary) []Block {
	var b []Block
	if len(s.TLDR) > 0 {
		b = append(b, H2("TL;DR"))
		for _, t := range s.TLDR {
			b = append(b, BulletText(t))
		}
	}
	if len(s.Prereqs) > 0 {
		b = append(b, H2("Prerequisites"))
		for _, p := range s.Prereqs {
			b = append(b, BulletText(p))
		}
	}
	if len(s.Concepts) > 0 {
		b = append(b, H2("Core Concepts"))
		for i, c := range s.Concepts {
			b = append(b, H3(fmt.Sprintf("%d. %s", i+1, c.Heading)))
			b = append(b, Callout("📘", "Definition: "+c.Definition, "blue_background"))
			b = append(b, paragraphsFromText(c.Body)...)
			if c.Example != "" {
				b = append(b, Para(RTBold("Example: "), RT(c.Example)))
			}
			if c.WhyItMatters != "" {
				b = append(b, Callout("💡", "Why it matters: "+c.WhyItMatters, "yellow_background"))
			}
			if cite := formatCite(c.CitedFrom); cite != "" {
				b = append(b, Quote(cite))
			}
		}
	}
	if len(s.Diagrams) > 0 {
		b = append(b, H2("Diagrams"))
		for _, d := range s.Diagrams {
			if d.Title != "" {
				b = append(b, H3(d.Title))
			}
			b = append(b, Mermaid(d.Mermaid))
		}
	}
	if len(s.Insights) > 0 {
		b = append(b, H2("Key Insights"))
		for _, ins := range s.Insights {
			text := ins.Text
			if cite := formatCite(ins.CitedFrom); cite != "" {
				text = ins.Text + "\n\n" + cite
			}
			b = append(b, Callout("🟡", text, "yellow_background"))
		}
	}
	if len(s.CodeBlocks) > 0 {
		b = append(b, H2("Code / Commands"))
		for _, c := range s.CodeBlocks {
			b = append(b, Code(c.Language, c.Content))
			if c.Purpose != "" {
				b = append(b, ParaText("Purpose: "+c.Purpose))
			}
		}
	}
	if len(s.FurtherStudy) > 0 {
		b = append(b, H2("Further Study"))
		for _, f := range s.FurtherStudy {
			b = append(b, BulletText(f))
		}
	}
	return b
}

func renderDebug(s Summary) []Block {
	var b []Block
	if len(s.TLDR) > 0 {
		b = append(b, H2("TL;DR"))
		for _, t := range s.TLDR {
			b = append(b, BulletText(t))
		}
	}

	// Status callout — colored by outcome so the reader can scan a page and
	// instantly know if it's worth re-reading.
	if s.Status != "" {
		emoji, color := "❓", "gray_background"
		switch s.Status {
		case "resolved":
			emoji, color = "✅", "green_background"
		case "partial":
			emoji, color = "🟨", "yellow_background"
		case "unresolved":
			emoji, color = "🟥", "red_background"
		}
		b = append(b, Callout(emoji, "Status: "+s.Status, color))
	}

	// Timeline diagram is required for debug; render before the details.
	for _, d := range s.Diagrams {
		if strings.EqualFold(d.Type, "timeline") {
			b = append(b, H2("Timeline"))
			b = append(b, Mermaid(d.Mermaid))
			break
		}
	}

	if len(s.Attempts) > 0 {
		b = append(b, H2("What I Tried"))
		for _, a := range s.Attempts {
			b = append(b, H3(fmt.Sprintf("Attempt %d: %s", a.Number, a.Description)))
			if a.Command != "" {
				b = append(b, Code("bash", a.Command))
			}
			if a.Expected != "" {
				b = append(b, Para(RTBold("Expected: "), RT(a.Expected)))
			}
			if a.Actual != "" {
				b = append(b, Para(RTBold("Actual: "), RT(a.Actual)))
			}
			if a.Learning != "" {
				b = append(b, Para(RTBold("Learning: "), RT(a.Learning)))
			}
			if cite := formatCite(a.CitedFrom); cite != "" {
				b = append(b, Quote(cite))
			}
		}
	}

	if s.RootCause != nil {
		b = append(b, H2("Root Cause"))
		b = append(b, Callout("🔎", s.RootCause.Text, "green_background"))
		if cite := formatCite(s.RootCause.CitedFrom); cite != "" {
			b = append(b, Quote(cite))
		}
	}

	if s.Resolution != nil {
		b = append(b, H2("Resolution"))
		for _, step := range s.Resolution.Steps {
			b = append(b, NumberedText(step))
		}
		if s.Resolution.Command != "" {
			lang := s.Resolution.Language
			if lang == "" {
				lang = "bash"
			}
			b = append(b, Code(lang, s.Resolution.Command))
		}
		if s.Resolution.Verify != "" {
			b = append(b, Callout("✅", "Verify: "+s.Resolution.Verify, "green_background"))
		}
	}

	if len(s.Prevention) > 0 {
		b = append(b, H2("Prevention"))
		for _, p := range s.Prevention {
			b = append(b, BulletText(p))
		}
	}

	// Non-timeline diagrams (arch, sequence, etc) get their own section
	other := 0
	for _, d := range s.Diagrams {
		if !strings.EqualFold(d.Type, "timeline") {
			other++
		}
	}
	if other > 0 {
		b = append(b, H2("Diagrams"))
		for _, d := range s.Diagrams {
			if strings.EqualFold(d.Type, "timeline") {
				continue
			}
			if d.Title != "" {
				b = append(b, H3(d.Title))
			}
			b = append(b, Mermaid(d.Mermaid))
		}
	}
	return b
}

// paragraphsFromText splits markdown-flavored body text into one paragraph
// per double-newline block. Nothing fancy — Notion doesn't render markdown
// natively so we skip inline styling and just preserve paragraph breaks.
func paragraphsFromText(s string) []Block {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n\n")
	out := make([]Block, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, ParaText(p))
	}
	return out
}

func formatCite(uuids []string) string {
	if len(uuids) == 0 {
		return ""
	}
	short := make([]string, 0, len(uuids))
	for _, id := range uuids {
		if len(id) >= 8 {
			short = append(short, "msg:"+id[:8])
		} else {
			short = append(short, "msg:"+id)
		}
	}
	return "Cited: " + strings.Join(short, ", ")
}
