package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Writer is the top-level façade: it validates a Summary, renders it into
// Notion blocks, and writes/updates the page. Callers should hold one per
// process; it's cheap and safe to reuse.
type Writer struct {
	cfg       *Config
	client    *Client
	dataHome  string
	onPublish PublishHook
}

// NewWriter loads notion.json from dataHome. Returns (nil, nil) if notion
// is not configured — callers treat that as "notion synthesis disabled".
// notionBaseURL is exposed for tests; pass "" for the real API.
func NewWriter(dataHome, notionBaseURL string) (*Writer, error) {
	cfg, err := LoadConfig(dataHome)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	return &Writer{
		cfg:      cfg,
		client:   NewClient(cfg.IntegrationToken, notionBaseURL),
		dataHome: dataHome,
	}, nil
}

func (w *Writer) Config() Config { return *w.cfg }

// SynthesizeIn is what callers pass to Synthesize.
type SynthesizeIn struct {
	Summary     Summary
	Meta        RenderMeta
	Transcript  Transcript

	// ExistingPageID + PreviousHash together allow re-synthesis to skip
	// Notion API calls when the summary hasn't changed.
	ExistingPageID string
	PreviousHash   string

	// Force overrides the "hash unchanged, skip" optimization.
	Force bool
}

// SynthesizeOut carries the resulting page identity back to the caller,
// which persists it via store.RecordSynthesis.
type SynthesizeOut struct {
	PageID      string
	URL         string
	SummaryHash string
	Skipped     bool // true when hash matched and we didn't touch Notion
}

// Synthesize is the single entry point. Validates the Summary, renders it
// to blocks, and creates or replaces the Notion page. On failure, saves the
// payload to <data>/notion-pending/<conversation-id>.json for later retry.
//
// If the error happens after a new page was created, the returned
// *SynthesizeOut is non-nil and carries that page's id and URL (with an
// empty SummaryHash). Callers must persist it: the page exists, and the
// next write has to update it rather than create another one.
func (w *Writer) Synthesize(ctx context.Context, in SynthesizeIn) (*SynthesizeOut, error) {
	hash := in.Summary.Hash()
	if !in.Force && in.ExistingPageID != "" && hash == in.PreviousHash {
		w.clearPending(in.Meta.ConversationID)
		return &SynthesizeOut{PageID: in.ExistingPageID, SummaryHash: hash, Skipped: true}, nil
	}

	blocks := Render(in.Summary, in.Meta, in.Transcript)

	var (
		pageID = in.ExistingPageID
		url    string
	)
	if pageID == "" {
		// Create carries the first request-sized batch inline; the rest
		// is appended below.
		var first []Block
		if batches := batchBlocks(blocks); len(batches) > 0 {
			first = batches[0]
		}
		page, err := w.client.CreatePage(ctx, w.cfg.ParentPageID, in.Summary.Title, first)
		if err != nil {
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion create page: %w", err)
		}
		pageID, url = page.ID, page.URL
		if url == "" {
			url = pageURL(pageID)
		}
		// From here on the page exists: a retry must update it in place.
		in.ExistingPageID = pageID
		if rest := blocks[len(first):]; len(rest) > 0 {
			if err := w.client.AppendChildren(ctx, pageID, rest); err != nil {
				w.persistPending(in, hash)
				return &SynthesizeOut{PageID: pageID, URL: url}, fmt.Errorf("notion append remainder: %w", err)
			}
		}
	} else {
		// Update-in-place: rewrite title (may have changed on re-synth) + body.
		if err := w.client.UpdatePageTitle(ctx, pageID, in.Summary.Title); err != nil {
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion update title: %w", err)
		}
		if err := w.client.ReplacePageBody(ctx, pageID, blocks); err != nil {
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion replace body: %w", err)
		}
		// UpdatePageTitle doesn't return a URL; reconstruct from the page id.
		url = pageURL(pageID)
	}

	w.clearPending(in.Meta.ConversationID)
	return &SynthesizeOut{
		PageID:      pageID,
		URL:         url,
		SummaryHash: hash,
	}, nil
}

func pageURL(pageID string) string {
	return fmt.Sprintf("https://www.notion.so/%s", stripHyphens(pageID))
}

// PublishHook persists a page written by DrainPending, which runs outside
// any MCP call. out.SummaryHash is empty when the page was created but its
// body is incomplete. Callers wire it to the store.
type PublishHook func(ctx context.Context, conversationID string, sessionType SessionType, out SynthesizeOut) error

// OnPublish sets the hook DrainPending calls for each page it writes.
func (w *Writer) OnPublish(h PublishHook) { w.onPublish = h }

// PendingDir is where failed Notion writes land for later retry.
func (w *Writer) PendingDir() string {
	return filepath.Join(w.dataHome, "notion-pending")
}

type pendingRecord struct {
	SavedAt    time.Time    `json:"saved_at"`
	Hash       string       `json:"hash"`
	ExistingID string       `json:"existing_page_id,omitempty"`
	In         SynthesizeIn `json:"in"`
}

// pendingPath is keyed by conversation so a newer failure replaces an
// older one instead of queueing a stale summary behind it.
func (w *Writer) pendingPath(conversationID string) string {
	name := conversationID
	if name == "" || strings.ContainsAny(name, `/\.`) {
		name = "unknown-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return filepath.Join(w.PendingDir(), name+".json")
}

// persistPending saves a failed write for retry. The file holds the full
// transcript, so it is private to the user (0600), and written via rename
// so a crash can't leave a truncated record.
func (w *Writer) persistPending(in SynthesizeIn, hash string) {
	dir := w.PendingDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	rec := pendingRecord{
		SavedAt:    time.Now().UTC(),
		Hash:       hash,
		ExistingID: in.ExistingPageID,
		In:         in,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	path := w.pendingPath(in.Meta.ConversationID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// clearPending drops a conversation's queued write once a newer write for
// it has succeeded, so the retry can't overwrite the page with stale content.
func (w *Writer) clearPending(conversationID string) {
	if conversationID == "" {
		return
	}
	_ = os.Remove(w.pendingPath(conversationID))
}

// DrainPending retries the newest queued write for each conversation and
// discards older ones. Returns counts of (drained, still-pending) so
// callers can log a summary. Called on `chatmem mcp` start and by
// `chatmem notion resync`.
func (w *Writer) DrainPending(ctx context.Context) (drained, stillPending int, err error) {
	dir := w.PendingDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	type queued struct {
		path string
		rec  pendingRecord
	}
	newest := map[string]queued{}
	var stale []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec pendingRecord
		if err := json.Unmarshal(b, &rec); err != nil {
			// Corrupt file — remove so it doesn't stick around forever.
			_ = os.Remove(path)
			continue
		}
		conv := rec.In.Meta.ConversationID
		if cur, ok := newest[conv]; ok {
			if !rec.SavedAt.After(cur.rec.SavedAt) {
				stale = append(stale, path)
				continue
			}
			stale = append(stale, cur.path)
		}
		newest[conv] = queued{path, rec}
	}
	for _, p := range stale {
		_ = os.Remove(p)
	}

	for conv, q := range newest {
		in := q.rec.In
		in.Force = true // pending exists because a previous write failed
		out, err := w.Synthesize(ctx, in)
		if out != nil && w.onPublish != nil {
			if herr := w.onPublish(ctx, conv, in.Summary.SessionType, *out); herr != nil && err == nil {
				err = herr
			}
		}
		// Files from before per-conversation naming live at another path;
		// Synthesize has cleared or re-saved the canonical one.
		if q.path != w.pendingPath(conv) {
			if _, statErr := os.Stat(w.pendingPath(conv)); err == nil || statErr == nil {
				_ = os.Remove(q.path)
			}
		}
		if err != nil {
			stillPending++
			continue
		}
		drained++
	}
	return drained, stillPending, nil
}

// PendingCount returns how many notion writes are queued for retry, without
// draining. Used by `chatmem doctor` + `chatmem notion status`.
func (w *Writer) PendingCount() (int, error) {
	entries, err := os.ReadDir(w.PendingDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n, nil
}

// Ping delegates to the underlying client so callers don't need to reach in.
func (w *Writer) Ping(ctx context.Context) (*User, error) { return w.client.Ping(ctx) }

func stripHyphens(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			out = append(out, s[i])
		}
	}
	return string(out)
}
