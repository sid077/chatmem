package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	pendingDirName  = "pending"
	pendingMaxAge   = 24 * time.Hour
	defaultInterval = 5 * time.Minute
	httpTimeout     = 5 * time.Second
	maxHTTPAttempts = 3
	ingestURLEnvVar = "CHATMEM_TELEMETRY_URL"
	intervalEnvVar  = "CHATMEM_TELEMETRY_INTERVAL"
)

// defaultIngestURL is baked in at release time via ldflag:
//
//	-X 'github.com/sid077/chatmem/internal/telemetry.defaultIngestURL=https://...'
//
// The Options.IngestURL field and CHATMEM_TELEMETRY_URL env var still
// override this. Empty at dev-build time = local-only mode.
var defaultIngestURL = ""

type Client struct {
	state      State
	log        *slog.Logger
	agg        *Aggregator
	dataHome   string
	ingestURL  string
	version    string
	interval   time.Duration
	httpClient *http.Client
	rng        *rand.Rand
}

type Options struct {
	Version    string
	IngestURL  string
	Interval   time.Duration
	HTTPClient *http.Client
}

// NewClient constructs a telemetry client bound to the given state and
// data directory. Environment variables (CHATMEM_TELEMETRY_URL,
// CHATMEM_TELEMETRY_INTERVAL) override the Options fields.
func NewClient(state State, dataHome string, log *slog.Logger, opts Options) *Client {
	if opts.Interval == 0 {
		opts.Interval = defaultInterval
	}
	if v := os.Getenv(intervalEnvVar); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			opts.Interval = d
		}
	}
	// Precedence for the ingest URL: env var > explicit opts > baked-in default.
	if opts.IngestURL == "" {
		opts.IngestURL = defaultIngestURL
	}
	if v := os.Getenv(ingestURLEnvVar); v != "" {
		opts.IngestURL = v
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	return &Client{
		state:      state,
		log:        log,
		agg:        NewAggregator(),
		dataHome:   dataHome,
		ingestURL:  opts.IngestURL,
		version:    opts.Version,
		interval:   opts.Interval,
		httpClient: opts.HTTPClient,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Aggregator returns the accumulator MCP handlers write into.
// It is always safe to call, even when telemetry is disabled — writes
// simply never leave the machine because Flush is a no-op in that case.
func (c *Client) Aggregator() *Aggregator { return c.agg }

// IngestURL returns the effective ingest URL after applying the
// env-var > opts > baked-in-default precedence chain.
func (c *Client) IngestURL() string { return c.ingestURL }

// Start launches the periodic flush goroutine. It runs until ctx is
// cancelled, at which point a final flush is attempted. Safe to call
// even when telemetry is disabled (the loop just idles and does nothing).
func (c *Client) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(c.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				// One last flush on shutdown — use a short-lived detached
				// context so we don't block the parent's shutdown forever.
				last, cancel := context.WithTimeout(context.Background(), httpTimeout+time.Second)
				c.Flush(last)
				cancel()
				return
			case <-t.C:
				c.Flush(ctx)
			}
		}
	}()
}

// Flush drains any pending files first, then snapshots and ships the
// current window. Failed shipments become new pending files. No-op when
// telemetry is disabled OR the window is empty.
func (c *Client) Flush(ctx context.Context) {
	if !c.state.Enabled {
		return
	}
	c.drainPending(ctx)
	if c.agg.IsEmpty() {
		return
	}
	snap := c.agg.Snapshot()
	p := c.buildPayload(snap)
	if err := c.post(ctx, p); err != nil {
		c.log.Debug("telemetry flush failed, persisting to pending", "err", err)
		c.persistPending(p)
	}
}

type Payload struct {
	InstallID   string                  `json:"install_id"`
	Version     string                  `json:"version"`
	WindowStart time.Time               `json:"window_start"`
	WindowEnd   time.Time               `json:"window_end"`
	Events      Events                  `json:"events"`
	Latency     map[string]LatencyStats `json:"latency"`
}

func (c *Client) buildPayload(snap Snapshot) Payload {
	return Payload{
		InstallID:   c.state.InstallID,
		Version:     c.version,
		WindowStart: snap.WindowStart,
		WindowEnd:   snap.WindowEnd,
		Events:      snap.Events,
		Latency:     snap.Latency,
	}
}

func (c *Client) post(ctx context.Context, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if c.ingestURL == "" {
		// No sink configured. Log a summary at INFO so the user can see
		// telemetry is working locally even without an ingest server.
		c.log.Info("telemetry flush (local-only, no ingest URL set)",
			"install_id", c.state.InstallID,
			"captures", p.Events.Captures,
			"searches", p.Events.Searches,
			"gets", p.Events.Gets,
			"errors", p.Events.Errors,
			"payload_bytes", len(body))
		return nil
	}

	url := c.ingestURL + "/v1/ping"
	var lastErr error
	for attempt := 0; attempt < maxHTTPAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * time.Second
			jitter := time.Duration(c.rng.Intn(500)) * time.Millisecond
			select {
			case <-time.After(backoff + jitter):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", fmt.Sprintf("chatmem/%s (install:%s)", c.version, c.state.InstallID))
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("http %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("http %d (permanent)", resp.StatusCode)
		}
		return nil
	}
	return lastErr
}

func (c *Client) pendingDir() string {
	return filepath.Join(c.dataHome, pendingDirName)
}

func (c *Client) persistPending(p Payload) {
	if err := os.MkdirAll(c.pendingDir(), 0o755); err != nil {
		c.log.Warn("telemetry mkdir pending", "err", err)
		return
	}
	name := time.Now().UTC().Format("20060102T150405.000000000") + ".json"
	path := filepath.Join(c.pendingDir(), name)
	b, err := json.Marshal(p)
	if err != nil {
		c.log.Warn("telemetry marshal pending", "err", err)
		return
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		c.log.Warn("telemetry write pending", "err", err)
	}
}

func (c *Client) drainPending(ctx context.Context) {
	entries, err := os.ReadDir(c.pendingDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-pendingMaxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(c.pendingDir(), e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Payload
		if err := json.Unmarshal(b, &p); err != nil {
			_ = os.Remove(path)
			continue
		}
		if err := c.post(ctx, p); err == nil {
			_ = os.Remove(path)
		}
	}
}

// PendingFiles returns the paths of pending payloads that haven't been
// shipped yet. Debug helper used by `chatmem telemetry dump`.
func (c *Client) PendingFiles() ([]string, error) {
	entries, err := os.ReadDir(c.pendingDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, filepath.Join(c.pendingDir(), e.Name()))
		}
	}
	return out, nil
}
