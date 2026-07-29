package notion

import "encoding/json"

// Block is a Notion block. We keep it loosely typed because Notion's block
// schema is deeply variant — every block type has different keys under a
// key that matches its type name. Rather than model all 30+ variants, we
// build them via constructors and let json.Marshal handle serialization.
type Block map[string]any

// ID returns the block id when Notion supplied one (list responses). Constructors
// leave ID empty; only Notion's server assigns ids.
func (b Block) ID() string {
	// Marshalling Block through json.Number is unnecessary — Notion returns
	// ids as strings.
	s, _ := b["id"].(string)
	return s
}

// UnmarshalJSON preserves the map shape.
func (b *Block) UnmarshalJSON(raw []byte) error {
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	*b = m
	return nil
}

// --- rich text helpers ---

// RT builds a plain rich-text run with no annotations.
func RT(content string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": map[string]any{"content": content},
	}
}

// RTBold builds a bold rich-text run.
func RTBold(content string) map[string]any {
	return map[string]any{
		"type":        "text",
		"text":        map[string]any{"content": content},
		"annotations": map[string]any{"bold": true},
	}
}

// RTCode wraps content in inline code styling.
func RTCode(content string) map[string]any {
	return map[string]any{
		"type":        "text",
		"text":        map[string]any{"content": content},
		"annotations": map[string]any{"code": true},
	}
}

// RTLink builds a rich-text run rendered as a hyperlink.
func RTLink(content, url string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": map[string]any{
			"content": content,
			"link":    map[string]any{"url": url},
		},
	}
}

// --- block constructors ---

func H1(text string) Block {
	return Block{"object": "block", "type": "heading_1",
		"heading_1": map[string]any{"rich_text": []any{RT(text)}}}
}
func H2(text string) Block {
	return Block{"object": "block", "type": "heading_2",
		"heading_2": map[string]any{"rich_text": []any{RT(text)}}}
}
func H3(text string) Block {
	return Block{"object": "block", "type": "heading_3",
		"heading_3": map[string]any{"rich_text": []any{RT(text)}}}
}

// Para writes a paragraph with the given rich-text runs.
func Para(runs ...any) Block {
	return Block{"object": "block", "type": "paragraph",
		"paragraph": map[string]any{"rich_text": runs}}
}
func ParaText(text string) Block { return Para(RT(text)) }

func Bullet(runs ...any) Block {
	return Block{"object": "block", "type": "bulleted_list_item",
		"bulleted_list_item": map[string]any{"rich_text": runs}}
}
func BulletText(text string) Block { return Bullet(RT(text)) }

func Numbered(runs ...any) Block {
	return Block{"object": "block", "type": "numbered_list_item",
		"numbered_list_item": map[string]any{"rich_text": runs}}
}
func NumberedText(text string) Block { return Numbered(RT(text)) }

// Callout with the given emoji and background color. Notion accepts:
// default, gray, brown, orange, yellow, green, blue, purple, pink, red,
// plus _background suffix variants.
func Callout(emoji, text, color string) Block {
	return Block{"object": "block", "type": "callout",
		"callout": map[string]any{
			"rich_text": []any{RT(text)},
			"icon":      map[string]any{"type": "emoji", "emoji": emoji},
			"color":     color,
		}}
}

// CalloutRich lets the caller supply pre-built rich-text runs (for links etc).
func CalloutRich(emoji string, runs []any, color string) Block {
	return Block{"object": "block", "type": "callout",
		"callout": map[string]any{
			"rich_text": runs,
			"icon":      map[string]any{"type": "emoji", "emoji": emoji},
			"color":     color,
		}}
}

// Code fence with a language tag. Notion supports language: "mermaid" and
// renders it as an interactive diagram.
func Code(language, content string) Block {
	return Block{"object": "block", "type": "code",
		"code": map[string]any{
			"language":  language,
			"rich_text": []any{RT(content)},
		}}
}
func Mermaid(source string) Block { return Code("mermaid", source) }

// Toggle with the given summary line and nested children (collapsed by default).
func Toggle(summary string, children []Block) Block {
	kids := make([]any, len(children))
	for i, c := range children {
		kids[i] = c
	}
	return Block{"object": "block", "type": "toggle",
		"toggle": map[string]any{
			"rich_text": []any{RT(summary)},
			"children":  kids,
		}}
}

func Divider() Block {
	return Block{"object": "block", "type": "divider", "divider": map[string]any{}}
}

// Quote block. Notion allows nested rich text.
func Quote(text string) Block {
	return Block{"object": "block", "type": "quote",
		"quote": map[string]any{"rich_text": []any{RT(text)}}}
}
