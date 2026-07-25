package registry

import (
	"strings"
	"testing"
)

var (
	approvedHex = strings.Repeat("ab", 32) // a valid 32-byte hex hash
	otherHex    = strings.Repeat("cd", 32)
)

func TestNew_rejectsMalformedHash(t *testing.T) {
	if _, err := New([]string{"not-hex"}); err == nil {
		t.Fatal("expected error for malformed hash")
	}
	if _, err := New([]string{strings.Repeat("ab", 16)}); err == nil {
		t.Fatal("expected error for wrong-length hash")
	}
	if _, err := New([]string{approvedHex, "", "  "}); err != nil {
		t.Fatalf("blank entries should be skipped, got %v", err)
	}
}

func TestRegister_onlyApprovedHashes(t *testing.T) {
	r, err := New([]string{approvedHex})
	if err != nil {
		t.Fatal(err)
	}

	approved, _ := ParseHash(approvedHex)
	other, _ := ParseHash(otherHex)

	if !r.Register("CESCROW", approved) {
		t.Fatal("expected approved-hash contract to be registered")
	}
	if r.Register("CESCROW", approved) {
		t.Fatal("re-registering the same contract should return false")
	}
	if r.Register("CSTRANGER", other) {
		t.Fatal("non-approved hash must not be registered")
	}

	if !r.IsEscrow("CESCROW") {
		t.Fatal("CESCROW should be a known escrow")
	}
	if r.IsEscrow("CSTRANGER") {
		t.Fatal("CSTRANGER should not be a known escrow")
	}
	if r.IsEscrow("") {
		t.Fatal("empty contract id is never an escrow")
	}
	if r.Size() != 1 {
		t.Fatalf("expected size 1, got %d", r.Size())
	}
}

func TestSeed_bypassesHashCheck(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Seed([]string{"CSEEDED", "", "CSEEDED2"})
	if !r.IsEscrow("CSEEDED") || !r.IsEscrow("CSEEDED2") {
		t.Fatal("seeded contracts should be known escrows")
	}
	if r.Size() != 2 {
		t.Fatalf("expected size 2, got %d", r.Size())
	}
}

func TestRemove_TombstoneBlocksResurrection(t *testing.T) {
	approved := "aa" + strings.Repeat("00", 30) + "aa"
	r, err := New([]string{approved})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := ParseHash(approved)
	if err != nil {
		t.Fatal(err)
	}

	r.Seed([]string{"CGONE"})
	if !r.Remove("CGONE") {
		t.Fatal("removing a tracked escrow should report true")
	}
	if r.IsEscrow("CGONE") {
		t.Fatal("removed escrow must not be tracked")
	}

	// Neither discovery nor seed may undo an explicit removal.
	if r.Register("CGONE", hash) {
		t.Fatal("discovery must not resurrect a tombstoned escrow")
	}
	r.Seed([]string{"CGONE"})
	if r.IsEscrow("CGONE") {
		t.Fatal("seed must not resurrect a tombstoned escrow")
	}

	// Only explicit Track outranks the tombstone.
	if !r.Track("CGONE") {
		t.Fatal("track should re-add the escrow")
	}
	if !r.IsEscrow("CGONE") || len(r.RemovedSnapshot()) != 0 {
		t.Fatal("track must clear the tombstone")
	}
}

func TestSeedRemoved_RestoresTombstonesAtBoot(t *testing.T) {
	r, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// A stale escrow list entry must not outlive its own tombstone,
	// regardless of restore order.
	r.SeedRemoved([]string{"CGONE"})
	r.Seed([]string{"CGONE", "CKEPT"})
	if r.IsEscrow("CGONE") {
		t.Fatal("restored tombstone must win over the escrow list")
	}
	if !r.IsEscrow("CKEPT") || r.Size() != 1 {
		t.Fatalf("expected only CKEPT tracked, got size %d", r.Size())
	}
	if got := r.RemovedSnapshot(); len(got) != 1 || got[0] != "CGONE" {
		t.Fatalf("RemovedSnapshot = %v, want [CGONE]", got)
	}
}
