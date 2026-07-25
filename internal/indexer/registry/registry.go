// Package registry is the in-memory authority for escrow identity: it
// answers "is this contract one of ours?".
//
// A contract is recognised as a TW escrow when its WASM code hash is in
// the approved set — one hash per published contract version. Shipping a
// new contract version is therefore a config change (one more approved
// hash), never a code change.
//
// The set of known escrow addresses is a *derived index*: populated by
// on-chain discovery (a contract instance created with an approved hash)
// and/or seeded from a trusted source (the API that deployed them). It is
// rebuildable from the chain, so it is not treated as precious durable
// state.
package registry

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Registry tracks approved escrow code hashes and the set of known escrow
// contract addresses. Safe for concurrent use: the approved set is
// immutable after New (lock-free reads); the escrow set is guarded by a
// RWMutex (rare writes, hot reads). By convention only the ingest loop
// WRITES (discovery, seed, executed commands); HTTP handlers may read
// concurrently.
//
// Removal is a TOMBSTONE, not a delete: a removed escrow is remembered so
// that discovery and seed files cannot silently resurrect it on the next
// catch-up — only an explicit Track (operator intent) clears the
// tombstone. Tombstones persist to the state file alongside the escrow
// set.
type Registry struct {
	approved map[xdr.Hash]struct{}

	mu      sync.RWMutex
	escrows map[string]struct{} // contractID (C... strkey) -> known escrow
	removed map[string]struct{} // tombstones: explicitly removed, never auto re-added
}

// New builds a Registry from approved WASM hashes (each a 32-byte hex
// string). Returns an error if any entry is malformed. An empty list is
// valid: no contract is recognised by hash until one is added (discovery
// then registers nothing; only Seed can populate escrows).
func New(approvedWasmHashes []string) (*Registry, error) {
	approved := make(map[xdr.Hash]struct{}, len(approvedWasmHashes))
	for _, h := range approvedWasmHashes {
		if strings.TrimSpace(h) == "" {
			continue
		}
		hash, err := ParseHash(h)
		if err != nil {
			return nil, fmt.Errorf("approved wasm hash %q: %w", h, err)
		}
		approved[hash] = struct{}{}
	}
	return &Registry{
		approved: approved,
		escrows:  make(map[string]struct{}),
		removed:  make(map[string]struct{}),
	}, nil
}

// IsApprovedHash reports whether h is an approved escrow code hash. The
// approved set is immutable after New, so this needs no lock.
func (r *Registry) IsApprovedHash(h xdr.Hash) bool {
	_, ok := r.approved[h]
	return ok
}

// Register records contractID as a known escrow IF its code hash is
// approved. Returns true if it was newly added. A non-approved hash is
// silently ignored (returns false), which is what lets the discovery
// pass call Register on every created contract instance unconditionally.
// A tombstoned contract is NOT re-added: the whole point of tombstones
// is that a catch-up replaying the creation ledger cannot resurrect an
// escrow an operator removed.
func (r *Registry) Register(contractID string, wasmHash xdr.Hash) bool {
	if contractID == "" || !r.IsApprovedHash(wasmHash) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, gone := r.removed[contractID]; gone {
		return false
	}
	if _, ok := r.escrows[contractID]; ok {
		return false
	}
	r.escrows[contractID] = struct{}{}
	return true
}

// Seed registers escrow addresses from a trusted source (e.g. the API
// that deployed them), bypassing the hash check. Used for first-boot
// bootstrap of escrows created before the indexed range. Tombstoned
// contracts are skipped — a stale seed file must not undo an explicit
// removal; use Track for that.
func (r *Registry) Seed(contractIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range contractIDs {
		if id == "" {
			continue
		}
		if _, gone := r.removed[id]; gone {
			continue
		}
		r.escrows[id] = struct{}{}
	}
}

// Track adds contractID by explicit operator/API intent: unlike Seed it
// clears any tombstone first — an explicit request to watch an escrow
// outranks a past removal. Returns true if the contract was not being
// tracked before.
func (r *Registry) Track(contractID string) bool {
	if contractID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.removed, contractID)
	if _, ok := r.escrows[contractID]; ok {
		return false
	}
	r.escrows[contractID] = struct{}{}
	return true
}

// Remove tombstones contractID: it stops being tracked AND is remembered
// as removed so discovery/seed cannot re-add it. Returns true if it was
// being tracked. Removing an unknown id still records the tombstone —
// the operator's intent is "never track this", not "only if present".
func (r *Registry) Remove(contractID string) bool {
	if contractID == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, existed := r.escrows[contractID]
	delete(r.escrows, contractID)
	r.removed[contractID] = struct{}{}
	return existed
}

// SeedRemoved restores the tombstone set from persisted state at boot.
func (r *Registry) SeedRemoved(contractIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range contractIDs {
		if id != "" {
			r.removed[id] = struct{}{}
			delete(r.escrows, id)
		}
	}
}

// IsEscrow reports whether contractID is a known TW escrow. O(1).
func (r *Registry) IsEscrow(contractID string) bool {
	if contractID == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.escrows[contractID]
	return ok
}

// Size returns the number of known escrows.
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.escrows)
}

// Snapshot returns the known escrow addresses, sorted for stable diffs
// across saves. Used to persist the registry to state.
func (r *Registry) Snapshot() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.escrows))
	for id := range r.escrows {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// RemovedSnapshot returns the tombstoned addresses, sorted. Persisted
// alongside Snapshot so removals survive restarts.
func (r *Registry) RemovedSnapshot() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.removed))
	for id := range r.removed {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ParseHash decodes a 32-byte hex string into an xdr.Hash.
func ParseHash(hexStr string) (xdr.Hash, error) {
	var h xdr.Hash
	b, err := hex.DecodeString(strings.TrimSpace(hexStr))
	if err != nil {
		return h, err
	}
	if len(b) != 32 {
		return h, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(h[:], b)
	return h, nil
}
