package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	chatmcp "github.com/sid077/chatmem/internal/mcp"
	"github.com/sid077/chatmem/internal/notion"
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

	server := chatmcp.NewServer(chatmcp.Deps{Store: st, Version: "test"})
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

	// The visible Content field must include the message text — otherwise MCP
	// clients that don't surface structured content (e.g. Windsurf Cascade)
	// see only a summary and can't act on the result.
	getText := firstText(call2.Content)
	for _, want := range []string{"hello from mcp", "user", recorded.ConversationID} {
		if !strings.Contains(getText, want) {
			t.Fatalf("get_conversation Content missing %q\n---\n%s", want, getText)
		}
	}

	// Now: search_history must also include the snippet in Content.
	call3, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "search_history",
		Arguments: map[string]any{
			"query":  "hello mcp",
			"top_k":  5,
		},
	})
	if err != nil {
		t.Fatalf("search_history: %v", err)
	}
	if call3.IsError {
		t.Fatalf("search_history returned error: %+v", call3.Content)
	}
	searchText := firstText(call3.Content)
	for _, want := range []string{"hello from mcp", "hit", "snippet:"} {
		if !strings.Contains(searchText, want) {
			t.Fatalf("search_history Content missing %q\n---\n%s", want, searchText)
		}
	}
}

func firstText(cs []sdk.Content) string {
	for _, c := range cs {
		if t, ok := c.(*sdk.TextContent); ok {
			return t.Text
		}
	}
	return ""
}

// TestSynthesizeToolAcceptsSummaryObject reproduces the v0.2.0 bug where the
// SDK reflected Summary as `[]byte` because the args struct used
// json.RawMessage. The tool's schema advertised an array-of-integers and
// clients couldn't call it with any real object. Fixed in v0.2.1 by making
// Summary a typed notion.Summary field.
func TestSynthesizeToolAcceptsSummaryObject(t *testing.T) {
	tmp := t.TempDir()
	pg := chatpg.New(chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Port:       54338,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := pg.Start(ctx); err != nil {
		t.Fatalf("start pg: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })

	st := store.New(pg.Pool())
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// Mock Notion — accept everything, echo a page id.
	notionMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/users/me":
			w.Write([]byte(`{"id":"u","name":"n","type":"bot"}`))
		case r.URL.Path == "/v1/pages":
			w.Write([]byte(`{"id":"pg-1","url":"https://www.notion.so/pg1"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer notionMock.Close()

	if err := notion.SaveConfig(tmp, notion.Config{
		IntegrationToken: "t", ParentPageID: "p", ConnectedAt: time.Now(),
		AutoSynthesize: notion.DefaultAuto(),
	}); err != nil {
		t.Fatalf("save notion cfg: %v", err)
	}
	writer, err := notion.NewWriter(tmp, notionMock.URL)
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	server := chatmcp.NewServer(chatmcp.Deps{
		Store: st, NotionWriter: writer, Version: "test",
	})
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

	// Confirm the tool's advertised schema accepts an object for `summary`.
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var synthTool *sdk.Tool
	for _, tl := range tools.Tools {
		if tl.Name == "synthesize_to_notion" {
			synthTool = tl
			break
		}
	}
	if synthTool == nil {
		t.Fatal("synthesize_to_notion tool not advertised")
	}
	schema, _ := json.Marshal(synthTool.InputSchema)
	if !strings.Contains(string(schema), `"object"`) {
		t.Fatalf("input schema does not accept an object for summary; got: %s", string(schema))
	}
	if strings.Contains(string(schema), `"maximum":255`) {
		t.Fatalf("input schema still advertises byte-array shape (max:255) for summary; got: %s", string(schema))
	}

	// Seed a conversation so the tool has messages to cite.
	rec, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "record_message",
		Arguments: map[string]any{
			"role": "user", "content": "test",
			"model": "m", "provider": "p", "client_id": "c",
		},
	})
	if err != nil || rec.IsError {
		t.Fatalf("record_message failed: err=%v isErr=%v content=%v", err, rec.IsError, rec.Content)
	}
	var recStruct struct {
		MessageID      string `json:"message_id"`
		ConversationID string `json:"conversation_id"`
	}
	if err := remarshal(rec.StructuredContent, &recStruct); err != nil {
		t.Fatalf("decode record: %v", err)
	}

	// Now call synthesize_to_notion with a REAL summary OBJECT — not bytes.
	summary := map[string]any{
		"title":        "Test synth",
		"session_type": "study",
		"tldr":         []string{"one", "two"},
		"concepts": []map[string]any{{
			"heading":    "H",
			"definition": "D",
			"body":       "B",
			"cited_from": []string{recStruct.MessageID},
		}},
		"diagrams": []map[string]any{{
			"type": "flowchart", "mermaid": "flowchart TD\nA-->B",
		}},
	}
	call, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "synthesize_to_notion",
		Arguments: map[string]any{
			"conversation_id": recStruct.ConversationID,
			"summary":         summary,
		},
	})
	if err != nil {
		t.Fatalf("synthesize_to_notion call: %v", err)
	}
	if call.IsError {
		t.Fatalf("synthesize_to_notion returned error: %s", firstText(call.Content))
	}
	var out struct {
		NotionURL string `json:"notion_url"`
	}
	if err := remarshal(call.StructuredContent, &out); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
	if out.NotionURL == "" {
		t.Fatal("expected non-empty notion_url in structured response")
	}
}

func remarshal(src any, dst any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

// TestMultiPassCoverageGate is the v0.3.0 end-to-end: extract facts, then
// call synthesize_to_notion with a Summary that leaves some messages
// uncited. Coverage gate must refuse the write and list the missed msg
// uuids. Then a corrected Summary that cites everything succeeds.
func TestMultiPassCoverageGate(t *testing.T) {
	tmp := t.TempDir()
	pg := chatpg.New(chatpg.Config{
		DataDir:    filepath.Join(tmp, "data"),
		RuntimeDir: filepath.Join(tmp, "runtime"),
		Port:       54339,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := pg.Start(ctx); err != nil {
		t.Fatalf("start pg: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })

	st := store.New(pg.Pool())
	if err := st.EnsureSchema(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	notionMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/users/me":
			w.Write([]byte(`{"id":"u","name":"n","type":"bot"}`))
		case r.URL.Path == "/v1/pages":
			w.Write([]byte(`{"id":"pg","url":"https://www.notion.so/pg"}`))
		default:
			w.Write([]byte(`{}`))
		}
	}))
	defer notionMock.Close()
	_ = notion.SaveConfig(tmp, notion.Config{
		IntegrationToken: "t", ParentPageID: "p", ConnectedAt: time.Now(),
		AutoSynthesize: notion.DefaultAuto(),
	})
	writer, _ := notion.NewWriter(tmp, notionMock.URL)

	server := chatmcp.NewServer(chatmcp.Deps{Store: st, NotionWriter: writer, Version: "test"})
	client := sdk.NewClient(&sdk.Implementation{Name: "t"}, nil)
	t1, t2 := sdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer session.Close()

	// Seed 4 messages
	var msgIDs []string
	var convID string
	for i, content := range []string{"what is hnsw", "graph-based ANN", "pgvector defaults?", "m=16 ef_construction=64"} {
		args := map[string]any{
			"role":    map[int]string{0: "user", 1: "assistant", 2: "user", 3: "assistant"}[i],
			"content": content,
		}
		if convID == "" {
			args["model"] = "m"
			args["provider"] = "p"
			args["client_id"] = "c"
		} else {
			args["conversation_id"] = convID
		}
		rec, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "record_message", Arguments: args})
		if err != nil || rec.IsError {
			t.Fatalf("record_message %d: %v %v", i, err, rec.Content)
		}
		var r struct {
			MessageID      string `json:"message_id"`
			ConversationID string `json:"conversation_id"`
		}
		_ = remarshal(rec.StructuredContent, &r)
		msgIDs = append(msgIDs, r.MessageID)
		convID = r.ConversationID
	}

	// Extract facts for the first 2 messages only.
	factsCall, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "record_facts",
		Arguments: map[string]any{
			"conversation_id": convID,
			"facts": []map[string]any{
				{"message_id": msgIDs[0], "category": "question", "text": "asked about HNSW", "importance": "normal"},
				{"message_id": msgIDs[1], "category": "concept", "text": "HNSW is graph-based ANN", "importance": "normal"},
			},
		},
	})
	if err != nil || factsCall.IsError {
		t.Fatalf("record_facts: %v %s", err, firstText(factsCall.Content))
	}
	var frOut struct {
		UnextractedMessages int `json:"unextracted_messages"`
	}
	_ = remarshal(factsCall.StructuredContent, &frOut)
	if frOut.UnextractedMessages != 2 {
		t.Fatalf("expected 2 unextracted, got %d", frOut.UnextractedMessages)
	}

	// Attempt synth with a Summary that only cites msgIDs[0] and [1] — msgs
	// [2] and [3] have no facts (trivia excluded), so if we DON'T extract
	// for them, coverage will still be measured against only the 2 with
	// facts. That's a 100% ratio when only [0] and [1] are cited. To force
	// a failure we now extract facts for [2] and [3] first.
	factsCall2, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "record_facts",
		Arguments: map[string]any{
			"conversation_id": convID,
			"facts": []map[string]any{
				{"message_id": msgIDs[2], "category": "question", "text": "asked about defaults", "importance": "normal"},
				{"message_id": msgIDs[3], "category": "reference", "text": "m=16 ef_construction=64", "importance": "critical"},
			},
		},
	})
	if err != nil || factsCall2.IsError {
		t.Fatalf("record_facts round 2: %v %s", err, firstText(factsCall2.Content))
	}

	// Now synth with Summary that cites only msgIDs[0] and [1] — that's 2/4 = 50%,
	// below default 95% → refuse.
	underCovered := map[string]any{
		"title":        "Partial HNSW notes",
		"session_type": "study",
		"tldr":         []string{"one"},
		"concepts": []map[string]any{{
			"heading":    "HNSW",
			"definition": "graph-based ANN",
			"body":       "b",
			"cited_from": []string{msgIDs[0], msgIDs[1]},
		}},
		"diagrams": []map[string]any{{"type": "flowchart", "mermaid": "A-->B"}},
	}
	call, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "synthesize_to_notion",
		Arguments: map[string]any{
			"conversation_id": convID,
			"summary":         underCovered,
		},
	})
	if err != nil {
		t.Fatalf("synth call: %v", err)
	}
	if !call.IsError {
		t.Fatalf("expected coverage refusal, got success: %s", firstText(call.Content))
	}
	txt := firstText(call.Content)
	if !strings.Contains(txt, "Coverage") || !strings.Contains(txt, msgIDs[3]) {
		t.Fatalf("expected error to list missed uuid %s in coverage message; got:\n%s", msgIDs[3], txt)
	}

	// Retry with full coverage.
	full := underCovered
	full["concepts"] = []map[string]any{{
		"heading":    "HNSW",
		"definition": "graph-based ANN with m=16 ef=64 defaults",
		"body":       "b",
		"cited_from": []string{msgIDs[0], msgIDs[1], msgIDs[2], msgIDs[3]},
	}}
	call2, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name: "synthesize_to_notion",
		Arguments: map[string]any{
			"conversation_id": convID,
			"summary":         full,
		},
	})
	if err != nil {
		t.Fatalf("second synth: %v", err)
	}
	if call2.IsError {
		t.Fatalf("expected success at 100%% coverage: %s", firstText(call2.Content))
	}
	var okOut struct {
		NotionURL string  `json:"notion_url"`
		Coverage  float64 `json:"coverage"`
	}
	_ = remarshal(call2.StructuredContent, &okOut)
	if okOut.NotionURL == "" || okOut.Coverage < 0.99 {
		t.Fatalf("bad structured out: %+v", okOut)
	}
}
