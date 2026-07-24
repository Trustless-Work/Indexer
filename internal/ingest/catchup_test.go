package ingest

import (
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestShouldCheckpoint(t *testing.T) {
	tests := []struct {
		name          string
		catchingUp    bool
		isFinal       bool
		current, last uint32
		want          bool
	}{
		{name: "at the tip saves every ledger", catchingUp: false, current: 101, last: 100, want: true},
		{name: "catching up inside the window skips", catchingUp: true, current: 150, last: 100, want: false},
		{name: "catching up at the interval saves", catchingUp: true, current: 200, last: 100, want: true},
		{name: "final backfill ledger always saves", catchingUp: true, isFinal: true, current: 101, last: 100, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldCheckpoint(tt.catchingUp, tt.isFinal, tt.current, tt.last); got != tt.want {
				t.Fatalf("shouldCheckpoint = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDrainPending_MergesSortsAndClears(t *testing.T) {
	pending := map[string]struct{}{"CB": {}, "CA": {}}

	out := drainPending(pending, []string{"CC", "CA"}) // overlap is fine

	if len(out) != 3 || out[0] != "CA" || out[1] != "CB" || out[2] != "CC" {
		t.Fatalf("drained = %v, want [CA CB CC]", out)
	}
	if len(pending) != 0 {
		t.Fatalf("pending not cleared: %v", pending)
	}
}

// metaWithPrevHash fabricates the minimal LedgerCloseMeta the continuity
// check reads: sequence + parent hash.
func metaWithPrevHash(seq uint32, prev xdr.Hash) xdr.LedgerCloseMeta {
	return xdr.LedgerCloseMeta{
		V: 0,
		V0: &xdr.LedgerCloseMetaV0{
			LedgerHeader: xdr.LedgerHeaderHistoryEntry{
				Header: xdr.LedgerHeader{
					LedgerSeq:          xdr.Uint32(seq),
					PreviousLedgerHash: prev,
				},
			},
		},
	}
}

func TestVerifyChainContinuity(t *testing.T) {
	parent := xdr.Hash{0xAB, 0xCD}
	meta := metaWithPrevHash(101, parent)

	// Matching hash (case-insensitively): contiguous chain, no error.
	if err := verifyChainContinuity(strings.ToUpper(parent.HexString()), meta); err != nil {
		t.Fatalf("contiguous resume should pass: %v", err)
	}

	// A different parent means the RPC serves another chain — fatal, with
	// both hashes and the remediation in the message.
	err := verifyChainContinuity(xdr.Hash{0x01}.HexString(), meta)
	if err == nil {
		t.Fatal("diverged chain must error")
	}
	for _, want := range []string{"state diverged", "101", "archive the state file"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}
