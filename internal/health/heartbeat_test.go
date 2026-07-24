package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// waitHits polls until the server saw want hits or the deadline passes —
// beats are asynchronous, so tests must wait, not assume.
func waitHits(t *testing.T, hits *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("hits = %d, want %d before deadline", hits.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHeartbeat_ThrottlesToOnePerInterval(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := NewHeartbeat(srv.URL)
	clock := newFakeClock()
	h.now = clock.now

	// A burst of ledgers inside one interval → exactly one ping.
	for range 50 {
		h.Beat(context.Background())
	}
	waitHits(t, &hits, 1)
	if got := hits.Load(); got != 1 {
		t.Fatalf("hits after burst = %d, want exactly 1", got)
	}

	// Once the interval passes, the next ledger pings again.
	clock.advance(heartbeatInterval + time.Second)
	h.Beat(context.Background())
	waitHits(t, &hits, 2)
}

func TestHeartbeat_NilIsSafeAndSilent(t *testing.T) {
	var h *Heartbeat // HEALTH_HEARTBEAT_URL unset → NewHeartbeat("") → nil
	if NewHeartbeat("") != nil {
		t.Fatal("empty url must produce a nil (no-op) heartbeat")
	}
	h.Beat(context.Background()) // must not panic
}

func TestHeartbeat_FailureNeverBlocksAndRetriesNextInterval(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // monitor is broken
	}))
	defer srv.Close()

	h := NewHeartbeat(srv.URL)
	clock := newFakeClock()
	h.now = clock.now

	h.Beat(context.Background())
	waitHits(t, &hits, 1)

	// A failed ping must not mark lastBeat: the next Beat after the
	// in-flight one clears may retry immediately (silence would already
	// be alerting — the point is we keep trying, throttled by flight).
	clock.advance(heartbeatInterval + time.Second)
	h.Beat(context.Background())
	waitHits(t, &hits, 2)
}
