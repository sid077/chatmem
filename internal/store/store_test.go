package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	chatpg "github.com/sid077/chatmem/internal/pg"
	"github.com/sid077/chatmem/internal/store"
)

func TestStoreRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	pg := chatpg.New(chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Port:       54334,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := pg.Start(ctx); err != nil {
		t.Fatalf("start pg: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })

	s := store.New(pg.Pool())
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	first, err := s.RecordMessage(ctx, store.RecordMessageIn{
		Role:     "user",
		Content:  "hello from chatmem",
		Model:    "claude-opus-4-7",
		Provider: "anthropic",
		ClientID: "claude-code",
	})
	if err != nil {
		t.Fatalf("record first: %v", err)
	}
	if first.ConversationID == uuid.Nil || first.MessageID == uuid.Nil {
		t.Fatal("expected non-nil ids")
	}

	if _, err := s.RecordMessage(ctx, store.RecordMessageIn{
		ConversationID: first.ConversationID,
		Role:           "assistant",
		Content:        "hi back",
	}); err != nil {
		t.Fatalf("record second: %v", err)
	}

	got, err := s.GetConversation(ctx, store.GetConversationIn{
		ConversationID: first.ConversationID,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(got.Messages))
	}
	if got.Messages[0].Role != "user" || got.Messages[0].Content != "hello from chatmem" {
		t.Fatalf("unexpected first message: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "assistant" || got.Messages[1].Content != "hi back" {
		t.Fatalf("unexpected second message: %+v", got.Messages[1])
	}
	if got.Conversation.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected model %q", got.Conversation.Model)
	}
	if got.NextAfter != nil {
		t.Fatalf("expected no NextAfter, got %v", got.NextAfter)
	}

	// Add a second conversation and search across both.
	if _, err := s.RecordMessage(ctx, store.RecordMessageIn{
		Role:     "user",
		Content:  "how does the kafka retention config work",
		Model:    "gpt-5",
		Provider: "openai",
		ClientID: "cursor",
	}); err != nil {
		t.Fatalf("record kafka: %v", err)
	}

	hits, err := s.SearchHistory(ctx, store.SearchHistoryIn{Query: "kafka retention", TopK: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits.Hits) == 0 {
		t.Fatal("expected at least one hit for 'kafka retention'")
	}
	if !strings.Contains(strings.ToLower(hits.Hits[0].Snippet), "kafka") {
		t.Fatalf("top hit should mention kafka, got %q", hits.Hits[0].Snippet)
	}

	// Filter by client_id — 'claude-code' has no kafka message
	filtered, err := s.SearchHistory(ctx, store.SearchHistoryIn{
		Query: "kafka retention", TopK: 5, ClientID: "claude-code",
	})
	if err != nil {
		t.Fatalf("search filtered: %v", err)
	}
	if len(filtered.Hits) != 0 {
		t.Fatalf("expected no hits when filtering to claude-code, got %d", len(filtered.Hits))
	}
}


// TestLargeMessageAndSchemaMigration covers the stored-tsvector search
// column: a database created with the pre-v0.3.3 expression index must
// migrate in place, and messages far over Postgres's 1MB tsvector limit
// must still record (they used to fail with SQLSTATE 54000).
func TestLargeMessageAndSchemaMigration(t *testing.T) {
	tmp := t.TempDir()
	pg := chatpg.New(chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Port:       54338,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := pg.Start(ctx); err != nil {
		t.Fatalf("start pg: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })
	pool := pg.Pool()

	// Simulate an existing install: old schema shape plus a stored row.
	if _, err := pool.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS vector;
		CREATE TABLE conversations (id UUID PRIMARY KEY DEFAULT gen_random_uuid(), client_id TEXT NOT NULL,
			model TEXT NOT NULL, provider TEXT NOT NULL, title TEXT, started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			ended_at TIMESTAMPTZ, metadata JSONB NOT NULL DEFAULT '{}'::jsonb, seq BIGSERIAL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE messages (id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE, role TEXT NOT NULL,
			content TEXT NOT NULL, tool_calls JSONB, token_count INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(), seq BIGSERIAL);
		CREATE TABLE chunks (id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			message_id UUID NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
			conversation_id UUID NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
			chunk_index INTEGER NOT NULL, content TEXT NOT NULL, embedding vector(384) NOT NULL,
			token_count INTEGER NOT NULL DEFAULT 0, seq BIGSERIAL);
		CREATE INDEX chunks_tsv_idx ON chunks USING gin (to_tsvector('english', content));
		WITH c AS (INSERT INTO conversations (client_id, model, provider) VALUES ('c','m','p') RETURNING id),
		     m AS (INSERT INTO messages (conversation_id, role, content) SELECT id, 'user', 'legacy zeppelin row' FROM c RETURNING id, conversation_id)
		INSERT INTO chunks (message_id, conversation_id, chunk_index, content, embedding)
		SELECT id, conversation_id, 0, 'legacy zeppelin row', array_fill(0::real, ARRAY[384])::vector FROM m;`); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}

	s := store.New(pool)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema must be re-appliable: %v", err)
	}
	hits, err := s.SearchHistory(ctx, store.SearchHistoryIn{Query: "zeppelin"})
	if err != nil || len(hits.Hits) != 1 {
		t.Fatalf("pre-existing row not searchable after migration: hits=%d err=%v", len(hits.Hits), err)
	}

	// ~3.5MB of distinct tokens, with a findable word at the start.
	var b strings.Builder
	b.WriteString("quokka ")
	for i := 0; b.Len() < 3_500_000; i++ {
		fmt.Fprintf(&b, "tok%dx%d ", i, i*7)
	}
	if _, err := s.RecordMessage(ctx, store.RecordMessageIn{
		Role: "tool", Content: b.String(), Model: "m", Provider: "p", ClientID: "c",
	}); err != nil {
		t.Fatalf("record 3.5MB message: %v", err)
	}
	hits, err = s.SearchHistory(ctx, store.SearchHistoryIn{Query: "quokka"})
	if err != nil || len(hits.Hits) != 1 {
		t.Fatalf("large message not searchable: hits=%d err=%v", len(hits.Hits), err)
	}
}
