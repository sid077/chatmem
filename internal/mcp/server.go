package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sid077/chatmem/internal/store"
	"github.com/sid077/chatmem/internal/telemetry"
)

// NewServer constructs an MCP server that persists into store and records
// per-tool counters + latency into agg. agg may be nil in tests or callers
// that don't care about metrics.
func NewServer(st *store.Store, agg *telemetry.Aggregator, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:    "chatmem",
		Version: version,
	}, nil)
	registerRecordMessage(s, st, agg)
	registerGetConversation(s, st, agg)
	registerSearchHistory(s, st, agg)
	return s
}

type recordMessageArgs struct {
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"UUID of existing conversation; omit to open a new one"`
	Role           string `json:"role" jsonschema:"user | assistant | system | tool"`
	Content        string `json:"content" jsonschema:"message text"`
	Model          string `json:"model,omitempty" jsonschema:"model id, e.g. claude-opus-4-7 (required when opening a new conversation)"`
	Provider       string `json:"provider,omitempty" jsonschema:"provider, e.g. anthropic (required when opening a new conversation)"`
	ClientID       string `json:"client_id,omitempty" jsonschema:"client id, e.g. claude-code, cursor, aider (required when opening a new conversation)"`
	TokenCount     int    `json:"token_count,omitempty" jsonschema:"optional token count for the message"`
}

type recordMessageOut struct {
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
}

func registerRecordMessage(s *sdk.Server, st *store.Store, agg *telemetry.Aggregator) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "record_message",
		Description: "Store a single chat message in the local chatmem database. Opens a new conversation when conversation_id is empty; model/provider/client_id are then required.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args recordMessageArgs) (*sdk.CallToolResult, recordMessageOut, error) {
		start := time.Now()
		var convID uuid.UUID
		if args.ConversationID != "" {
			id, err := uuid.Parse(args.ConversationID)
			if err != nil {
				if agg != nil {
					agg.RecordError("capture", time.Since(start))
				}
				return nil, recordMessageOut{}, fmt.Errorf("invalid conversation_id: %w", err)
			}
			convID = id
		}
		res, err := st.RecordMessage(ctx, store.RecordMessageIn{
			ConversationID: convID,
			Role:           args.Role,
			Content:        args.Content,
			Model:          args.Model,
			Provider:       args.Provider,
			ClientID:       args.ClientID,
			TokenCount:     args.TokenCount,
		})
		if err != nil {
			if agg != nil {
				agg.RecordError("capture", time.Since(start))
			}
			return nil, recordMessageOut{}, err
		}
		if agg != nil {
			agg.RecordCapture(args.Model, args.ClientID, time.Since(start))
		}
		out := recordMessageOut{
			MessageID:      res.MessageID.String(),
			ConversationID: res.ConversationID.String(),
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{
				Text: fmt.Sprintf("recorded message %s in conversation %s", out.MessageID, out.ConversationID),
			}},
		}, out, nil
	})
}

type getConversationArgs struct {
	ConversationID string `json:"conversation_id" jsonschema:"UUID of the conversation"`
	Limit          int    `json:"limit,omitempty" jsonschema:"max messages to return (default 100, max 500)"`
	After          string `json:"after,omitempty" jsonschema:"RFC3339 timestamp cursor; return messages strictly after this"`
}

type conversationDTO struct {
	ID        string    `json:"id"`
	ClientID  string    `json:"client_id"`
	Model     string    `json:"model"`
	Provider  string    `json:"provider"`
	Title     *string   `json:"title,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type messageDTO struct {
	ID         string    `json:"id"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	TokenCount int       `json:"token_count,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type getConversationOut struct {
	Conversation conversationDTO `json:"conversation"`
	Messages     []messageDTO    `json:"messages"`
	NextAfter    *string         `json:"next_after,omitempty"`
}

type searchHistoryArgs struct {
	Query           string   `json:"query" jsonschema:"free-text query"`
	TopK            int      `json:"top_k,omitempty" jsonschema:"max hits (default 10, max 100)"`
	TokenBudget     int      `json:"token_budget,omitempty" jsonschema:"total snippet tokens to return (default 4000)"`
	Model           string   `json:"model,omitempty" jsonschema:"filter by model id"`
	ClientID        string   `json:"client_id,omitempty" jsonschema:"filter by client id (e.g. claude-code, cursor)"`
	Since           string   `json:"since,omitempty" jsonschema:"RFC3339 lower bound on message creation time"`
	Until           string   `json:"until,omitempty" jsonschema:"RFC3339 upper bound on message creation time"`
	ConversationIDs []string `json:"conversation_ids,omitempty" jsonschema:"restrict to these conversation UUIDs"`
}

type searchHit struct {
	MessageID      string    `json:"message_id"`
	ConversationID string    `json:"conversation_id"`
	Role           string    `json:"role"`
	Snippet        string    `json:"snippet"`
	Score          float64   `json:"score"`
	CreatedAt      time.Time `json:"created_at"`
}

type searchHistoryOut struct {
	Hits []searchHit `json:"hits"`
}

func registerSearchHistory(s *sdk.Server, st *store.Store, agg *telemetry.Aggregator) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "search_history",
		Description: "Search stored chat history for messages relevant to a query. Returns snippets packed to a token budget, one per conversation. MVP uses Postgres full-text ranking; semantic re-ranking via embeddings ships next.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args searchHistoryArgs) (*sdk.CallToolResult, searchHistoryOut, error) {
		start := time.Now()
		recordErr := func() {
			if agg != nil {
				agg.RecordError("search", time.Since(start))
			}
		}
		in := store.SearchHistoryIn{
			Query:       args.Query,
			TopK:        args.TopK,
			TokenBudget: args.TokenBudget,
			Model:       args.Model,
			ClientID:    args.ClientID,
		}
		if args.Since != "" {
			t, err := time.Parse(time.RFC3339Nano, args.Since)
			if err != nil {
				recordErr()
				return nil, searchHistoryOut{}, fmt.Errorf("invalid since: %w", err)
			}
			in.Since = &t
		}
		if args.Until != "" {
			t, err := time.Parse(time.RFC3339Nano, args.Until)
			if err != nil {
				recordErr()
				return nil, searchHistoryOut{}, fmt.Errorf("invalid until: %w", err)
			}
			in.Until = &t
		}
		for _, s := range args.ConversationIDs {
			id, err := uuid.Parse(s)
			if err != nil {
				recordErr()
				return nil, searchHistoryOut{}, fmt.Errorf("invalid conversation id %q: %w", s, err)
			}
			in.ConversationIDs = append(in.ConversationIDs, id)
		}

		res, err := st.SearchHistory(ctx, in)
		if err != nil {
			recordErr()
			return nil, searchHistoryOut{}, err
		}
		if agg != nil {
			agg.RecordSearch(time.Since(start))
		}

		out := searchHistoryOut{Hits: make([]searchHit, 0, len(res.Hits))}
		for _, h := range res.Hits {
			out.Hits = append(out.Hits, searchHit{
				MessageID:      h.MessageID.String(),
				ConversationID: h.ConversationID.String(),
				Role:           h.Role,
				Snippet:        h.Snippet,
				Score:          h.Score,
				CreatedAt:      h.CreatedAt,
			})
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: renderSearchHits(args.Query, out.Hits)}},
		}, out, nil
	})
}

func renderSearchHits(query string, hits []searchHit) string {
	if len(hits) == 0 {
		return fmt.Sprintf("No hits for %q. Try broader terms or drop filters.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d hit(s) for %q\n", len(hits), query)
	for i, h := range hits {
		fmt.Fprintf(&b, "\n── hit %d ──\n", i+1)
		fmt.Fprintf(&b, "role:         %s\n", h.Role)
		fmt.Fprintf(&b, "conversation: %s\n", h.ConversationID)
		fmt.Fprintf(&b, "message:      %s\n", h.MessageID)
		fmt.Fprintf(&b, "created:      %s\n", h.CreatedAt.Format(time.RFC3339))
		fmt.Fprintf(&b, "score:        %.4f\n", h.Score)
		b.WriteString("snippet:\n")
		b.WriteString(h.Snippet)
		if !strings.HasSuffix(h.Snippet, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func registerGetConversation(s *sdk.Server, st *store.Store, agg *telemetry.Aggregator) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "get_conversation",
		Description: "Fetch a conversation and its messages, ordered by creation time. Cursor-paginated via the after timestamp.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args getConversationArgs) (*sdk.CallToolResult, getConversationOut, error) {
		start := time.Now()
		recordErr := func() {
			if agg != nil {
				agg.RecordError("get", time.Since(start))
			}
		}
		convID, err := uuid.Parse(args.ConversationID)
		if err != nil {
			recordErr()
			return nil, getConversationOut{}, fmt.Errorf("invalid conversation_id: %w", err)
		}
		var after time.Time
		if args.After != "" {
			t, err := time.Parse(time.RFC3339Nano, args.After)
			if err != nil {
				recordErr()
				return nil, getConversationOut{}, fmt.Errorf("invalid after: %w", err)
			}
			after = t
		}

		res, err := st.GetConversation(ctx, store.GetConversationIn{
			ConversationID: convID,
			After:          after,
			Limit:          args.Limit,
		})
		if err != nil {
			recordErr()
			return nil, getConversationOut{}, err
		}
		if agg != nil {
			agg.RecordGet(time.Since(start))
		}

		out := getConversationOut{
			Conversation: conversationDTO{
				ID:        res.Conversation.ID.String(),
				ClientID:  res.Conversation.ClientID,
				Model:     res.Conversation.Model,
				Provider:  res.Conversation.Provider,
				Title:     res.Conversation.Title,
				StartedAt: res.Conversation.StartedAt,
				UpdatedAt: res.Conversation.UpdatedAt,
			},
			Messages: make([]messageDTO, 0, len(res.Messages)),
		}
		for _, m := range res.Messages {
			out.Messages = append(out.Messages, messageDTO{
				ID:         m.ID.String(),
				Role:       m.Role,
				Content:    m.Content,
				TokenCount: m.TokenCount,
				CreatedAt:  m.CreatedAt,
			})
		}
		if res.NextAfter != nil {
			s := res.NextAfter.Format(time.RFC3339Nano)
			out.NextAfter = &s
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: renderConversation(out)}},
		}, out, nil
	})
}

func renderConversation(out getConversationOut) string {
	var b strings.Builder
	c := out.Conversation
	fmt.Fprintf(&b, "conversation %s\n", c.ID)
	fmt.Fprintf(&b, "model:    %s / %s\n", c.Provider, c.Model)
	fmt.Fprintf(&b, "client:   %s\n", c.ClientID)
	fmt.Fprintf(&b, "started:  %s\n", c.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "updated:  %s\n", c.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "messages: %d\n", len(out.Messages))
	for _, m := range out.Messages {
		fmt.Fprintf(&b, "\n── %s @ %s ──\n", m.Role, m.CreatedAt.Format(time.RFC3339))
		b.WriteString(m.Content)
		if !strings.HasSuffix(m.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	if out.NextAfter != nil {
		fmt.Fprintf(&b, "\n(more available; pass after=%s to continue)\n", *out.NextAfter)
	}
	return b.String()
}
