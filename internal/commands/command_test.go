package commands

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParse_ValidCommands(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Kind
	}{
		{"track", `{"command":"track_escrow","contract_id":"CABC"}`, KindTrackEscrow},
		{"refresh", `{"command":"refresh_escrow","contract_id":"CABC"}`, KindRefreshEscrow},
		{"reseed", `{"command":"reseed","contract_ids":["CABC","CDEF"]}`, KindReseed},
		{"remove", `{"command":"remove_escrow","contract_id":"CABC"}`, KindRemoveEscrow},
		{"pause", `{"command":"pause","ttl_seconds":600}`, KindPause},
		{"resume", `{"command":"resume"}`, KindResume},
		{"reconcile", `{"command":"reconcile"}`, KindReconcile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if cmd.Kind != tt.want {
				t.Errorf("kind = %s, want %s", cmd.Kind, tt.want)
			}
		})
	}
}

func TestParse_RejectsUnusableCommands(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"command":`},
		{"unknown kind", `{"command":"drop_database"}`},
		{"track without id", `{"command":"track_escrow"}`},
		{"refresh without id", `{"command":"refresh_escrow"}`},
		{"reseed empty", `{"command":"reseed","contract_ids":[]}`},
		{"pause negative ttl", `{"command":"pause","ttl_seconds":-1}`},
		{"pause over max ttl", `{"command":"pause","ttl_seconds":7200}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.body)); !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("expected ErrInvalidCommand, got %v", err)
			}
		})
	}
}

func TestParse_ReseedBatchCap(t *testing.T) {
	ids := make([]string, MaxReseedBatch+1)
	for i := range ids {
		ids[i] = "C"
	}
	body := `{"command":"reseed","contract_ids":["` + strings.Join(ids, `","`) + `"]}`
	if _, err := Parse([]byte(body)); !errors.Is(err, ErrInvalidCommand) {
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
