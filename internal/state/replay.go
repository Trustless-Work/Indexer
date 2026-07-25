package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// NullStore is the Store for replay runs: nothing is ever persisted and
// nothing is ever resumed. A replay is a read-side tool — it must not
// move the live indexer's cursor, record gaps, or fight for its flock.
type NullStore struct{}

// Load reports no persisted state, so the caller starts exactly where
// its explicit --from says.
func (NullStore) Load(context.Context) (State, error) { return State{}, ErrStateNotFound }

// Save discards st.
func (NullStore) Save(context.Context, State) error { return nil }

// Close is a no-op.
func (NullStore) Close() error { return nil }

var _ Store = NullStore{}

// ReadOnlyWatchlist reads the escrow watchlist (tracked + tombstoned)
// from the state files at path WITHOUT taking the single-writer lock —
// safe next to a running indexer because both files are replaced
// atomically, and a replay only needs a point-in-time copy to attribute
// deposits for escrows created before the replayed range. Returns empty
// sets (not an error) when no state exists.
func ReadOnlyWatchlist(path string) (escrows, removed []string, err error) {
	wlData, err := os.ReadFile(watchlistPath(path))
	switch {
	case err == nil:
		var wl watchlistRecord
		if jerr := json.Unmarshal(wlData, &wl); jerr != nil {
			return nil, nil, fmt.Errorf("%w: watchlist: %v", ErrStateCorrupted, jerr)
		}
		return wl.EscrowContracts, wl.RemovedEscrows, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, nil, fmt.Errorf("reading watchlist file: %w", err)
	}

	// No split watchlist yet: fall back to a legacy combined file.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading state file: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrStateCorrupted, err)
	}
	return st.EscrowContracts, st.RemovedEscrows, nil
}
