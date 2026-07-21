package mcp_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	chatmcp "github.com/sid077/chatmem/internal/mcp"
	chatpg "github.com/sid077/chatmem/internal/pg"
	"github.com/sid077/chatmem/internal/store"
)

func TestMCPServerRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	pg := chatpg.New(chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Port:       54335,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := pg.Start(ctx); err != nil {
		t.Fatalf("start pg: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })

	st := store.New(pg.Pool())
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	server := chatmcp.NewServer(st, "test")
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client"}, nil)
	t1, t2 := sdk.NewInMemoryTransports()

	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	want := map[string]bool{"record_message": false, "get_conversation": false}
	for _, tool := range tools.Tools {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("tool %s not registered", name)
		}
	}

	call1, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "record_message",
		Arguments: map[string]any{
			"role":      "user",
			"content":   "hello from mcp",
			"model":     "claude-opus-4-7",
			"provider":  "anthropic",
			"client_id": "test-client",
		},
	})
	if err != nil {
		t.Fatalf("record_message: %v", err)
	}
	if call1.IsError {
		t.Fatalf("record_message returned error: %+v", call1.Content)
	}
	var recorded struct {
		MessageID      string `json:"message_id"`
		ConversationID string `json:"conversation_id"`
	}
	if err := remarshal(call1.StructuredContent, &recorded); err != nil {
		t.Fatalf("decode structured content: %v (raw=%+v)", err, call1.StructuredContent)
	}
	if recorded.ConversationID == "" || recorded.MessageID == "" {
		t.Fatalf("empty ids in response: %+v", recorded)
	}

	call2, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "get_conversation",
		Arguments: map[string]any{
			"conversation_id": recorded.ConversationID,
			"limit":           10,
		},
	})
	if err != nil {
		t.Fatalf("get_conversation: %v", err)
	}
	if call2.IsError {
		t.Fatalf("get_conversation returned error: %+v", call2.Content)
	}
	var fetched struct {
		Conversation struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"conversation"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := remarshal(call2.StructuredContent, &fetched); err != nil {
		t.Fatalf("decode get structured content: %v (raw=%+v)", err, call2.StructuredContent)
	}
	if fetched.Conversation.ID != recorded.ConversationID {
		t.Fatalf("conversation id mismatch: got %s want %s", fetched.Conversation.ID, recorded.ConversationID)
	}
	if fetched.Conversation.Model != "claude-opus-4-7" {
		t.Fatalf("model mismatch: got %q", fetched.Conversation.Model)
	}
	if len(fetched.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(fetched.Messages))
	}
	if fetched.Messages[0].Role != "user" || fetched.Messages[0].Content != "hello from mcp" {
		t.Fatalf("unexpected message: %+v", fetched.Messages[0])
	}
}

func remarshal(src any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
