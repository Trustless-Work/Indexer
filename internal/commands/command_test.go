package commands

import (
	"errors"
	"testing"
	"time"
)

func TestValidate_AcceptsEveryKindWithItsArguments(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
	}{
		{"track", Command{Kind: KindTrackEscrow, ContractID: "CABC"}},
		{"refresh", Command{Kind: KindRefreshEscrow, ContractID: "CABC"}},
		{"reseed", Command{Kind: KindReseed, ContractIDs: []string{"CABC", "CDEF"}}},
		{"remove", Command{Kind: KindRemoveEscrow, ContractID: "CABC"}},
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
