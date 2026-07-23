package telemetry_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sid077/chatmem/internal/telemetry"
)

func TestAggregatorCountersAndPercentiles(t *testing.T) {
	a := telemetry.NewAggregator()
	// 100 samples for one op: 1..100 ms
	for i := 1; i <= 100; i++ {
		a.RecordSearch(time.Duration(i) * time.Millisecond)
	}
	a.RecordCapture("claude-opus-4-7", "windsurf", 42*time.Millisecond)
	a.RecordCapture("gpt-5", "cursor", 17*time.Millisecond)
	a.RecordGet(1 * time.Millisecond)

	snap := a.Snapshot()
	if snap.Events.Searches != 100 {
		t.Fatalf("searches=%d want 100", snap.Events.Searches)
	}
	if snap.Events.Captures != 2 {
		t.Fatalf("captures=%d want 2", snap.Events.Captures)
	}
	if snap.Events.Models["claude-opus-4-7"] != 1 || snap.Events.Models["gpt-5"] != 1 {
		t.Fatalf("model dist: %+v", snap.Events.Models)
	}
	if snap.Events.Clients["windsurf"] != 1 || snap.Events.Clients["cursor"] != 1 {
		t.Fatalf("client dist: %+v", snap.Events.Clients)
	}

	search := snap.Latency["search"]
	if search.Count != 100 || search.Min != 1 || search.Max != 100 {
		t.Fatalf("search stats: %+v", search)
	}
	if search.P50 != 50 || search.P95 != 95 || search.P99 != 99 {
		t.Fatalf("percentiles wrong: %+v", search)
	}

	// After Snapshot, a fresh window must be empty
	if !a.IsEmpty() {
		t.Fatal("expected aggregator empty after snapshot")
	}
}

func TestClientFlushPostsToIngest(t *testing.T) {
	var got atomic.Int32
	var lastPayload telemetry.Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/ping" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &lastPayload)
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	state := telemetry.State{InstallID: "test-install", Enabled: true, Source: "default"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := telemetry.NewClient(state, tmp, log, telemetry.Options{
		Version:   "test",
		IngestURL: srv.URL,
	})
	c.Aggregator().RecordCapture("m", "c", 3*time.Millisecond)
	c.Flush(context.Background())

	if got.Load() != 1 {
		t.Fatalf("expected 1 POST, got %d", got.Load())
	}
	if lastPayload.InstallID != "test-install" || lastPayload.Events.Captures != 1 {
		t.Fatalf("unexpected payload: %+v", lastPayload)
	}

	// A flush of an empty window must not POST anything.
	c.Flush(context.Background())
	if got.Load() != 1 {
		t.Fatalf("empty flush should not POST, count now %d", got.Load())
	}
}

func TestClientFlushPersistsPendingOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	state := telemetry.State{InstallID: "id", Enabled: true, Source: "default"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := telemetry.NewClient(state, tmp, log, telemetry.Options{
		Version:   "test",
		IngestURL: srv.URL,
		// Short-circuit the retry backoff by supplying a fast client that
		// still fails — we only need to verify pending persistence.
		HTTPClient: &http.Client{Timeout: 100 * time.Millisecond},
	})
	c.Aggregator().RecordSearch(2 * time.Millisecond)
	c.Flush(context.Background())

	pending, err := c.PendingFiles()
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending file, got %d (%v)", len(pending), pending)
	}
	// The file should be under $tmp/pending/*.json
	if filepath.Dir(pending[0]) != filepath.Join(tmp, "pending") {
		t.Fatalf("pending file in wrong dir: %s", pending[0])
	}
	b, err := os.ReadFile(pending[0])
	if err != nil {
		t.Fatalf("read pending: %v", err)
	}
	var p telemetry.Payload
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if p.Events.Searches != 1 {
		t.Fatalf("pending payload malformed: %+v", p.Events)
	}
}

func TestClientDisabledIsNoop(t *testing.T) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	state := telemetry.State{InstallID: "id", Enabled: false, Source: "env"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := telemetry.NewClient(state, tmp, log, telemetry.Options{
		Version:   "test",
		IngestURL: srv.URL,
	})
	c.Aggregator().RecordCapture("m", "c", 1*time.Millisecond)
	c.Flush(context.Background())

	if got.Load() != 0 {
		t.Fatalf("disabled client should never POST, got %d", got.Load())
	}
}
