package health

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetrics_ExposesTrackerSnapshot(t *testing.T) {
	clock := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	tr := newTrackerAt("testnet", func() time.Time { return clock })
	tr.RecordLedger(Progress{
		LedgerSeq:      1234,
		LedgerClosedAt: clock.Add(-5 * time.Second),
		KnownEscrows:   220,
		Events:         3,
		StateChanges:   2,
		Gaps:           1,
	})

	h := Handler(tr, nil, MetricsHandler(NewRegistry(tr)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	body := rr.Body.String()
	for _, want := range []string{
		`indexer_current_ledger{network="testnet"} 1234`,
		`indexer_known_escrows{network="testnet"} 220`,
		`indexer_events_published_total{network="testnet"} 3`,
		`indexer_state_changes_published_total{network="testnet"} 2`,
		`indexer_gaps_recorded{network="testnet"} 1`,
		`indexer_ready{network="testnet"} 1`,
		`indexer_paused{network="testnet"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output lacks %q", want)
		}
	}
}
