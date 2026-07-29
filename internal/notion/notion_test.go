package notion_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sid077/chatmem/internal/notion"
)

// --- Summary validation ---

func TestSummaryValidate_HappyStudy(t *testing.T) {
	valid := map[string]bool{
		"11111111-1111-1111-1111-111111111111": true,
		"22222222-2222-2222-2222-222222222222": true,
	}
	s := notion.Summary{
		Title:       "HNSW cosine distance intuition",
		SessionType: notion.Study,
		TLDR:        []string{"HNSW is a graph index", "cosine is normalized dot"},
		Concepts: []notion.Concept{{
			Heading: "HNSW", Definition: "Hierarchical Navigable Small World.",
			Body: "…", CitedFrom: []string{"11111111-1111-1111-1111-111111111111"},
		}},
		Diagrams: []notion.Diagram{{Type: "flowchart", Mermaid: "flowchart TD\nA-->B"}},
	}
	if err := s.Validate(valid); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestSummaryValidate_DebugMissingTimelineFails(t *testing.T) {
	s := notion.Summary{
		Title:       "Broken thing",
		SessionType: notion.Debug,
		TLDR:        []string{"broken", "fixed"},
		Attempts: []notion.DebugAttempt{{
			Number: 1, Description: "did a thing",
			Expected: "x", Actual: "y", Learning: "z",
			CitedFrom: []string{"11111111-1111-1111-1111-111111111111"},
		}},
		RootCause: &notion.Cite{Text: "misconfig", CitedFrom: []string{"11111111-1111-1111-1111-111111111111"}},
		Diagrams:  []notion.Diagram{}, // NO timeline
	}
	err := s.Validate(map[string]bool{"11111111-1111-1111-1111-111111111111": true})
	if err == nil {
		t.Fatal("expected validation error for missing timeline")
	}
	if !strings.Contains(err.Error(), "timeline") {
		t.Fatalf("wrong error message: %v", err)
	}
}

func TestSummaryValidate_UncitedConcept(t *testing.T) {
	s := notion.Summary{
		Title: "x", SessionType: notion.Study, TLDR: []string{"a"},
		Concepts: []notion.Concept{{Heading: "H", Definition: "D"}}, // no CitedFrom
		Diagrams: []notion.Diagram{{Type: "flowchart", Mermaid: "A-->B"}},
	}
	err := s.Validate(map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "cited_from") {
		t.Fatalf("expected cited_from error, got: %v", err)
	}
}

func TestSummaryHash_Stable(t *testing.T) {
	s := notion.Summary{Title: "x", SessionType: notion.Study, TLDR: []string{"a"}}
	if s.Hash() != s.Hash() {
		t.Fatal("hash not stable")
	}
	s2 := s
	s2.Title = "y"
	if s.Hash() == s2.Hash() {
		t.Fatal("hash collided on different content")
	}
}

// --- Writer against a mock Notion server ---

type mockNotion struct {
	mu       sync.Mutex
	pages    map[string][]notion.Block // pageID -> children
	pageURL  map[string]string
	nextID   int
	requests []string
}

func newMock() *mockNotion {
	return &mockNotion{pages: map[string][]notion.Block{}, pageURL: map[string]string{}}
}

func (m *mockNotion) alloc() string {
	m.nextID++
	// Fake UUID-shaped id so any code that assumes uuid format is happy.
	return "00000000-0000-0000-0000-" + padID(m.nextID)
}

func padID(n int) string {
	s := "000000000000"
	x := ""
	for n > 0 {
		x = string(rune('0'+n%10)) + x
		n /= 10
	}
	if len(x) < len(s) {
		x = s[:len(s)-len(x)] + x
	}
	return x
}

func (m *mockNotion) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests = append(m.requests, r.Method+" "+r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/users/me":
			json.NewEncoder(w).Encode(map[string]any{
				"id": "user-1", "name": "test", "type": "bot",
			})
		case r.Method == "POST" && r.URL.Path == "/v1/pages":
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			id := m.alloc()
			url := "https://www.notion.so/" + strings.ReplaceAll(id, "-", "")
			m.pageURL[id] = url
			if kids, ok := in["children"].([]any); ok {
				for _, k := range kids {
					m.pages[id] = append(m.pages[id], toBlock(k))
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"id": id, "url": url})
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/v1/blocks/") && strings.HasSuffix(r.URL.Path, "/children"):
			pageID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/blocks/"), "/children")
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if kids, ok := in["children"].([]any); ok {
				for _, k := range kids {
					m.pages[pageID] = append(m.pages[pageID], toBlock(k))
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/blocks/") && strings.HasSuffix(r.URL.Path, "/children"):
			pageID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/blocks/"), "/children")
			out := make([]any, 0, len(m.pages[pageID]))
			for i, b := range m.pages[pageID] {
				// Attach a fake id so DeleteBlock works.
				bb := map[string]any{}
				for k, v := range b {
					bb[k] = v
				}
				bb["id"] = pageID + "-" + itoa(i)
				out = append(out, bb)
			}
			json.NewEncoder(w).Encode(map[string]any{"results": out, "has_more": false})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/blocks/"):
			// Simulate a proper delete: strip the child from whichever page has it.
			for pid, kids := range m.pages {
				out := kids[:0]
				id := strings.TrimPrefix(r.URL.Path, "/v1/blocks/")
				for i, k := range kids {
					if pid+"-"+itoa(i) == id {
						continue
					}
					out = append(out, k)
				}
				m.pages[pid] = out
			}
			json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/v1/pages/"):
			// UpdatePageTitle — just ack.
			json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.Error(w, "not found: "+r.Method+" "+r.URL.Path, 404)
		}
	})
}

func toBlock(v any) notion.Block {
	b, _ := json.Marshal(v)
	var out notion.Block
	_ = json.Unmarshal(b, &out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestWriter_CreateAndUpdatePage(t *testing.T) {
	mock := newMock()
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	tmp := t.TempDir()
	cfg := notion.Config{
		IntegrationToken: "test-token",
		ParentPageID:     "parent-1",
		ConnectedAt:      time.Now(),
		AutoSynthesize:   notion.DefaultAuto(),
	}
	if err := notion.SaveConfig(tmp, cfg); err != nil {
		t.Fatalf("save cfg: %v", err)
	}

	w, err := notion.NewWriter(tmp, srv.URL)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if w == nil {
		t.Fatal("writer was nil")
	}

	uid := "11111111-1111-1111-1111-111111111111"
	summary := notion.Summary{
		Title:       "Test page",
		SessionType: notion.Debug,
		TLDR:        []string{"bug", "fixed"},
		Status:      "resolved",
		Attempts: []notion.DebugAttempt{{
			Number: 1, Description: "tried thing",
			Expected: "x", Actual: "y", Learning: "z",
			CitedFrom: []string{uid},
		}},
		RootCause: &notion.Cite{Text: "cause", CitedFrom: []string{uid}},
		Diagrams:  []notion.Diagram{{Type: "timeline", Mermaid: "timeline\ntitle x"}},
	}
	if err := summary.Validate(map[string]bool{uid: true}); err != nil {
		t.Fatalf("valid summary rejected: %v", err)
	}

	ctx := context.Background()

	// First synth — creates a new page.
	out1, err := w.Synthesize(ctx, notion.SynthesizeIn{
		Summary: summary,
		Meta: notion.RenderMeta{
			ConversationID: uid, Model: "claude", Provider: "anth",
			ClientID: "cli", SynthesizedAt: time.Now(), Version: 1,
		},
		Transcript: notion.Transcript{Turns: []notion.TranscriptTurn{
			{MessageID: uid, Role: "user", Content: "hi", CreatedAt: time.Now()},
		}},
	})
	if err != nil {
		t.Fatalf("synth 1: %v", err)
	}
	if out1.PageID == "" || out1.URL == "" || out1.Skipped {
		t.Fatalf("unexpected first-synth output: %+v", out1)
	}
	firstHash := out1.SummaryHash

	// Re-synth with identical summary — must skip (no API call).
	out2, err := w.Synthesize(ctx, notion.SynthesizeIn{
		Summary:        summary,
		Meta:           notion.RenderMeta{ConversationID: uid, SynthesizedAt: time.Now(), Version: 2},
		ExistingPageID: out1.PageID,
		PreviousHash:   firstHash,
	})
	if err != nil {
		t.Fatalf("synth 2: %v", err)
	}
	if !out2.Skipped {
		t.Fatalf("expected skip on identical hash, got: %+v", out2)
	}

	// Re-synth with changed title — must PATCH.
	summary.Title = "Test page (updated)"
	out3, err := w.Synthesize(ctx, notion.SynthesizeIn{
		Summary:        summary,
		Meta:           notion.RenderMeta{ConversationID: uid, SynthesizedAt: time.Now(), Version: 2},
		ExistingPageID: out1.PageID,
		PreviousHash:   firstHash,
	})
	if err != nil {
		t.Fatalf("synth 3: %v", err)
	}
	if out3.Skipped {
		t.Fatal("expected re-synth to touch Notion on hash change")
	}
	if out3.SummaryHash == firstHash {
		t.Fatal("expected new hash after title change")
	}

	// Requests we expect: POST /v1/pages (create),
	// then on update path: PATCH /v1/pages/<id> (title) + GET children + DELETE per child + PATCH /children.
	if len(mock.requests) < 3 {
		t.Fatalf("unexpectedly few Notion requests: %v", mock.requests)
	}
}

func TestWriter_PersistsPendingOnFailure(t *testing.T) {
	// Server always 500s to force pending persistence.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	_ = notion.SaveConfig(tmp, notion.Config{
		IntegrationToken: "t", ParentPageID: "p", ConnectedAt: time.Now(),
		AutoSynthesize: notion.DefaultAuto(),
	})
	w, _ := notion.NewWriter(tmp, srv.URL)

	uid := "11111111-1111-1111-1111-111111111111"
	summary := notion.Summary{
		Title: "x", SessionType: notion.Study, TLDR: []string{"a"},
		Concepts: []notion.Concept{{Heading: "H", Definition: "D", CitedFrom: []string{uid}}},
		Diagrams: []notion.Diagram{{Type: "flowchart", Mermaid: "A-->B"}},
	}
	_, err := w.Synthesize(context.Background(), notion.SynthesizeIn{
		Summary: summary,
		Meta:    notion.RenderMeta{ConversationID: uid, SynthesizedAt: time.Now(), Version: 1},
	})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	n, err := w.PendingCount()
	if err != nil {
		t.Fatalf("pending count: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 pending, got %d", n)
	}
}

func TestConfig_LoadSaveRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	orig := notion.Config{
		IntegrationToken: "secret", ParentPageID: "p1",
		ConnectedAt: time.Now().UTC().Truncate(time.Second),
		AutoSynthesize: notion.AutoSynthesizeCfg{
			Enabled: true, IdleMinutes: 5, MessageThreshold: 15, MinMessages: 2,
		},
	}
	if err := notion.SaveConfig(tmp, orig); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := notion.LoadConfig(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || got.IntegrationToken != "secret" ||
		got.AutoSynthesize.IdleMinutes != 5 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}
