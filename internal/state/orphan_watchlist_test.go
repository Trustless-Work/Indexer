package state

// Tests for the orphan-watchlist recovery path: the cursor file is gone
// but the watchlist file survives. Before this path existed, Load
// returned a bare ErrStateNotFound without reading the watchlist, and
// the first Save of the "fresh" boot overwrote it — silently destroying
// tombstones and gap evidence. The documented operator move that caused
// it: deleting only STATE_PATH to "reset to tip".

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// orphanFixture saves a full state, then deletes ONLY the cursor file —
// the exact incident shape — and reopens the store as a fresh process
// would.
func orphanFixture(t *testing.T) (*FileStore, State) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	full := State{
		Network:         "Test SDF Network",
		LastLedgerSeq:   42,
		LastLedgerHash:  "abc123",
		EscrowContracts: []string{"CESCROW_ALIVE"},
		RemovedEscrows:  []string{"CESCROW_TOMBSTONED"},
		Gaps: []Gap{{
			FromLedger: 10,
			ToLedger:   20,
			Reason:     "rpc_retention",
			DetectedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		}},
	}
	if err := s.Save(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("deleting cursor file: %v", err)
	}

	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	return reopened, full
}

func TestFileStore_OrphanWatchlist_LoadPreservesEvidence(t *testing.T) {
	s, want := orphanFixture(t)

	got, err := s.Load(context.Background())
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound (the cursor IS gone), got %v", err)
	}
	if got.LastLedgerSeq != 0 || got.LastLedgerHash != "" {
		t.Errorf("orphan load invented a cursor: seq=%d hash=%q", got.LastLedgerSeq, got.LastLedgerHash)
	}
	if got.Network != want.Network {
		t.Errorf("network = %q, want %q", got.Network, want.Network)
	}
	if len(got.EscrowContracts) != 1 || got.EscrowContracts[0] != "CESCROW_ALIVE" {
		t.Errorf("escrows = %v, want the surviving watchlist", got.EscrowContracts)
	}
	if len(got.RemovedEscrows) != 1 || got.RemovedEscrows[0] != "CESCROW_TOMBSTONED" {
		t.Errorf("tombstones = %v, want the surviving set", got.RemovedEscrows)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].FromLedger != 10 || got.Gaps[0].ToLedger != 20 {
		t.Errorf("gaps = %+v, want the surviving evidence", got.Gaps)
	}
}

// The incident regression: after an orphan load, a Save that carries the
// recovered fields forward must leave the watchlist file intact — before
// the fix, this Save was the destruction.
func TestFileStore_OrphanWatchlist_SaveDoesNotDestroyEvidence(t *testing.T) {
	s, want := orphanFixture(t)

	recovered, err := s.Load(context.Background())
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}

	fresh := State{
		Network:         want.Network,
		LastLedgerSeq:   100,
		LastLedgerHash:  "def456",
		EscrowContracts: recovered.EscrowContracts,
		RemovedEscrows:  recovered.RemovedEscrows,
		Gaps:            recovered.Gaps,
	}
	if err := s.Save(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLedgerSeq != 100 {
		t.Errorf("cursor = %d, want 100", got.LastLedgerSeq)
	}
	if len(got.RemovedEscrows) != 1 || got.RemovedEscrows[0] != "CESCROW_TOMBSTONED" {
		t.Errorf("tombstones lost across the recovery save: %v", got.RemovedEscrows)
	}
	if len(got.Gaps) != 1 {
		t.Errorf("gap evidence lost across the recovery save: %+v", got.Gaps)
	}
}

func TestFileStore_OrphanWatchlist_CorruptRefusesBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(watchlistPath(path), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.Load(context.Background()); !errors.Is(err, ErrStateCorrupted) {
		t.Fatalf("expected ErrStateCorrupted for a corrupt orphan watchlist, got %v", err)
	}
}

// No files at all stays a plain fresh start (the common first boot).
func TestFileStore_OrphanWatchlist_AbsentIsFreshStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.Load(context.Background())
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound, got %v", err)
	}
	if len(got.EscrowContracts)+len(got.RemovedEscrows)+len(got.Gaps) != 0 {
		t.Errorf("fresh start carries data: %+v", got)
	}
}
