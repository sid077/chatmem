package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/siddhantdubey/chatmem/internal/store"
)

func NewServer(st *store.Store, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:    "chatmem",
		Version: version,
	}, nil)
	registerRecordMessage(s, st)
	registerGetConversation(s, st)
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

func registerRecordMessage(s *sdk.Server, st *store.Store) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "record_message",
		Description: "Store a single chat message in the local chatmem database. Opens a new conversation when conversation_id is empty; model/provider/client_id are then required.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args recordMessageArgs) (*sdk.CallToolResult, recordMessageOut, error) {
		var convID uuid.UUID
		if args.ConversationID != "" {
			id, err := uuid.Parse(args.ConversationID)
			if err != nil {
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
			return nil, recordMessageOut{}, err
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

func registerGetConversation(s *sdk.Server, st *store.Store) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "get_conversation",
		Description: "Fetch a conversation and its messages, ordered by creation time. Cursor-paginated via the after timestamp.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args getConversationArgs) (*sdk.CallToolResult, getConversationOut, error) {
		convID, err := uuid.Parse(args.ConversationID)
		if err != nil {
			return nil, getConversationOut{}, fmt.Errorf("invalid conversation_id: %w", err)
		}
		var after time.Time
		if args.After != "" {
			t, err := time.Parse(time.RFC3339Nano, args.After)
			if err != nil {
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
			return nil, getConversationOut{}, err
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
			Content: []sdk.Content{&sdk.TextContent{
				Text: fmt.Sprintf("conversation %s: %d message(s)", out.Conversation.ID, len(out.Messages)),
			}},
		}, out, nil
	})
}
