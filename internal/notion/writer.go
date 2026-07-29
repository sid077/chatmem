package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Writer is the top-level façade: it validates a Summary, renders it into
// Notion blocks, and writes/updates the page. Callers should hold one per
// process; it's cheap and safe to reuse.
type Writer struct {
	cfg      *Config
	client   *Client
	dataHome string
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
// to blocks, and creates or replaces the Notion page. On network failure,
// enqueues the payload in <data>/notion-pending/*.json for later retry.
func (w *Writer) Synthesize(ctx context.Context, in SynthesizeIn) (*SynthesizeOut, error) {
	hash := in.Summary.Hash()
	if !in.Force && in.ExistingPageID != "" && hash == in.PreviousHash {
		return &SynthesizeOut{PageID: in.ExistingPageID, SummaryHash: hash, Skipped: true}, nil
	}

	blocks := Render(in.Summary, in.Meta, in.Transcript)

	// Notion caps children at 100 per request. Split for the initial create
	// (which allows children inline) — pass first 100 to create, then Append
	// the rest.
	initialBlocks := blocks
	extra := []Block{}
	if len(blocks) > 100 {
		initialBlocks = blocks[:100]
		extra = blocks[100:]
	}

	var page *Page
	var err error
	if in.ExistingPageID == "" {
		page, err = w.client.CreatePage(ctx, w.cfg.ParentPageID, in.Summary.Title, initialBlocks)
		if err != nil {
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion create page: %w", err)
		}
	} else {
		// Update-in-place: rewrite title (may have changed on re-synth) + body.
		if err := w.client.UpdatePageTitle(ctx, in.ExistingPageID, in.Summary.Title); err != nil {
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion update title: %w", err)
		}
		if err := w.client.ReplacePageBody(ctx, in.ExistingPageID, initialBlocks); err != nil {
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion replace body: %w", err)
		}
		page = &Page{ID: in.ExistingPageID}
	}

	if len(extra) > 0 {
		if err := w.client.AppendChildren(ctx, page.ID, extra); err != nil {
			// Page exists with partial content; the pending retry will finish it.
			w.persistPending(in, hash)
			return nil, fmt.Errorf("notion append remainder: %w", err)
		}
	}

	url := page.URL
	if url == "" {
		// UpdatePageTitle doesn't return a URL; reconstruct from the page id.
		url = fmt.Sprintf("https://www.notion.so/%s", stripHyphens(page.ID))
	}
	return &SynthesizeOut{
		PageID:      page.ID,
		URL:         url,
		SummaryHash: hash,
	}, nil
}

// PendingDir is where failed Notion writes land for later retry.
func (w *Writer) PendingDir() string {
	return filepath.Join(w.dataHome, "notion-pending")
}

type pendingRecord struct {
	SavedAt    time.Time     `json:"saved_at"`
	Hash       string        `json:"hash"`
	ExistingID string        `json:"existing_page_id,omitempty"`
	In         SynthesizeIn  `json:"in"`
}

func (w *Writer) persistPending(in SynthesizeIn, hash string) {
	dir := w.PendingDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := time.Now().UTC().Format("20060102T150405.000000000") + ".json"
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
	_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
}

// DrainPending retries every payload in the pending directory. Returns
// counts of (drained, still-pending) so callers can log a summary.
// Called from the background sweep + `chatmem notion resync`.
func (w *Writer) DrainPending(ctx context.Context) (drained, stillPending int, err error) {
	dir := w.PendingDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
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
		rec.In.Force = true // pending exists because a previous write failed
		if _, err := w.Synthesize(ctx, rec.In); err != nil {
			stillPending++
			continue
		}
		_ = os.Remove(path)
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
