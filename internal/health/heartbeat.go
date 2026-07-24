package health

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/stellar/go-stellar-sdk/support/log"
)

// heartbeatInterval throttles outbound pings: the loop processes a
// ledger every ~5s (and far faster during catch-up), but the monitor
// only needs "still alive" once a minute. One GET/min costs nothing and
// stays far inside any free-tier quota.
const heartbeatInterval = time.Minute

// heartbeatTimeout bounds the ping. The beat runs in its own goroutine,
// so this protects resources, not loop latency.
const heartbeatTimeout = 10 * time.Second

// heartbeatWarnEvery suppresses failure-log spam: if the monitor is
// down for an hour we want a handful of warnings, not one per minute.
const heartbeatWarnEvery = 10

// Heartbeat pushes a dead-man's-switch ping to an external monitor
// (Better Stack, Healthchecks.io — anything that accepts a GET).
//
// The signal is deliberately inverted from /healthz: the monitor alerts
// on SILENCE, so every failure mode that stops the ingest loop — crash
// loop, hung RPC, broker backpressure, the process simply gone — cuts
// the pings and fires the alert, without the monitor needing to reach
// into our network. Beat is called after each processed ledger; a
// stalled loop therefore goes quiet within one interval.
//
// Fire-and-forget by design: a ping must NEVER slow down or fail the
// pipeline it watches. Failures are logged (throttled) and otherwise
// ignored — if pings can't leave, the monitor alerts anyway, which is
// the correct outcome.
type Heartbeat struct {
	url    string
	client *http.Client
	now    func() time.Time

	mu       sync.Mutex
	lastBeat time.Time
	failures int
	inFlight bool
}

// NewHeartbeat builds a Heartbeat for url. An empty url returns nil —
// and every method on a nil Heartbeat is a safe no-op, so callers wire
// it unconditionally (same pattern as the tracker: no branching in the
// hot path).
func NewHeartbeat(url string) *Heartbeat {
	if url == "" {
		return nil
	}
	return &Heartbeat{
		url:    url,
		client: &http.Client{Timeout: heartbeatTimeout},
		now:    time.Now,
	}
}

// Beat sends one throttled, asynchronous ping. Safe to call from the
// ingest loop on every ledger; only one request is ever in flight.
func (h *Heartbeat) Beat(ctx context.Context) {
	if h == nil {
		return
	}

	h.mu.Lock()
	if h.inFlight || h.now().Sub(h.lastBeat) < heartbeatInterval {
		h.mu.Unlock()
		return
	}
	h.inFlight = true
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			h.inFlight = false
			h.mu.Unlock()
		}()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
		if err != nil {
			h.noteFailure(ctx, err)
			return
		}
		resp, err := h.client.Do(req)
		if err != nil {
			h.noteFailure(ctx, err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			h.noteFailure(ctx, nil)
			return
		}

		h.mu.Lock()
		h.lastBeat = h.now()
		h.failures = 0
		h.mu.Unlock()
	}()
}

// noteFailure counts a failed ping and logs a throttled warning. The
// pipeline is untouched: a monitor outage alerts BY silence, it must
// not create noise or pressure here.
func (h *Heartbeat) noteFailure(ctx context.Context, err error) {
	h.mu.Lock()
	h.failures++
	n := h.failures
	h.mu.Unlock()
	if n == 1 || n%heartbeatWarnEvery == 0 {
		log.Ctx(ctx).Warnf("Heartbeat ping failed (%d consecutive): %v — the uptime monitor will alert on silence", n, err)
	}
}
