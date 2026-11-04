package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL     = "https://api.notion.com"
	notionAPIVersion   = "2022-06-28"
	defaultHTTPTimeout = 20 * time.Second
	maxHTTPAttempts    = 4
)

// Client is a thin wrapper around the 4 Notion API endpoints chatmem needs.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient constructs a client bound to the given integration token.
// baseURL is exposed for tests (httptest); pass "" for the real API.
func NewClient(token, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: defaultHTTPTimeout},
	}
}

// User is what /v1/users/me returns (subset we care about).
type User struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Bot    *Bot   `json:"bot,omitempty"`
}
type Bot struct {
	Owner       map[string]any `json:"owner,omitempty"`
	WorkspaceName string       `json:"workspace_name,omitempty"`
}

// Ping verifies the token is valid. Used by connect/status/doctor.
func (c *Client) Ping(ctx context.Context) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/v1/users/me", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Page is Notion's page resource (subset we need).
type Page struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreatePage POSTs a new page under parentPageID with the given title and body blocks.
func (c *Client) CreatePage(ctx context.Context, parentPageID, title string, children []Block) (*Page, error) {
	body := map[string]any{
		"parent": map[string]any{"page_id": parentPageID},
		"properties": map[string]any{
			"title": map[string]any{
				"title": []any{
					map[string]any{
						"type": "text",
						"text": map[string]any{"content": truncateTitle(title)},
					},
				},
			},
		},
		"children": children,
	}
	var out Page
	if err := c.do(ctx, http.MethodPost, "/v1/pages", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePageTitle rewrites just the title property (used on re-synthesis).
func (c *Client) UpdatePageTitle(ctx context.Context, pageID, title string) error {
	body := map[string]any{
		"properties": map[string]any{
			"title": map[string]any{
				"title": []any{
					map[string]any{"type": "text", "text": map[string]any{"content": truncateTitle(title)}},
				},
			},
		},
	}
	return c.do(ctx, http.MethodPatch, "/v1/pages/"+pageID, body, nil)
}

type childListResp struct {
	Results    []Block `json:"results"`
	NextCursor string  `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// ListChildren pages through all direct children of pageID.
func (c *Client) ListChildren(ctx context.Context, pageID string) ([]Block, error) {
	var all []Block
	cursor := ""
	for {
		path := "/v1/blocks/" + pageID + "/children?page_size=100"
		if cursor != "" {
			path += "&start_cursor=" + cursor
		}
		var resp childListResp
		if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Results...)
		if !resp.HasMore || resp.NextCursor == "" {
			return all, nil
		}
		cursor = resp.NextCursor
	}
}

// DeleteBlock archives (soft-deletes) a block by id.
func (c *Client) DeleteBlock(ctx context.Context, blockID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/blocks/"+blockID, nil, nil)
}

// AppendChildren PATCHes children onto pageID, split into batches that fit
// Notion's per-request limits (see batchBlocks).
func (c *Client) AppendChildren(ctx context.Context, pageID string, children []Block) error {
	for _, batch := range batchBlocks(children) {
		body := map[string]any{"children": batch}
		if err := c.do(ctx, http.MethodPatch, "/v1/blocks/"+pageID+"/children", body, nil); err != nil {
			return err
		}
	}
	return nil
}

// ReplacePageBody clears every child of pageID and writes newChildren.
// Used for re-synthesis so the URL stays stable across page rewrites.
func (c *Client) ReplacePageBody(ctx context.Context, pageID string, newChildren []Block) error {
	existing, err := c.ListChildren(ctx, pageID)
	if err != nil {
		return fmt.Errorf("list existing children: %w", err)
	}
	for _, b := range existing {
		id := b.ID()
		if id == "" {
			continue
		}
		if err := c.DeleteBlock(ctx, id); err != nil {
			return fmt.Errorf("delete block %s: %w", id, err)
		}
	}
	return c.AppendChildren(ctx, pageID, newChildren)
}

// do executes an authenticated JSON request against the Notion API with
// exponential backoff. Body may be nil; out may be nil (discarded).
//
// Creating a page and appending children are not idempotent: after a
// network error or a 5xx the request may already have been applied, and
// retrying it duplicates the page or its blocks. Those are retried only on
// 429, which Notion documents as "not processed"; any other failure goes
// back to the caller, whose pending-write retry knows the page id.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	url := c.baseURL + path
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}
	idempotent := !(method == http.MethodPost ||
		(method == http.MethodPatch && strings.HasSuffix(path, "/children")))

	var (
		lastErr    error
		retryAfter time.Duration
	)
	for attempt := 0; attempt < maxHTTPAttempts; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<attempt)*time.Second + time.Duration(rand.IntN(500))*time.Millisecond
			if retryAfter > wait {
				wait = retryAfter
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bytesReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Notion-Version", notionAPIVersion)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			if !idempotent {
				return err
			}
			lastErr = err
			continue
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("notion %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 200))
			if resp.StatusCode != 429 && !idempotent {
				return lastErr
			}
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
			continue
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("notion %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(respBody), 400))
		}
		if out != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("notion: unknown failure")
	}
	return lastErr
}

func bytesReader(b []byte) *bytes.Reader {
	if b == nil {
		return bytes.NewReader(nil)
	}
	return bytes.NewReader(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// maxRetryAfter bounds how long a single Retry-After can stall a write.
const maxRetryAfter = 60 * time.Second

// parseRetryAfter reads Notion's Retry-After header (whole seconds).
func parseRetryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

// Notion caps page titles at 2000 chars; be conservative for readability.
func truncateTitle(s string) string {
	const max = 200
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return truncateBytes(s, max-len("…")) + "…"
}
