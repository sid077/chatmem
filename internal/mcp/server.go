package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sid077/chatmem/internal/notion"
	"github.com/sid077/chatmem/internal/store"
	"github.com/sid077/chatmem/internal/telemetry"
)

// Deps bundles the runtime dependencies the MCP server needs. Callers pass
// a partially-populated Deps; nil fields disable the corresponding feature
// (tests pass nil for Aggregator + NotionWriter for speed/isolation).
type Deps struct {
	Store        *store.Store
	Aggregator   *telemetry.Aggregator
	NotionWriter *notion.Writer // nil = notion tools return "not configured"
	Version      string
}

// NewServer constructs an MCP server for the given deps.
func NewServer(d Deps) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{
		Name:    "chatmem",
		Version: d.Version,
	}, nil)
	registerRecordMessage(s, d)
	registerGetConversation(s, d.Store, d.Aggregator)
	registerSearchHistory(s, d.Store, d.Aggregator)
	registerGetSynthesisPrompt(s, d)
	registerSynthesizeToNotion(s, d)
	registerListNotionPages(s, d)
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
	MessageID          string `json:"message_id"`
	ConversationID     string `json:"conversation_id"`
	SuggestSynthesize  bool   `json:"suggest_synthesize,omitempty"`
	SynthesizeReason   string `json:"synthesize_reason,omitempty"`
	MessagesSinceSynth int    `json:"messages_since_last_synth,omitempty"`
}

// shouldSuggestSynth applies the auto-fire thresholds against post-write
// conversation state. Returns (suggest, humanReadableReason).
func shouldSuggestSynth(cfg *notion.Config, res store.RecordMessageOut) (bool, string) {
	if cfg == nil || !cfg.AutoSynthesize.Enabled {
		return false, ""
	}
	a := cfg.AutoSynthesize
	if res.MessagesSinceLastSynth >= a.MessageThreshold {
		return true, fmt.Sprintf("conversation has %d new messages since last synth (threshold %d)",
			res.MessagesSinceLastSynth, a.MessageThreshold)
	}
	// Idle-timer path is only useful when the daemon sweep fires it. On a
	// per-message basis, we can't tell "idle" — the message just arrived.
	// So the hint from record_message is purely count-based; idle is
	// enforced by the daemon sweep instead.
	_ = a.IdleMinutes
	_ = a.MinMessages
	return false, ""
}

func registerRecordMessage(s *sdk.Server, d Deps) {
	st := d.Store
	agg := d.Aggregator
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
			MessageID:          res.MessageID.String(),
			ConversationID:     res.ConversationID.String(),
			MessagesSinceSynth: res.MessagesSinceLastSynth,
		}

		// Auto-fire hint: consult the notion config (if any) and add the
		// suggest_synthesize field so the client-side LLM can chain into
		// get_synthesis_prompt + synthesize_to_notion.
		var cfg *notion.Config
		if d.NotionWriter != nil {
			c := d.NotionWriter.Config()
			cfg = &c
		}
		if suggest, reason := shouldSuggestSynth(cfg, res); suggest {
			out.SuggestSynthesize = true
			out.SynthesizeReason = reason
		}

		text := fmt.Sprintf("recorded message %s in conversation %s", out.MessageID, out.ConversationID)
		if out.SuggestSynthesize {
			text += fmt.Sprintf("\n\n[chatmem hint] suggest_synthesize=true — %s. Call get_synthesis_prompt then synthesize_to_notion for conversation %s before continuing.",
				out.SynthesizeReason, out.ConversationID)
		}

		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: text}},
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

// ─── notion synthesis tools (v0.2.0) ─────────────────────────────────────

type getSynthesisPromptArgs struct {
	ConversationID string `json:"conversation_id" jsonschema:"UUID of the conversation to summarize"`
}

type getSynthesisPromptOut struct {
	Prompt         string `json:"prompt"`
	ConversationID string `json:"conversation_id"`
	MessageCount   int    `json:"message_count"`
}

func registerGetSynthesisPrompt(s *sdk.Server, d Deps) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "get_synthesis_prompt",
		Description: "Returns the full brief the LLM should follow to compose a Summary payload for synthesize_to_notion. Includes the transcript, the JSON schema, quality rules, and message UUIDs the LLM must cite. Call this first; then call synthesize_to_notion with the summary you compose.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args getSynthesisPromptArgs) (*sdk.CallToolResult, getSynthesisPromptOut, error) {
		if d.NotionWriter == nil {
			return nil, getSynthesisPromptOut{}, fmt.Errorf("notion integration is not configured — run `chatmem notion connect <token> --parent <page-id>` on the host machine")
		}
		convID, err := uuid.Parse(args.ConversationID)
		if err != nil {
			return nil, getSynthesisPromptOut{}, fmt.Errorf("invalid conversation_id: %w", err)
		}
		sc, err := d.Store.GetSynthContext(ctx, convID)
		if err != nil {
			return nil, getSynthesisPromptOut{}, err
		}
		if len(sc.Messages) == 0 {
			return nil, getSynthesisPromptOut{}, fmt.Errorf("conversation %s has no messages", convID)
		}
		pc := notion.PromptConversation{
			ConversationID: convID.String(),
			Model:          sc.Model,
			Provider:       sc.Provider,
			ClientID:       sc.ClientID,
			Messages:       make([]notion.PromptMessage, 0, len(sc.Messages)),
		}
		for _, m := range sc.Messages {
			pc.Messages = append(pc.Messages, notion.PromptMessage{
				ID: m.ID.String(), Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt,
			})
		}
		prompt := notion.BuildSynthesisPrompt(pc)
		out := getSynthesisPromptOut{
			Prompt:         prompt,
			ConversationID: convID.String(),
			MessageCount:   len(sc.Messages),
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: prompt}},
		}, out, nil
	})
}

type synthesizeArgs struct {
	ConversationID string          `json:"conversation_id" jsonschema:"UUID of the conversation"`
	Summary        json.RawMessage `json:"summary" jsonschema:"structured Summary object matching the schema in get_synthesis_prompt"`
	Force          bool            `json:"force,omitempty" jsonschema:"true = rewrite the Notion page even if the summary hash is unchanged"`
}

type synthesizeOut struct {
	ConversationID string `json:"conversation_id"`
	NotionPageID   string `json:"notion_page_id"`
	NotionURL      string `json:"notion_url"`
	Skipped        bool   `json:"skipped,omitempty"`
	Version        int    `json:"version"`
	SessionType    string `json:"session_type"`
}

func registerSynthesizeToNotion(s *sdk.Server, d Deps) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "synthesize_to_notion",
		Description: "Publish a structured summary of a conversation as a Notion page (or update the existing one). Pass the Summary object composed per get_synthesis_prompt's schema. Fails with a validation error if required fields are missing or cited_from UUIDs don't match this conversation's messages — retry with fixes.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args synthesizeArgs) (*sdk.CallToolResult, synthesizeOut, error) {
		if d.NotionWriter == nil {
			return nil, synthesizeOut{}, fmt.Errorf("notion integration is not configured — run `chatmem notion connect <token> --parent <page-id>`")
		}
		convID, err := uuid.Parse(args.ConversationID)
		if err != nil {
			return nil, synthesizeOut{}, fmt.Errorf("invalid conversation_id: %w", err)
		}
		var summary notion.Summary
		if err := json.Unmarshal(args.Summary, &summary); err != nil {
			return nil, synthesizeOut{}, fmt.Errorf("summary is not valid JSON: %w", err)
		}

		sc, err := d.Store.GetSynthContext(ctx, convID)
		if err != nil {
			return nil, synthesizeOut{}, err
		}
		if len(sc.Messages) == 0 {
			return nil, synthesizeOut{}, fmt.Errorf("conversation %s has no messages", convID)
		}

		// Build the validation UUID set from the actual messages so
		// fabricated citations get caught.
		validUUIDs := make(map[string]bool, len(sc.Messages))
		for _, m := range sc.Messages {
			validUUIDs[strings.ToLower(m.ID.String())] = true
		}
		if err := summary.Validate(validUUIDs); err != nil {
			return nil, synthesizeOut{}, err
		}

		// Build transcript for the "Full Transcript" section.
		tx := notion.Transcript{Turns: make([]notion.TranscriptTurn, 0, len(sc.Messages))}
		for _, m := range sc.Messages {
			tx.Turns = append(tx.Turns, notion.TranscriptTurn{
				MessageID: m.ID.String(), Role: m.Role, Content: m.Content, CreatedAt: m.CreatedAt,
			})
		}

		version := 1
		if sc.NotionPageID != "" {
			version = sc.SynthesizedVersion + 1
		}
		res, err := d.NotionWriter.Synthesize(ctx, notion.SynthesizeIn{
			Summary: summary,
			Meta: notion.RenderMeta{
				ConversationID: convID.String(),
				Model:          sc.Model, Provider: sc.Provider, ClientID: sc.ClientID,
				SynthesizedAt: time.Now().UTC(),
				Version:       version,
			},
			Transcript:     tx,
			ExistingPageID: sc.NotionPageID,
			PreviousHash:   sc.NotionSummaryHash,
			Force:          args.Force,
		})
		if err != nil {
			return nil, synthesizeOut{}, err
		}

		// Persist the resulting page identity + reset the synth counter.
		if err := d.Store.RecordSynthesis(ctx, store.RecordSynthesisIn{
			ConversationID: convID,
			PageID:         res.PageID,
			PageURL:        res.URL,
			SessionType:    string(summary.SessionType),
			SummaryHash:    res.SummaryHash,
		}); err != nil {
			return nil, synthesizeOut{}, fmt.Errorf("notion page written, but failed to persist state locally: %w", err)
		}

		out := synthesizeOut{
			ConversationID: convID.String(),
			NotionPageID:   res.PageID,
			NotionURL:      res.URL,
			Skipped:        res.Skipped,
			Version:        version,
			SessionType:    string(summary.SessionType),
		}

		text := ""
		if res.Skipped {
			text = fmt.Sprintf("Skipped — summary hash unchanged since last synthesis.\nPage: %s", out.NotionURL)
		} else if sc.NotionPageID == "" {
			text = fmt.Sprintf("Created Notion page (session_type=%s):\n%s", summary.SessionType, out.NotionURL)
		} else {
			text = fmt.Sprintf("Updated Notion page to version %d (session_type=%s):\n%s",
				version, summary.SessionType, out.NotionURL)
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: text}},
		}, out, nil
	})
}

type listNotionPagesArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"max pages to return (default 50, max 500)"`
}

type notionPageDTO struct {
	ConversationID string    `json:"conversation_id"`
	URL            string    `json:"url"`
	SessionType    string    `json:"session_type"`
	Model          string    `json:"model"`
	ClientID       string    `json:"client_id"`
	SynthesizedAt  time.Time `json:"synthesized_at"`
}

type listNotionPagesOut struct {
	Pages []notionPageDTO `json:"pages"`
}

func registerListNotionPages(s *sdk.Server, d Deps) {
	sdk.AddTool(s, &sdk.Tool{
		Name:        "list_notion_pages",
		Description: "List conversations that have already been published to Notion by chatmem. Useful for the LLM to check whether a topic is already covered before proposing another synthesis, or to link back to related pages.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, args listNotionPagesArgs) (*sdk.CallToolResult, listNotionPagesOut, error) {
		limit := args.Limit
		if limit <= 0 {
			limit = 50
		}
		pages, err := d.Store.ListNotionPages(ctx, limit)
		if err != nil {
			return nil, listNotionPagesOut{}, err
		}
		out := listNotionPagesOut{Pages: make([]notionPageDTO, 0, len(pages))}
		var b strings.Builder
		if len(pages) == 0 {
			b.WriteString("No conversations have been synthesized to Notion yet.\n")
		} else {
			fmt.Fprintf(&b, "%d notion page(s):\n", len(pages))
		}
		for _, p := range pages {
			out.Pages = append(out.Pages, notionPageDTO{
				ConversationID: p.ConversationID.String(),
				URL:            p.URL,
				SessionType:    p.SessionType,
				Model:          p.Model,
				ClientID:       p.ClientID,
				SynthesizedAt:  p.SynthesizedAt,
			})
			fmt.Fprintf(&b, "\n%s [%s] %s\n  %s\n  synthesized %s\n",
				p.ConversationID, p.SessionType, p.Model, p.URL, p.SynthesizedAt.Format(time.RFC3339))
		}
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: b.String()}},
		}, out, nil
	})
}
