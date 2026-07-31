package store

import (
	_ "embed"
	"context"
	"fmt"
	"strings"
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
	MessageID              uuid.UUID
	ConversationID         uuid.UUID
	MessagesSinceLastSynth int    // counter after this write
	LastMessageAt          time.Time
	NotionPageURL          string // "" if never synthesized
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
			`INSERT INTO conversations (id, client_id, model, provider, last_message_at, messages_since_last_synth)
			 VALUES ($1,$2,$3,$4, now(), 1)`,
			convID, in.ClientID, in.Model, in.Provider); err != nil {
			return RecordMessageOut{}, fmt.Errorf("insert conversation: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE conversations
			   SET updated_at = now(),
			       last_message_at = now(),
			       messages_since_last_synth = messages_since_last_synth + 1
			 WHERE id = $1`, convID); err != nil {
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

	// Snapshot the post-write conversation state for the synth-hint decision.
	var out RecordMessageOut
	out.MessageID = msgID
	out.ConversationID = convID
	if err := tx.QueryRow(ctx,
		`SELECT messages_since_last_synth, last_message_at, COALESCE(notion_page_url, '')
		 FROM conversations WHERE id = $1`, convID).
		Scan(&out.MessagesSinceLastSynth, &out.LastMessageAt, &out.NotionPageURL); err != nil {
		return RecordMessageOut{}, fmt.Errorf("read conv state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RecordMessageOut{}, err
	}
	return out, nil
}

// RecordSynthesis marks a conversation as freshly synthesized. Called by the
// notion writer after a successful Notion API call. Resets messages_since_last_synth.
type RecordSynthesisIn struct {
	ConversationID uuid.UUID
	PageID         string
	PageURL        string
	SessionType    string
	SummaryHash    string
}

func (s *Store) RecordSynthesis(ctx context.Context, in RecordSynthesisIn) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversations
		   SET notion_page_id = $2,
		       notion_page_url = $3,
		       notion_session_type = $4,
		       notion_summary_hash = $5,
		       notion_synthesized_at = now(),
		       messages_since_last_synth = 0
		 WHERE id = $1`,
		in.ConversationID, in.PageID, in.PageURL, in.SessionType, in.SummaryHash)
	if err != nil {
		return fmt.Errorf("update synth state: %w", err)
	}
	return nil
}

// NotionPage is a row in the local index of published pages.
type NotionPage struct {
	ConversationID uuid.UUID
	PageID         string
	URL            string
	SessionType    string
	SynthesizedAt  time.Time
	SummaryHash    string
	Model          string
	ClientID       string
}

// ListNotionPages returns all conversations that have been synthesized,
// most recent first. Used by `chatmem notion list` and the list_notion_pages MCP tool.
func (s *Store) ListNotionPages(ctx context.Context, limit int) ([]NotionPage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.notion_page_id, c.notion_page_url,
		        COALESCE(c.notion_session_type, ''), c.notion_synthesized_at,
		        COALESCE(c.notion_summary_hash, ''), c.model, c.client_id
		 FROM conversations c
		 WHERE c.notion_page_id IS NOT NULL
		 ORDER BY c.notion_synthesized_at DESC NULLS LAST
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotionPage
	for rows.Next() {
		var p NotionPage
		if err := rows.Scan(&p.ConversationID, &p.PageID, &p.URL, &p.SessionType,
			&p.SynthesizedAt, &p.SummaryHash, &p.Model, &p.ClientID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SynthContext bundles everything the notion synthesis pipeline needs about
// one conversation, in a single DB call.
type SynthContext struct {
	ConversationID     uuid.UUID
	Model              string
	Provider           string
	ClientID           string
	NotionPageID       string
	NotionSummaryHash  string
	SynthesizedVersion int // number of successful past synths; 0 = never
	Messages           []Message
}

// GetSynthContext loads the conversation's notion state + all messages.
// Used by both get_synthesis_prompt (needs messages) and synthesize_to_notion
// (needs messages + notion state).
func (s *Store) GetSynthContext(ctx context.Context, convID uuid.UUID) (SynthContext, error) {
	var out SynthContext
	out.ConversationID = convID

	var pageID, hash *string
	var synthedAt *time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT model, provider, client_id,
		        notion_page_id, notion_summary_hash, notion_synthesized_at
		 FROM conversations WHERE id = $1`, convID).
		Scan(&out.Model, &out.Provider, &out.ClientID,
			&pageID, &hash, &synthedAt); err != nil {
		return SynthContext{}, fmt.Errorf("load conv metadata: %w", err)
	}
	if pageID != nil {
		out.NotionPageID = *pageID
	}
	if hash != nil {
		out.NotionSummaryHash = *hash
	}
	if synthedAt != nil {
		out.SynthesizedVersion = 1 // count is fine as "at least once"; the callers only need version+1
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, role, content, tool_calls, token_count, created_at
		 FROM messages
		 WHERE conversation_id = $1
		 ORDER BY created_at ASC`, convID)
	if err != nil {
		return SynthContext{}, fmt.Errorf("load messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.ToolCalls, &m.TokenCount, &m.CreatedAt); err != nil {
			return SynthContext{}, err
		}
		out.Messages = append(out.Messages, m)
	}
	return out, rows.Err()
}

// FactIn is one atomic fact extracted from a message. Category and
// importance are LLM judgments; chatmem does not interpret them beyond
// coverage bookkeeping.
type FactIn struct {
	MessageID  uuid.UUID
	Category   string // concept | decision | command | error | reference | question | code | diagram-hint | insight
	Text       string
	Importance string // critical | normal | trivial
}

// Fact is a stored fact row.
type Fact struct {
	ID             uuid.UUID
	ConversationID uuid.UUID
	MessageID      uuid.UUID
	Category       string
	Text           string
	Importance     string
	CreatedAt      time.Time
}

// RecordFacts bulk-inserts facts for a conversation in one transaction.
// Facts are additive — calling twice for the same message stacks facts;
// dedup is the LLM's job.
func (s *Store) RecordFacts(ctx context.Context, convID uuid.UUID, in []FactIn) error {
	if len(in) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, f := range in {
		if f.MessageID == uuid.Nil || f.Category == "" || strings.TrimSpace(f.Text) == "" {
			return fmt.Errorf("facts[%d]: message_id, category, text are required", i)
		}
		imp := f.Importance
		if imp == "" {
			imp = "normal"
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO synth_facts (conversation_id, message_id, category, text, importance)
			 VALUES ($1, $2, $3, $4, $5)`,
			convID, f.MessageID, f.Category, f.Text, imp); err != nil {
			return fmt.Errorf("insert fact %d: %w", i, err)
		}
	}
	return tx.Commit(ctx)
}

// GetFacts returns every fact for a conversation ordered by (message
// created_at, fact created_at) so the LLM sees them chronologically.
func (s *Store) GetFacts(ctx context.Context, convID uuid.UUID) ([]Fact, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT f.id, f.conversation_id, f.message_id, f.category, f.text, f.importance, f.created_at
		 FROM synth_facts f
		 JOIN messages m ON m.id = f.message_id
		 WHERE f.conversation_id = $1
		 ORDER BY m.created_at ASC, f.created_at ASC`, convID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.ConversationID, &f.MessageID, &f.Category, &f.Text, &f.Importance, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UnextractedMessages returns messages that don't yet have any fact.
// Used by the chunked extraction loop.
func (s *Store) UnextractedMessages(ctx context.Context, convID uuid.UUID, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 40
	}
	rows, err := s.pool.Query(ctx,
		`SELECT m.id, m.role, m.content, m.tool_calls, m.token_count, m.created_at
		 FROM messages m
		 WHERE m.conversation_id = $1
		   AND NOT EXISTS (SELECT 1 FROM synth_facts f WHERE f.message_id = m.id)
		 ORDER BY m.created_at ASC
		 LIMIT $2`, convID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.ToolCalls, &m.TokenCount, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CoverageReport is what coverage checks return.
type CoverageReport struct {
	TotalMessages       int
	MessagesWithFacts   int
	MessagesRequiringCitation int      // = non-trivia fact messages, or all messages if no facts extracted
	CitedMessages       int
	MissedMessageIDs    []uuid.UUID // non-trivia msgs not in cited_from
	Ratio               float64     // CitedMessages / MessagesRequiringCitation
	FactCounts          map[string]int // category -> count
}

// MessageCoverage compares the caller-supplied set of cited message ids
// against the set of messages that carry at least one non-trivia fact
// (or all messages, if no facts were extracted yet).
func (s *Store) MessageCoverage(ctx context.Context, convID uuid.UUID, cited []uuid.UUID) (CoverageReport, error) {
	var report CoverageReport
	report.FactCounts = map[string]int{}

	// Total messages
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE conversation_id = $1`, convID).
		Scan(&report.TotalMessages); err != nil {
		return report, err
	}

	// Messages with any fact + category counts
	rows, err := s.pool.Query(ctx,
		`SELECT category, count(*) FROM synth_facts WHERE conversation_id = $1 GROUP BY category`, convID)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var cat string
		var n int
		if err := rows.Scan(&cat, &n); err != nil {
			rows.Close()
			return report, err
		}
		report.FactCounts[cat] = n
	}
	rows.Close()

	// The set of messages that require citation:
	//   - if any facts exist: messages with at least one non-trivia fact
	//   - if no facts: all messages
	requiredRows, err := s.pool.Query(ctx,
		`WITH nontrivia AS (
		    SELECT DISTINCT message_id FROM synth_facts
		    WHERE conversation_id = $1 AND importance <> 'trivial'
		 )
		 SELECT id FROM messages
		 WHERE conversation_id = $1
		   AND (
		     (SELECT count(*) FROM synth_facts WHERE conversation_id = $1) = 0
		     OR id IN (SELECT message_id FROM nontrivia)
		   )`, convID)
	if err != nil {
		return report, err
	}
	required := map[uuid.UUID]bool{}
	for requiredRows.Next() {
		var id uuid.UUID
		if err := requiredRows.Scan(&id); err != nil {
			requiredRows.Close()
			return report, err
		}
		required[id] = true
	}
	requiredRows.Close()
	report.MessagesRequiringCitation = len(required)

	// Messages with facts (any importance)
	if err := s.pool.QueryRow(ctx,
		`SELECT count(DISTINCT message_id) FROM synth_facts WHERE conversation_id = $1`, convID).
		Scan(&report.MessagesWithFacts); err != nil {
		return report, err
	}

	// Coverage
	citedSet := map[uuid.UUID]bool{}
	for _, id := range cited {
		citedSet[id] = true
	}
	for id := range required {
		if citedSet[id] {
			report.CitedMessages++
		} else {
			report.MissedMessageIDs = append(report.MissedMessageIDs, id)
		}
	}
	if report.MessagesRequiringCitation == 0 {
		report.Ratio = 1.0
	} else {
		report.Ratio = float64(report.CitedMessages) / float64(report.MessagesRequiringCitation)
	}
	return report, nil
}

// RecordCoverage persists the coverage snapshot to the conversation row so
// `chatmem notion coverage <id>` can show what was included / missed.
func (s *Store) RecordCoverage(ctx context.Context, convID uuid.UUID, r CoverageReport) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE conversations
		    SET notion_coverage_ratio = $2,
		        notion_covered_msg_ids = $3,
		        notion_missed_msg_ids = $4
		  WHERE id = $1`,
		convID, r.Ratio,
		coverageIDs(r.MessagesRequiringCitation, r.MissedMessageIDs, /*covered=*/ true),
		r.MissedMessageIDs)
	return err
}

// coverageIDs is a helper that returns the caller-friendly slice for the
// covered_msg_ids column. Currently we only persist missed; covered is
// derived on read to keep the row small.
func coverageIDs(_ int, missed []uuid.UUID, covered bool) []uuid.UUID {
	if covered {
		return nil // not persisted directly; missed is enough
	}
	return missed
}

// ConversationsNeedingSynth returns conversations past the auto-fire threshold,
// used by the background sweep to build the stale-synth log.
type NeedsSynth struct {
	ConversationID         uuid.UUID
	MessagesSinceLastSynth int
	LastMessageAt          time.Time
	IsNewPage              bool
}

func (s *Store) ConversationsNeedingSynth(ctx context.Context, messageThreshold, minMessages int, idleCutoff time.Time) ([]NeedsSynth, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, messages_since_last_synth, last_message_at, notion_page_id IS NULL
		 FROM conversations
		 WHERE messages_since_last_synth >= $1
		    OR (messages_since_last_synth >= $2 AND last_message_at < $3)`,
		messageThreshold, minMessages, idleCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NeedsSynth
	for rows.Next() {
		var n NeedsSynth
		if err := rows.Scan(&n.ConversationID, &n.MessagesSinceLastSynth, &n.LastMessageAt, &n.IsNewPage); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
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

type SearchHit struct {
	MessageID      uuid.UUID
	ConversationID uuid.UUID
	Role           string
	Snippet        string
	Score          float64
	TokenCount     int
	CreatedAt      time.Time
}

type SearchHistoryIn struct {
	Query           string
	TopK            int
	TokenBudget     int
	Model           string
	ClientID        string
	Since           *time.Time
	Until           *time.Time
	ConversationIDs []uuid.UUID
}

type SearchHistoryOut struct {
	Hits []SearchHit
}

// SearchHistory does keyword-based ranking via Postgres full-text search
// (to_tsvector + plainto_tsquery). Semantic re-ranking via chunk embeddings
// is a TODO once the ONNX embedder lands — the schema is already set up for it.
func (s *Store) SearchHistory(ctx context.Context, in SearchHistoryIn) (SearchHistoryOut, error) {
	if in.Query == "" {
		return SearchHistoryOut{}, fmt.Errorf("query required")
	}
	if in.TopK <= 0 || in.TopK > 100 {
		in.TopK = 10
	}
	if in.TokenBudget <= 0 {
		in.TokenBudget = 4000
	}

	args := []any{in.Query}
	where := []string{"to_tsvector('english', ch.content) @@ plainto_tsquery('english', $1)"}
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if in.Model != "" {
		where = append(where, "c.model = "+next(in.Model))
	}
	if in.ClientID != "" {
		where = append(where, "c.client_id = "+next(in.ClientID))
	}
	if in.Since != nil {
		where = append(where, "m.created_at >= "+next(*in.Since))
	}
	if in.Until != nil {
		where = append(where, "m.created_at <= "+next(*in.Until))
	}
	if len(in.ConversationIDs) > 0 {
		where = append(where, "ch.conversation_id = ANY("+next(in.ConversationIDs)+")")
	}

	// Fetch a superset — token-budget packing may trim.
	fetch := in.TopK * 3
	sql := fmt.Sprintf(`
		SELECT ch.message_id, ch.conversation_id, m.role, ch.content, ch.token_count, m.created_at,
		       ts_rank_cd(to_tsvector('english', ch.content), plainto_tsquery('english', $1)) AS score
		FROM chunks ch
		JOIN messages m ON m.id = ch.message_id
		JOIN conversations c ON c.id = ch.conversation_id
		WHERE %s
		ORDER BY score DESC, m.created_at DESC
		LIMIT %d`, strings.Join(where, " AND "), fetch)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return SearchHistoryOut{}, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var (
		out    SearchHistoryOut
		budget = in.TokenBudget
		seen   = map[uuid.UUID]bool{}
	)
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.MessageID, &h.ConversationID, &h.Role, &h.Snippet, &h.TokenCount, &h.CreatedAt, &h.Score); err != nil {
			return SearchHistoryOut{}, err
		}
		if seen[h.ConversationID] {
			continue // MMR-lite: one hit per conversation
		}
		// Fall back to a rough token estimate if store didn't record one.
		est := h.TokenCount
		if est == 0 {
			est = len(h.Snippet) / 4
		}
		if len(out.Hits) > 0 && est > budget {
			break
		}
		budget -= est
		seen[h.ConversationID] = true
		out.Hits = append(out.Hits, h)
		if len(out.Hits) >= in.TopK {
			break
		}
	}
	return out, rows.Err()
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
