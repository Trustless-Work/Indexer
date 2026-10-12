package commands

import (
	"errors"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/strkey"
)

// testCID returns a syntactically valid contract address (C-strkey)
// derived from b, for commands whose ids are strkey-validated.
func testCID(t *testing.T, b byte) string {
	t.Helper()
	raw := make([]byte, 32)
	raw[0] = b
	id, err := strkey.Encode(strkey.VersionByteContract, raw)
	if err != nil {
		t.Fatalf("encoding test contract id: %v", err)
	}
	return id
}

func TestValidate_AcceptsEveryKindWithItsArguments(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
	}{
		{"track", Command{Kind: KindTrackEscrow, ContractID: testCID(t, 1)}},
		{"refresh", Command{Kind: KindRefreshEscrow, ContractID: testCID(t, 1)}},
		{"reseed", Command{Kind: KindReseed, ContractIDs: []string{testCID(t, 1), testCID(t, 2)}}},
		{"remove", Command{Kind: KindRemoveEscrow, ContractID: "CABC-legacy-garbage"}},
		{"pause", Command{Kind: KindPause, TTLSeconds: 600}},
		{"resume", Command{Kind: KindResume}},
		{"reconcile", Command{Kind: KindReconcile}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cmd.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidate_RejectsUnusableCommands(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
	}{
		// The zero value must not be executable: a decode that populated
		// nothing has to fail here rather than reach the loop.
		{"empty kind", Command{}},
		{"unknown kind", Command{Kind: "drop_database"}},
		{"track without id", Command{Kind: KindTrackEscrow}},
		{"refresh without id", Command{Kind: KindRefreshEscrow}},
		{"remove without id", Command{Kind: KindRemoveEscrow}},
		{"reseed empty", Command{Kind: KindReseed}},
		{"pause negative ttl", Command{Kind: KindPause, TTLSeconds: -1}},
		{"pause over max ttl", Command{Kind: KindPause, TTLSeconds: 7200}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cmd.Validate(); !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("expected ErrInvalidCommand, got %v", err)
			}
		})
	}
}

func TestValidate_ReseedBatchCap(t *testing.T) {
	ids := make([]string, MaxReseedBatch+1)
	for i := range ids {
		ids[i] = "C"
	}
	if err := (Command{Kind: KindReseed, ContractIDs: ids}).Validate(); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("expected the over-cap reseed to be rejected, got %v", err)
	}
}

func TestPauseTTL_DefaultsAndCaps(t *testing.T) {
	if got := (Command{Kind: KindPause}).PauseTTL(); got != DefaultPauseTTL {
		t.Errorf("zero ttl = %s, want default %s", got, DefaultPauseTTL)
	}
	if got := (Command{Kind: KindPause, TTLSeconds: 60}).PauseTTL(); got != time.Minute {
		t.Errorf("60s ttl = %s, want 1m", got)
	}
}

// Strkey validation: ids that will be QUERIED (track/refresh/reseed)
// must be real contract addresses; remove stays lenient because it is
// the cleanup path for garbage accepted before this validation existed.
func TestValidate_RejectsMalformedContractIDs(t *testing.T) {
	bad := "CABC" // truncated: not a decodable C-strkey
	tests := []struct {
		name string
		cmd  Command
	}{
		{"track", Command{Kind: KindTrackEscrow, ContractID: bad}},
		{"refresh", Command{Kind: KindRefreshEscrow, ContractID: bad}},
		{"reseed", Command{Kind: KindReseed, ContractIDs: []string{testCID(t, 1), bad}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmd.Validate()
			if !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("Validate accepted a malformed contract id: %v", err)
			}
		})
	}
}

func TestValidate_RemoveAcceptsLegacyGarbage(t *testing.T) {
	if err := (Command{Kind: KindRemoveEscrow, ContractID: "not-a-strkey"}).Validate(); err != nil {
		t.Fatalf("remove must stay lenient for legacy cleanup: %v", err)
	}
}
