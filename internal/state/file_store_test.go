package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStore_SaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Load(context.Background()); !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("expected ErrStateNotFound on first load, got %v", err)
	}

	want := State{Network: "Test SDF Network", LastLedgerSeq: 42, EscrowContracts: []string{"CESCROW"}}
	if err := s.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", got.Version, CurrentVersion)
	}
	if got.LastLedgerSeq != 42 || got.Network != "Test SDF Network" || len(got.EscrowContracts) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestFileStore_GapsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	detected := time.Date(2026, 7, 22, 15, 4, 5, 0, time.UTC)
	want := State{
		Network:       "Test SDF Network",
		LastLedgerSeq: 100,
		Gaps: []Gap{
			{FromLedger: 10, ToLedger: 49, Reason: "rpc_retention", DetectedAt: detected},
		},
	}
	if err := s.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Gaps) != 1 {
		t.Fatalf("gaps = %+v, want exactly 1", got.Gaps)
	}
	g := got.Gaps[0]
	if g.FromLedger != 10 || g.ToLedger != 49 || g.Reason != "rpc_retention" || !g.DetectedAt.Equal(detected) {
		t.Fatalf("gap round-trip mismatch: %+v", g)
	}
}

// A state file written before the Gaps field existed must load cleanly —
// the field is additive and versionless by design.
func TestFileStore_PreGapsFileLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := []byte(`{"version":1,"network":"Test SDF Network","last_ledger_seq":7,"escrow_contracts":["CESCROW"]}`)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLedgerSeq != 7 || got.Gaps != nil {
		t.Fatalf("legacy load mismatch: %+v", got)
	}
}

func TestFileStore_LockHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s1, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()

	if _, err := NewFileStore(path); !errors.Is(err, ErrStateLockHeld) {
		t.Fatalf("expected ErrStateLockHeld for a second opener, got %v", err)
	}
}

func TestFileStore_SplitLayoutOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	st := State{
		Network:         "net",
		LastLedgerSeq:   7,
		LastLedgerHash:  "abc",
		EscrowContracts: []string{"C1", "C2"},
		RemovedEscrows:  []string{"C9"},
		Gaps:            []Gap{{FromLedger: 1, ToLedger: 3, Reason: "rpc_retention", DetectedAt: time.Now().UTC()}},
	}
	if err := s.Save(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	// The cursor file must be tiny: no escrow fields in it.
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cur) > 200 {
		t.Errorf("cursor file weighs %d bytes; the split exists to keep it tiny", len(cur))
	}
	if string(cur) != "" && (contains(cur, "escrow_contracts") || contains(cur, "C1")) {
		t.Errorf("cursor file must not carry the watchlist: %s", cur)
	}

	wl, err := os.ReadFile(watchlistPath(path))
	if err != nil {
		t.Fatalf("watchlist file missing: %v", err)
	}
	for _, want := range []string{"C1", "C2", "C9", "rpc_retention"} {
		if !contains(wl, want) {
			t.Errorf("watchlist file lacks %q: %s", want, wl)
		}
	}

	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EscrowContracts) != 2 || len(got.RemovedEscrows) != 1 || len(got.Gaps) != 1 || got.LastLedgerSeq != 7 {
		t.Fatalf("merged load mismatch: %+v", got)
	}
}

func TestFileStore_UnchangedWatchlistIsNotRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	st := State{Network: "net", LastLedgerSeq: 1, EscrowContracts: []string{"C1"}}
	if err := s.Save(context.Background(), st); err != nil {
		t.Fatal(err)
	}

	// Delete the watchlist file: an unchanged watchlist on the next Save
	// must NOT bring it back — proof the write was skipped.
	if err := os.Remove(watchlistPath(path)); err != nil {
		t.Fatal(err)
	}
	st.LastLedgerSeq = 2
	if err := s.Save(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(watchlistPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unchanged watchlist was rewritten (write amplification is back)")
	}

	// A REAL change writes it again.
	st.LastLedgerSeq = 3
	st.EscrowContracts = []string{"C1", "C2"}
	if err := s.Save(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(watchlistPath(path)); err != nil {
		t.Fatalf("changed watchlist was not written: %v", err)
	}
}

func TestFileStore_LegacyCombinedFileMigrates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	legacy := `{"version":1,"network":"net","last_ledger_seq":99,"last_ledger_hash":"ff","escrow_contracts":["COLD"],"removed_escrows":["CGONE"],"gaps":[{"from_ledger":5,"to_ledger":6,"reason":"rpc_retention","detected_at":"2026-07-22T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.LastLedgerSeq != 99 || len(got.EscrowContracts) != 1 || len(got.RemovedEscrows) != 1 || len(got.Gaps) != 1 {
		t.Fatalf("legacy load mismatch: %+v", got)
	}

	// First Save migrates to the split layout; a reload still sees all.
	if err := s.Save(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(path)
	if contains(cur, "COLD") {
		t.Errorf("cursor file still carries legacy watchlist after migration: %s", cur)
	}
	reloaded, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.EscrowContracts) != 1 || reloaded.EscrowContracts[0] != "COLD" || len(reloaded.Gaps) != 1 {
		t.Fatalf("post-migration reload mismatch: %+v", reloaded)
	}
}

// contains reports whether sub occurs in b (test readability helper).
func contains(b []byte, sub string) bool {
	return strings.Contains(string(b), sub)
}
