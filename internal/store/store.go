package store

import (
	_ "embed"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

//go:embed schema.sql
var schemaSQL string

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("create ext vector: %w", err)
	}
	if _, err := s.pool.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

type RecordMessageIn struct {
	ConversationID uuid.UUID
	Role           string
	Content        string
	Model          string
	Provider       string
	ClientID       string
	ToolCalls      []byte
	TokenCount     int
}

type RecordMessageOut struct {
	MessageID      uuid.UUID
	ConversationID uuid.UUID
}

func (s *Store) RecordMessage(ctx context.Context, in RecordMessageIn) (RecordMessageOut, error) {
	if in.Role == "" || in.Content == "" {
		return RecordMessageOut{}, fmt.Errorf("role and content required")
	}
	if in.ConversationID == uuid.Nil && (in.Model == "" || in.Provider == "" || in.ClientID == "") {
		return RecordMessageOut{}, fmt.Errorf("model, provider, client_id required when opening a new conversation")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RecordMessageOut{}, err
	}
	defer tx.Rollback(ctx)

	convID := in.ConversationID
	if convID == uuid.Nil {
		convID = uuid.New()
		if _, err := tx.Exec(ctx,
			`INSERT INTO conversations (id, client_id, model, provider) VALUES ($1,$2,$3,$4)`,
			convID, in.ClientID, in.Model, in.Provider); err != nil {
			return RecordMessageOut{}, fmt.Errorf("insert conversation: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE conversations SET updated_at = now() WHERE id = $1`, convID); err != nil {
			return RecordMessageOut{}, fmt.Errorf("touch conversation: %w", err)
		}
	}

	msgID := uuid.New()
	if _, err := tx.Exec(ctx,
		`INSERT INTO messages (id, conversation_id, role, content, tool_calls, token_count)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		msgID, convID, in.Role, in.Content, in.ToolCalls, in.TokenCount); err != nil {
		return RecordMessageOut{}, fmt.Errorf("insert message: %w", err)
	}

	// Naive chunking: whole message = one chunk, zero embedding.
	// Real chunker + embedder lands in internal/embed.
	zero := pgvector.NewVector(make([]float32, 384))
	if _, err := tx.Exec(ctx,
		`INSERT INTO chunks (message_id, conversation_id, chunk_index, content, embedding, token_count)
		 VALUES ($1,$2,0,$3,$4,$5)`,
		msgID, convID, in.Content, zero, in.TokenCount); err != nil {
		return RecordMessageOut{}, fmt.Errorf("insert chunk: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RecordMessageOut{}, err
	}
	return RecordMessageOut{MessageID: msgID, ConversationID: convID}, nil
}

type Conversation struct {
	ID        uuid.UUID
	ClientID  string
	Model     string
	Provider  string
	Title     *string
	StartedAt time.Time
	EndedAt   *time.Time
	Metadata  []byte
	UpdatedAt time.Time
}

type Message struct {
	ID         uuid.UUID
	Role       string
	Content    string
	ToolCalls  []byte
	TokenCount int
	CreatedAt  time.Time
}

type GetConversationIn struct {
	ConversationID uuid.UUID
	After          time.Time
	Limit          int
}

type GetConversationOut struct {
	Conversation Conversation
	Messages     []Message
	NextAfter    *time.Time
}

func (s *Store) GetConversation(ctx context.Context, in GetConversationIn) (GetConversationOut, error) {
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 100
	}

	var conv Conversation
	if err := s.pool.QueryRow(ctx,
		`SELECT id, client_id, model, provider, title, started_at, ended_at, metadata, updated_at
		 FROM conversations WHERE id = $1`, in.ConversationID).Scan(
		&conv.ID, &conv.ClientID, &conv.Model, &conv.Provider, &conv.Title,
		&conv.StartedAt, &conv.EndedAt, &conv.Metadata, &conv.UpdatedAt); err != nil {
		return GetConversationOut{}, fmt.Errorf("load conversation: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, role, content, tool_calls, token_count, created_at
		 FROM messages
		 WHERE conversation_id = $1 AND created_at > $2
		 ORDER BY created_at ASC
		 LIMIT $3`,
		in.ConversationID, in.After, in.Limit+1)
	if err != nil {
		return GetConversationOut{}, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.ToolCalls, &m.TokenCount, &m.CreatedAt); err != nil {
			return GetConversationOut{}, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return GetConversationOut{}, err
	}

	out := GetConversationOut{Conversation: conv, Messages: msgs}
	if len(msgs) > in.Limit {
		out.Messages = msgs[:in.Limit]
		next := out.Messages[len(out.Messages)-1].CreatedAt
		out.NextAfter = &next
	}
	return out, nil
}
