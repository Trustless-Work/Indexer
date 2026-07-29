package processors

import (
	"context"
	"testing"
	"time"

	"github.com/Trustless-Work/Indexer/internal/indexer/registry"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
)

// emptyRPC answers every getLedgerEntries with a successful, EMPTY result —
// the shape a degraded provider produces (a node still resyncing, a load
// balancer routing to a replica without the data, a silently truncated
// response). It is indistinguishable, on the wire, from "none of these
// entries exist", which is precisely what makes the guard necessary.
type emptyRPC struct{ calls int }

func (r *emptyRPC) GetLedgerEntries(
	context.Context,
	protocol.GetLedgerEntriesRequest,
) (protocol.GetLedgerEntriesResponse, error) {
	r.calls++
	return protocol.GetLedgerEntriesResponse{}, nil
}

// contractIDs mints n distinct, well-formed C... strkeys so the detector
// actually builds ledger keys for them and counts them as requested.
func contractIDs(t *testing.T, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := range n {
		var raw [32]byte
		raw[0], raw[1] = byte(i+1), byte(i>>8)
		id, err := strkey.Encode(strkey.VersionByteContract, raw[:])
		if err != nil {
			t.Fatalf("encode contract id %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func detectorOver(t *testing.T, rpc LedgerEntryGetter, ids []string) *EscrowStateDetector {
	t.Helper()
	reg, err := registry.New(nil)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	reg.Seed(ids)
	return NewEscrowStateDetector(rpc, reg)
}

// The regression this whole guard exists for. Before it, one empty response
// to a sweep batch published a "removed" per escrow, and the consumer
// applied every one of them to real rows — the sweep rotating over the
// watchlist would have wiped the read-model in a handful of ledgers.
func TestFetchStates_EmptyRPCResponseRemovesNobody(t *testing.T) {
	ids := contractIDs(t, 20)
	rpc := &emptyRPC{}
	d := detectorOver(t, rpc, ids)

	changes, err := d.FetchStates(context.Background(), ids, 100, time.Now().UTC())
	if err != nil {
		t.Fatalf("FetchStates: %v", err)
	}

	if len(changes) != 0 {
		t.Fatalf("published %d changes from an empty response, want 0: %+v", len(changes), changes)
	}
	for _, c := range changes {
		if c.StateChangeType == "removed" {
			t.Fatalf("published a removal for %s off an empty response", c.EscrowID)
		}
	}
	if rpc.calls == 0 {
		t.Fatal("the detector never queried the RPC; the test is not exercising the path")
	}
}

// The guard has to leave a number behind: a silent guard would trade one
// invisible failure (mass false removal) for another (nobody notices the
// provider is broken).
func TestFetchStates_SuppressedRemovalsIsCounted(t *testing.T) {
	ids := contractIDs(t, 20)
	d := detectorOver(t, &emptyRPC{}, ids)

	if got := d.SuppressedRemovals(); got != 0 {
		t.Fatalf("counter starts at %d, want 0", got)
	}
	if _, err := d.FetchStates(context.Background(), ids, 100, time.Now().UTC()); err != nil {
		t.Fatalf("FetchStates: %v", err)
	}
	if got := d.SuppressedRemovals(); got != len(ids) {
		t.Errorf("suppressed = %d, want %d", got, len(ids))
	}
}

// The guard must not break the feature it protects. A small batch is the
// per-ledger activity path, where one escrow really can have just been
// withdrawn, so absence there is still reported as a removal.
func TestFetchStates_SmallBatchStillReportsRemovals(t *testing.T) {
	ids := contractIDs(t, 3)
	d := detectorOver(t, &emptyRPC{}, ids)

	changes, err := d.FetchStates(context.Background(), ids, 100, time.Now().UTC())
	if err != nil {
		t.Fatalf("FetchStates: %v", err)
	}

	if len(changes) != len(ids) {
		t.Fatalf("changes = %d, want %d removals", len(changes), len(ids))
	}
	for _, c := range changes {
		if c.StateChangeType != "removed" {
			t.Errorf("%s = %q, want removed", c.EscrowID, c.StateChangeType)
		}
	}
	if got := d.SuppressedRemovals(); got != 0 {
		t.Errorf("suppressed = %d, want 0 — the guard must not arm on a small batch", got)
	}
}

func TestIsImplausibleRemovalBatch(t *testing.T) {
	cases := []struct {
		name               string
		requested, missing int
		want               bool
	}{
		{"small batch, all gone, is organic", 3, 3, false},
		{"just under the minimum batch", removalGuardMinBatch - 1, removalGuardMinBatch - 1, false},
		{"at the minimum, all gone", removalGuardMinBatch, removalGuardMinBatch, true},
		{"big batch, all gone", 200, 200, true},
		{"big batch, exactly half gone, still believed", 200, 100, false},
		{"big batch, just over half gone", 200, 101, true},
		{"big batch, a realistic handful gone", 200, 3, false},
		{"nothing missing", 200, 0, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := isImplausibleRemovalBatch(tt.requested, tt.missing); got != tt.want {
				t.Errorf("isImplausibleRemovalBatch(%d, %d) = %v, want %v",
					tt.requested, tt.missing, got, tt.want)
			}
		})
	}
}
