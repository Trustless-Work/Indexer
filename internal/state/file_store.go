package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// FileStore persists State as JSON, written atomically via the canonical
// temp + fsync + rename + parent-dir fsync sequence so an observer always
// sees the old or new file, never a half-written one — even across a
// power loss. A non-blocking advisory flock on a sidecar lock file
// enforces a single writer.
//
// The record is SPLIT across two files by write temperature:
//
//   - the cursor file (the configured STATE_PATH): version, network,
//     last ledger seq + hash. ~150 bytes, rewritten on every checkpoint
//     — every ledger at the tip.
//   - the watchlist file (same path, .watchlist.json suffix): escrow
//     set, tombstones and gap evidence. Rewritten ONLY when its content
//     actually changed.
//
// Before the split, every tip checkpoint rewrote and fsynced the whole
// combined record; at 2.5k escrows that is ~150KB per ledger (~30KB/s
// of sustained volume writes) to move a 4-byte cursor. Both files are
// marshaled compactly for the same reason.
//
// Write order on a combined save is watchlist FIRST, then cursor: a
// crash between the two leaves a newer watchlist with an older cursor,
// which at-least-once delivery absorbs. The reverse order could advance
// the cursor past a just-tracked escrow and forget it — a lost command
// that was already acked.
//
// Migration: a pre-split file at STATE_PATH still carries the escrow
// fields. Load uses them whenever no watchlist file exists yet; the
// next Save writes the new layout (and the cursor file drops the
// legacy fields naturally).
type FileStore struct {
	path     string
	lockFile *os.File
	// lastWatchlist caches the bytes last written (or loaded) for the
	// watchlist file, so an unchanged watchlist costs zero writes.
	lastWatchlist []byte
}

// cursorRecord is the hot half of the on-disk layout.
type cursorRecord struct {
	Version        int    `json:"version"`
	Network        string `json:"network"`
	LastLedgerSeq  uint32 `json:"last_ledger_seq"`
	LastLedgerHash string `json:"last_ledger_hash,omitempty"`
}

// watchlistRecord is the cold half: everything that changes rarely.
type watchlistRecord struct {
	Version         int      `json:"version"`
	Network         string   `json:"network"`
	EscrowContracts []string `json:"escrow_contracts"`
	RemovedEscrows  []string `json:"removed_escrows,omitempty"`
	Gaps            []Gap    `json:"gaps,omitempty"`
}

// watchlistPath derives the cold-file path from the configured state
// path: "x/state.json" → "x/state.watchlist.json".
func watchlistPath(path string) string {
	if strings.HasSuffix(path, ".json") {
		return strings.TrimSuffix(path, ".json") + ".watchlist.json"
	}
	return path + ".watchlist"
}

// NewFileStore opens (and locks) the state at path. It creates the parent
// directory if missing and acquires the single-writer lock; if another
// process holds it, construction fails with ErrStateLockHeld.
func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("state path must not be empty")
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating state directory %q: %w", dir, err)
		}
	}

	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening state lock %q: %w", lockPath, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrStateLockHeld, lockPath)
		}
		return nil, fmt.Errorf("acquiring state lock %q: %w", lockPath, err)
	}

	return &FileStore{path: path, lockFile: lock}, nil
}

// Load reads and merges the on-disk record. Returns ErrStateNotFound when
// no cursor file exists and ErrStateCorrupted when either file cannot be
// parsed.
func (s *FileStore) Load(_ context.Context) (State, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ErrStateNotFound
		}
		return State{}, fmt.Errorf("reading state file %q: %w", s.path, err)
	}

	// The cursor file parses as the full State: post-split files simply
	// leave the escrow fields empty; pre-split files carry them and act
	// as the migration source below.
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrStateCorrupted, err)
	}

	wlPath := watchlistPath(s.path)
	wlData, err := os.ReadFile(wlPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No watchlist file yet: legacy combined layout (or a fresh
		// deployment that never tracked anything). The fields already in
		// st are authoritative; the next Save migrates.
		return st, nil
	case err != nil:
		return State{}, fmt.Errorf("reading watchlist file %q: %w", wlPath, err)
	}

	var wl watchlistRecord
	if err := json.Unmarshal(wlData, &wl); err != nil {
		return State{}, fmt.Errorf("%w: watchlist: %v", ErrStateCorrupted, err)
	}
	st.EscrowContracts = wl.EscrowContracts
	st.RemovedEscrows = wl.RemovedEscrows
	st.Gaps = wl.Gaps
	s.lastWatchlist = wlData
	return st, nil
}

// Save persists st: the watchlist file only when its content changed,
// the cursor file always.
func (s *FileStore) Save(_ context.Context, st State) error {
	wl := watchlistRecord{
		Version:         CurrentVersion,
		Network:         st.Network,
		EscrowContracts: st.EscrowContracts,
		RemovedEscrows:  st.RemovedEscrows,
		Gaps:            st.Gaps,
	}
	wlData, err := json.Marshal(&wl)
	if err != nil {
		return fmt.Errorf("marshaling watchlist: %w", err)
	}
	if !bytes.Equal(wlData, s.lastWatchlist) {
		if err := atomicWrite(watchlistPath(s.path), wlData); err != nil {
			return fmt.Errorf("writing watchlist file: %w", err)
		}
		s.lastWatchlist = wlData
	}

	cur := cursorRecord{
		Version:        CurrentVersion,
		Network:        st.Network,
		LastLedgerSeq:  st.LastLedgerSeq,
		LastLedgerHash: st.LastLedgerHash,
	}
	curData, err := json.Marshal(&cur)
	if err != nil {
		return fmt.Errorf("marshaling cursor: %w", err)
	}
	if err := atomicWrite(s.path, curData); err != nil {
		return fmt.Errorf("writing cursor file: %w", err)
	}
	return nil
}

// Close releases the advisory lock. Idempotent.
func (s *FileStore) Close() error {
	if s.lockFile == nil {
		return nil
	}
	_ = syscall.Flock(int(s.lockFile.Fd()), syscall.LOCK_UN)
	err := s.lockFile.Close()
	s.lockFile = nil
	return err
}

// atomicWrite lands data at path via temp + fsync + rename + dir fsync.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing temp file %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	// fsync the parent directory so the rename survives an OS crash.
	if dir, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// writeFileSync writes data to path, fsyncs and closes it (no rename).
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
