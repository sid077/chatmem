package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/sid077/chatmem/internal/store"
)

// importMessage is the shape one message must have in the input file.
// Extra fields are ignored so users can paste JSON that has other keys.
type importMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	TokenCount int   `json:"token_count,omitempty"`
	// Optional per-message overrides. If unset, the top-level --model / etc
	// flag values are used instead.
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
}

func newImportCmd() *cobra.Command {
	var (
		file       string
		model      string
		provider   string
		clientID   string
		convIDFlag string
		title      string
		stdin      bool
	)
	cmd := &cobra.Command{
		Use:   "import [-f FILE | --stdin] --model M --provider P --client-id C",
		Short: "Bulk-import an existing conversation transcript from a JSONL / JSON file",
		Long: `Read a transcript from a JSONL file (one message per line) or a JSON array,
insert every message into a single conversation, and print the new conversation id.

Input formats accepted:
  1. JSONL — one JSON object per line:
       {"role":"user","content":"hi"}
       {"role":"assistant","content":"hey"}
  2. JSON array of the same objects:
       [{"role":"user","content":"hi"},{"role":"assistant","content":"hey"}]

Per-message model/provider/client_id override the top-level flag defaults.
Everything else on each object (timestamps, tool calls, ids from the source
chat) is silently ignored — chatmem generates its own ids and timestamps.

To append to an existing chatmem conversation, pass --conversation-id.

Examples:
  # Piping a JSONL transcript
  cat chatgpt-export.jsonl | chatmem import --stdin \
    --model gpt-5 --provider openai --client-id chatgpt-web

  # From a file, appending to an existing conversation
  chatmem import -f ./followup.jsonl \
    --conversation-id 8a2f... \
    --model claude-opus-4-7 --provider anthropic --client-id claude-code`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonRoot(); err != nil {
				return err
			}
			if err := preflight(); err != nil {
				return err
			}
			if !stdin && file == "" {
				return fmt.Errorf("either -f FILE or --stdin is required")
			}
			if stdin && file != "" {
				return fmt.Errorf("choose one of -f FILE or --stdin, not both")
			}

			// Read + parse input
			var src io.Reader = os.Stdin
			if !stdin {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				src = f
			}
			msgs, err := parseTranscript(src)
			if err != nil {
				return err
			}
			if len(msgs) == 0 {
				return errors.New("no messages found in input")
			}

			var convID uuid.UUID
			if convIDFlag != "" {
				id, err := uuid.Parse(convIDFlag)
				if err != nil {
					return fmt.Errorf("--conversation-id %q is not a valid UUID: %w", convIDFlag, err)
				}
				convID = id
			}

			// Validate defaults are provided when opening a new conversation.
			// Per-message overrides let users get away with omitting the flag
			// for follow-ups on existing convs, but a brand-new conv needs
			// SOMETHING for each of model/provider/client_id.
			if convID == uuid.Nil {
				needFlag := func(name, val string) error {
					if val == "" && !allMessagesHave(msgs, name) {
						return fmt.Errorf("--%s is required (no per-message override on every line, and no conversation-id to append to)", name)
					}
					return nil
				}
				if err := needFlag("model", model); err != nil {
					return err
				}
				if err := needFlag("provider", provider); err != nil {
					return err
				}
				if err := needFlag("client-id", clientID); err != nil {
					return err
				}
			}

			s, cleanup, err := openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()

			fmt.Fprintf(os.Stderr, "importing %d messages...\n", len(msgs))
			out, err := bulkImport(cmd.Context(), s, msgs, bulkOpts{
				ConversationID: convID,
				Model:          model,
				Provider:       provider,
				ClientID:       clientID,
			})
			if err != nil {
				return err
			}

			fmt.Printf("imported %d messages into conversation %s\n", out.Count, out.ConversationID)
			if title != "" {
				fmt.Fprintf(os.Stderr, "(note: --title is stored client-side only for now; the LLM composes the actual Notion page title at synth time)\n")
			}
			fmt.Println()
			fmt.Println("Next steps:")
			fmt.Printf("  chatmem notion status                              # is notion connected?\n")
			fmt.Printf("  # From an LLM session with chatmem:\n")
			fmt.Printf("  #   \"call get_synthesis_prompt for conversation %s, then synthesize_to_notion\"\n", out.ConversationID)
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "path to a JSONL or JSON-array transcript file")
	cmd.Flags().BoolVar(&stdin, "stdin", false, "read transcript from stdin instead of a file")
	cmd.Flags().StringVar(&model, "model", "", "default model id for messages without per-message override")
	cmd.Flags().StringVar(&provider, "provider", "", "default provider (anthropic|openai|...) for messages without per-message override")
	cmd.Flags().StringVar(&clientID, "client-id", "", "default client id (claude-code|cursor|windsurf|chatgpt-web|...) — the surface where the original chat happened")
	cmd.Flags().StringVar(&convIDFlag, "conversation-id", "", "append to this existing chatmem conversation UUID instead of opening a new one")
	cmd.Flags().StringVar(&title, "title", "", "unused today; reserved for future use")
	return cmd
}

func allMessagesHave(msgs []importMessage, field string) bool {
	for _, m := range msgs {
		switch field {
		case "model":
			if m.Model == "" {
				return false
			}
		case "provider":
			if m.Provider == "" {
				return false
			}
		case "client-id":
			if m.ClientID == "" {
				return false
			}
		}
	}
	return true
}

// parseTranscript auto-detects JSONL vs JSON-array by peeking at the first
// non-whitespace byte. Anything else is an error.
func parseTranscript(r io.Reader) ([]importMessage, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimLeft(string(buf), " \t\r\n")
	if trimmed == "" {
		return nil, errors.New("empty input")
	}

	if trimmed[0] == '[' {
		var out []importMessage
		if err := json.Unmarshal(buf, &out); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
		return out, nil
	}
	if trimmed[0] == '{' {
		// JSONL — parse line by line
		var out []importMessage
		sc := bufio.NewScanner(strings.NewReader(string(buf)))
		sc.Buffer(make([]byte, 1024*1024), 64*1024*1024) // handle large messages
		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var m importMessage
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				return nil, fmt.Errorf("parse JSONL line %d: %w", lineNo, err)
			}
			out = append(out, m)
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, errors.New("input must start with '{' (JSONL) or '[' (JSON array)")
}

type bulkOpts struct {
	ConversationID uuid.UUID
	Model          string
	Provider       string
	ClientID       string
}

type bulkResult struct {
	ConversationID uuid.UUID
	Count          int
}

func bulkImport(ctx context.Context, s *store.Store, msgs []importMessage, opts bulkOpts) (bulkResult, error) {
	convID := opts.ConversationID
	for i, m := range msgs {
		if strings.TrimSpace(m.Role) == "" || strings.TrimSpace(m.Content) == "" {
			return bulkResult{}, fmt.Errorf("message %d: role and content are required", i+1)
		}
		in := store.RecordMessageIn{
			ConversationID: convID,
			Role:           m.Role,
			Content:        m.Content,
			TokenCount:     m.TokenCount,
			Model:          firstNonEmpty(m.Model, opts.Model),
			Provider:       firstNonEmpty(m.Provider, opts.Provider),
			ClientID:       firstNonEmpty(m.ClientID, opts.ClientID),
		}
		res, err := s.RecordMessage(ctx, in)
		if err != nil {
			return bulkResult{}, fmt.Errorf("record message %d/%d: %w", i+1, len(msgs), err)
		}
		convID = res.ConversationID
	}
	return bulkResult{ConversationID: convID, Count: len(msgs)}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
