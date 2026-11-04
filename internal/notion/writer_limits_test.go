package notion_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sid077/chatmem/internal/notion"
)

const limitsConv = "22222222-2222-2222-2222-222222222222"

func newTestWriter(t *testing.T, baseURL string) (*notion.Writer, string) {
	t.Helper()
	tmp := t.TempDir()
	if err := notion.SaveConfig(tmp, notion.Config{
		IntegrationToken: "t", ParentPageID: "parent", ConnectedAt: time.Now(),
		AutoSynthesize: notion.DefaultAuto(),
	}); err != nil {
		t.Fatal(err)
	}
	w, err := notion.NewWriter(tmp, baseURL)
	if err != nil || w == nil {
		t.Fatalf("new writer: %v", err)
	}
	return w, tmp
}

func limitsSummary(body string) notion.Summary {
	return notion.Summary{
		Title: "limits", SessionType: notion.Study, TLDR: []string{"a"},
		Concepts: []notion.Concept{{Heading: "H", Definition: "D", Body: body, CitedFrom: []string{limitsConv}}},
		Diagrams: []notion.Diagram{{Type: "flowchart", Mermaid: "A-->B"}},
	}
}

// TestWriter_RespectsNotionRequestLimits feeds content that breaks every
// documented Notion request limit when sent naively: a 5000-char paragraph,
// a 300-paragraph message, a multi-MB message, emoji (2 UTF-16 units each),
// and enough turns to need several requests. The mock rejects any request
// over the limits, exactly like the real API.
func TestWriter_RespectsNotionRequestLimits(t *testing.T) {
	mock := newMock()
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	w, _ := newTestWriter(t, srv.URL)

	var turns []notion.TranscriptTurn
	add := func(content string) {
		turns = append(turns, notion.TranscriptTurn{
			MessageID: limitsConv, Role: "user", Content: content, CreatedAt: time.Now(),
		})
	}
	add(strings.Repeat("x", 5000))
	add(strings.Repeat("para\n\n", 300))
	add(strings.Repeat("word ", 700_000)) // 3.5MB
	add(strings.Repeat("😀", 1500))
	for i := 0; i < 250; i++ {
		add("short turn")
	}

	out, err := w.Synthesize(context.Background(), notion.SynthesizeIn{
		Summary:    limitsSummary(strings.Repeat("y", 4500)),
		Meta:       notion.RenderMeta{ConversationID: limitsConv, SynthesizedAt: time.Now(), Version: 1},
		Transcript: notion.Transcript{Turns: turns},
	})
	if err != nil {
		t.Fatalf("synthesize: %v (limit violations: %v)", err, mock.limitErrs)
	}
	if len(mock.limitErrs) > 0 {
		t.Fatalf("requests exceeded Notion limits: %v", mock.limitErrs)
	}
	if got := len(mock.pages[out.PageID]); got < 250 {
		t.Fatalf("page has %d top-level blocks, want every turn written", got)
	}
}

// TestWriter_PartialCreateDoesNotDuplicatePage: the page is created, then
// appending the rest fails. The writer must report the page id, queue the
// retry against that page, and the retry must not create a second page.
func TestWriter_PartialCreateDoesNotDuplicatePage(t *testing.T) {
	mock := newMock()
	mock.failAppends = 1
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	w, _ := newTestWriter(t, srv.URL)

	var turns []notion.TranscriptTurn
	for i := 0; i < 150; i++ { // > 100 top-level blocks → needs an append
		turns = append(turns, notion.TranscriptTurn{MessageID: limitsConv, Role: "user", Content: "hi", CreatedAt: time.Now()})
	}
	in := notion.SynthesizeIn{
		Summary:    limitsSummary("body"),
		Meta:       notion.RenderMeta{ConversationID: limitsConv, SynthesizedAt: time.Now(), Version: 1},
		Transcript: notion.Transcript{Turns: turns},
	}
	out, err := w.Synthesize(context.Background(), in)
	if err == nil {
		t.Fatal("expected append failure")
	}
	if out == nil || out.PageID == "" {
		t.Fatalf("partial create must return the created page id, got %+v", out)
	}
	if out.SummaryHash != "" {
		t.Fatal("partial create must not report a summary hash")
	}

	var hooked []notion.SynthesizeOut
	w.OnPublish(func(_ context.Context, conv string, _ notion.SessionType, o notion.SynthesizeOut) error {
		if conv != limitsConv {
			t.Errorf("hook got conversation %q", conv)
		}
		hooked = append(hooked, o)
		return nil
	})
	drained, still, err := w.DrainPending(context.Background())
	if err != nil || drained != 1 || still != 0 {
		t.Fatalf("drain: drained=%d still=%d err=%v", drained, still, err)
	}

	posts := 0
	for _, r := range mock.requests {
		if r == "POST /v1/pages" {
			posts++
		}
	}
	if posts != 1 {
		t.Fatalf("want exactly 1 page create, got %d: %v", posts, mock.requests)
	}
	if len(hooked) != 1 || hooked[0].PageID != out.PageID || hooked[0].SummaryHash == "" {
		t.Fatalf("hook must record the drained page with its hash, got %+v", hooked)
	}
	if n, _ := w.PendingCount(); n != 0 {
		t.Fatalf("pending not cleared after drain: %d", n)
	}
}

// TestWriter_PendingIsPerConversationAndPrivate: repeated failures for one
// conversation keep only the newest payload, a later success clears it,
// and the file (which holds the full transcript) is mode 0600.
func TestWriter_PendingIsPerConversationAndPrivate(t *testing.T) {
	var fail = true
	mock := newMock()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(rw, "boom", 500)
			return
		}
		mock.handler().ServeHTTP(rw, r)
	}))
	defer srv.Close()
	w, _ := newTestWriter(t, srv.URL)

	in := notion.SynthesizeIn{
		Summary: limitsSummary("v1"),
		Meta:    notion.RenderMeta{ConversationID: limitsConv, SynthesizedAt: time.Now(), Version: 1},
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Synthesize(context.Background(), in); err == nil {
			t.Fatal("expected failure")
		}
	}
	if n, _ := w.PendingCount(); n != 1 {
		t.Fatalf("want 1 pending file for one conversation, got %d", n)
	}
	fi, err := os.Stat(filepath.Join(w.PendingDir(), limitsConv+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending file mode %v, want 0600", fi.Mode().Perm())
	}

	fail = false
	in.Summary = limitsSummary("v2")
	if _, err := w.Synthesize(context.Background(), in); err != nil {
		t.Fatalf("synthesize after recovery: %v", err)
	}
	if n, _ := w.PendingCount(); n != 0 {
		t.Fatalf("stale pending survived a newer successful write: %d", n)
	}
}

// TestClient_CreateNotRetriedOnServerError: a 5xx on POST /v1/pages may
// have created the page, so the client must not retry it.
func TestClient_CreateNotRetriedOnServerError(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
		}
		http.Error(rw, "boom", 502)
	}))
	defer srv.Close()
	c := notion.NewClient("t", srv.URL)
	if _, err := c.CreatePage(context.Background(), "parent", "x", nil); err == nil {
		t.Fatal("expected error")
	}
	if posts != 1 {
		t.Fatalf("create was attempted %d times, want 1", posts)
	}
}
