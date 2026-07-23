package telemetry

import (
	"sort"
	"sync"
	"time"
)

// Aggregator is a thread-safe in-memory accumulator for the current flush
// window. Snapshot() returns a copy AND resets the internal state, so the
// caller can serialize + ship the result while the next window fills up.
type Aggregator struct {
	mu       sync.Mutex
	events   Events
	latency  map[string]*reservoir
	started  time.Time
	capacity int
}

type Events struct {
	Captures int64            `json:"captures"`
	Searches int64            `json:"searches"`
	Gets     int64            `json:"gets"`
	Errors   int64            `json:"errors"`
	Models   map[string]int64 `json:"models"`
	Clients  map[string]int64 `json:"clients"`
}

type Snapshot struct {
	WindowStart time.Time               `json:"window_start"`
	WindowEnd   time.Time               `json:"window_end"`
	Events      Events                  `json:"events"`
	Latency     map[string]LatencyStats `json:"latency"`
}

type LatencyStats struct {
	Count int64   `json:"count"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
	P99   float64 `json:"p99"`
}

const defaultReservoirCap = 500

func NewAggregator() *Aggregator {
	return &Aggregator{
		events:   newEvents(),
		latency:  map[string]*reservoir{},
		started:  time.Now().UTC(),
		capacity: defaultReservoirCap,
	}
}

func newEvents() Events {
	return Events{
		Models:  map[string]int64{},
		Clients: map[string]int64{},
	}
}

// RecordCapture bumps the capture counter, the model+client distributions,
// and adds an observation to the "capture" latency reservoir.
func (a *Aggregator) RecordCapture(model, client string, elapsed time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events.Captures++
	if model != "" {
		a.events.Models[model]++
	}
	if client != "" {
		a.events.Clients[client]++
	}
	a.observeLocked("capture", elapsed)
}

func (a *Aggregator) RecordSearch(elapsed time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events.Searches++
	a.observeLocked("search", elapsed)
}

func (a *Aggregator) RecordGet(elapsed time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events.Gets++
	a.observeLocked("get", elapsed)
}

// RecordError bumps the error counter under the "error" op label so
// error-path latency is not conflated with the success-path samples.
func (a *Aggregator) RecordError(op string, elapsed time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events.Errors++
	a.observeLocked("error_"+op, elapsed)
}

// Snapshot atomically returns the current window and resets the aggregator
// so the next window starts fresh.
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	snap := Snapshot{
		WindowStart: a.started,
		WindowEnd:   time.Now().UTC(),
		Events:      a.events,
		Latency:     make(map[string]LatencyStats, len(a.latency)),
	}
	for op, r := range a.latency {
		snap.Latency[op] = r.stats()
	}
	// reset for next window
	a.events = newEvents()
	a.latency = map[string]*reservoir{}
	a.started = time.Now().UTC()
	return snap
}

// IsEmpty reports whether the current window contains anything worth flushing.
// Used to skip zero-event pings so the ingest sees only meaningful data.
func (a *Aggregator) IsEmpty() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.events.Captures+a.events.Searches+a.events.Gets+a.events.Errors == 0
}

func (a *Aggregator) observeLocked(op string, d time.Duration) {
	r, ok := a.latency[op]
	if !ok {
		r = newReservoir(a.capacity)
		a.latency[op] = r
	}
	r.add(float64(d) / float64(time.Millisecond))
}

// reservoir is a bounded ring of the last N observations. For MVP the
// "last N" bias vs. true random reservoir sampling is acceptable — flush
// windows are short and event volume is low.
type reservoir struct {
	buf []float64
	cap int
}

func newReservoir(cap int) *reservoir { return &reservoir{cap: cap} }

func (r *reservoir) add(v float64) {
	if len(r.buf) < r.cap {
		r.buf = append(r.buf, v)
		return
	}
	copy(r.buf, r.buf[1:])
	r.buf[len(r.buf)-1] = v
}

func (r *reservoir) stats() LatencyStats {
	if len(r.buf) == 0 {
		return LatencyStats{}
	}
	xs := make([]float64, len(r.buf))
	copy(xs, r.buf)
	sort.Float64s(xs)
	var sum float64
	for _, v := range xs {
		sum += v
	}
	return LatencyStats{
		Count: int64(len(xs)),
		Min:   xs[0],
		Max:   xs[len(xs)-1],
		Mean:  sum / float64(len(xs)),
		P50:   percentile(xs, 50),
		P95:   percentile(xs, 95),
		P99:   percentile(xs, 99),
	}
}

// percentile uses the nearest-rank method (ceiling form) on a sorted slice.
// Not exact linear interpolation, but plenty for coarse observability.
// For N=100 samples 1..100 this returns P50=50, P95=95, P99=99.
func percentile(sorted []float64, p int) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	// ceil(p/100 * N) with integer math
	idx := (p*len(sorted) + 99) / 100
	if idx > 0 {
		idx--
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
