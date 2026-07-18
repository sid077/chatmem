package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	chatpg "github.com/siddhantdubey/chatmem/internal/pg"
	"github.com/siddhantdubey/chatmem/internal/store"
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
}
