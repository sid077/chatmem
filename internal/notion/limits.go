package notion

import (
	"encoding/json"
	"unicode/utf16"
	"unicode/utf8"
)

// Notion API request limits (developers.notion.com/reference/request-limits).
// Exceeding any of them fails the whole request with HTTP 400, so every
// block constructor and every write path goes through the helpers below.
const (
	maxRichTextUnits  = 2000    // text.content per rich-text object, in UTF-16 code units
	maxRichTextRuns   = 100     // elements per rich_text array
	maxChildrenPerReq = 100     // top-level children per create/append request
	maxBlocksPerReq   = 1000    // blocks per request, nested ones included
	maxBytesPerReq    = 450_000 // payload cap is 500KB; leave room for the envelope

	// maxParagraphUnits is the most text one paragraph block can hold.
	maxParagraphUnits = maxRichTextUnits * maxRichTextRuns

	// maxTranscriptTurnBytes caps one verbatim transcript turn so its toggle
	// always fits in a single request. The full text stays in chatmem.
	maxTranscriptTurnBytes = 100_000
)

const truncatedMarker = " … [truncated]"

// splitUTF16 cuts s into pieces of at most n UTF-16 code units without
// splitting a rune.
func splitUTF16(s string, n int) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	start, units := 0, 0
	for i, r := range s {
		w := utf16.RuneLen(r)
		if w < 0 {
			w = 1 // invalid rune; json.Marshal emits U+FFFD
		}
		if units+w > n {
			out = append(out, s[start:i])
			start, units = i, 0
		}
		units += w
	}
	return append(out, s[start:])
}

// truncateBytes shortens s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// splitRuns enforces the rich-text limits on a run list: any run longer
// than maxRichTextUnits becomes several runs with the same annotations and
// link, and the list is capped at maxRichTextRuns (the last run gets a
// truncation marker). Callers that must not lose text pre-split it to
// maxParagraphUnits.
func splitRuns(runs []any) []any {
	out := make([]any, 0, len(runs))
	for _, r := range runs {
		run, ok := r.(map[string]any)
		text, ok2 := run["text"].(map[string]any)
		content, ok3 := text["content"].(string)
		if !ok || !ok2 || !ok3 {
			out = append(out, r)
			continue
		}
		pieces := splitUTF16(content, maxRichTextUnits)
		if len(pieces) == 1 {
			out = append(out, r)
			continue
		}
		for _, p := range pieces {
			nt := make(map[string]any, len(text))
			for k, v := range text {
				nt[k] = v
			}
			nt["content"] = p
			nr := make(map[string]any, len(run))
			for k, v := range run {
				nr[k] = v
			}
			nr["text"] = nt
			out = append(out, nr)
		}
	}
	if len(out) > maxRichTextRuns {
		out = out[:maxRichTextRuns]
		if last, ok := out[len(out)-1].(map[string]any); ok {
			if text, ok := last["text"].(map[string]any); ok {
				c, _ := text["content"].(string)
				keep := splitUTF16(c, maxRichTextUnits-utf16Len(truncatedMarker))[0]
				nt := map[string]any{}
				for k, v := range text {
					nt[k] = v
				}
				nt["content"] = keep + truncatedMarker
				nl := map[string]any{}
				for k, v := range last {
					nl[k] = v
				}
				nl["text"] = nt
				out[len(out)-1] = nl
			}
		}
	}
	return out
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

// countBlocks counts b plus every nested child, which is what Notion's
// per-request block limit measures.
func countBlocks(b Block) int {
	n := 1
	t, _ := b["type"].(string)
	body, _ := b[t].(map[string]any)
	kids, _ := body["children"].([]any)
	for _, k := range kids {
		switch c := k.(type) {
		case Block:
			n += countBlocks(c)
		case map[string]any:
			n += countBlocks(Block(c))
		default:
			n++
		}
	}
	return n
}

// batchBlocks splits blocks into request-sized groups that respect the
// per-request child, nested-block and payload-size limits. Every block
// built by this package fits in one request on its own.
func batchBlocks(blocks []Block) [][]Block {
	var (
		out            [][]Block
		cur            []Block
		curN, curBytes int
	)
	for _, b := range blocks {
		n := countBlocks(b)
		raw, _ := json.Marshal(b)
		if len(cur) > 0 && (len(cur)+1 > maxChildrenPerReq || curN+n > maxBlocksPerReq || curBytes+len(raw) > maxBytesPerReq) {
			out = append(out, cur)
			cur, curN, curBytes = nil, 0, 0
		}
		cur = append(cur, b)
		curN += n
		curBytes += len(raw) + 1
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
