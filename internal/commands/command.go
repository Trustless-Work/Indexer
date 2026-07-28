// Package commands is the Indexer's control plane: the command contract
// and the authenticated admin HTTP surface that accepts them.
//
// Commands are OPERATOR actions. There is exactly one way in — the
// admin surface, behind ADMIN_TOKEN — and it does exactly one thing with
// a command: validate it and ENQUEUE it into a channel. The ingest loop
// drains that channel between ledgers and executes; it stays the single
// writer of the registry and the state file (the same invariant the
// discovery pass and the detector rely on). No command is ever applied
// from a second goroutine.
//
// An AMQP consumer used to be a second way in, so the core API could
// announce freshly deployed escrows. It was removed in the A1 audit fix:
// on-chain discovery already registers a deployed escrow and publishes
// its state within the SAME ledger (the deploy emits tw_init from the
// escrow itself, and the discovery pass runs before detection precisely
// so that works), so the announcement bought no measurable latency —
// while handing anyone who could publish to the broker an unauthenticated
// control channel into the pipeline. If a future need for automated
// reconciliation appears, see the note in docs/control-plane.md: the
// direction to reach for is the indexer PULLING from a source it
// authenticates, not the broker pushing commands at it.
package commands

import (
	"errors"
	"fmt"
	"time"
)

// Kind names one command. Part of the contract with operators and with
// any tooling built on the admin surface — treat values as frozen.
type Kind string

const (
	// KindTrackEscrow adds one escrow to the watchlist (clearing any
	// tombstone: explicit intent outranks a past removal), then fetches
	// and publishes its current state immediately. Idempotent.
	KindTrackEscrow Kind = "track_escrow"
	// KindRefreshEscrow re-fetches and re-publishes the current state of
	// an ALREADY tracked escrow — the "this escrow looks stale/missing
	// downstream" support answer, in seconds instead of a redeploy.
	KindRefreshEscrow Kind = "refresh_escrow"
	// KindReseed bulk-adds escrows (post state-loss recovery), then
	// fetches and publishes the current state of each. Idempotent.
	KindReseed Kind = "reseed"
	// KindRemoveEscrow tombstones one escrow: it stops being tracked and
	// discovery/seed cannot silently re-add it.
	KindRemoveEscrow Kind = "remove_escrow"
	// KindPause halts ledger processing for TTL (bounded); the loop keeps
	// draining commands so resume works, /readyz reports 503 and the
	// heartbeat goes silent — a paused indexer is DELIBERATELY noisy, so
	// a forgotten pause cannot quietly recreate the outage.
	KindPause Kind = "pause"
	// KindResume lifts a pause before its TTL expires.
	KindResume Kind = "resume"
	// KindReconcile restarts the reconciliation sweep from the top with
	// the changed-since filter disarmed: the current state of every
	// tracked escrow is re-published over the following ledgers, budget
	// intact.
	KindReconcile Kind = "reconcile"
)

// MaxReseedBatch bounds one reseed command. A getLedgerEntries request
// carries at most 200 keys, so 5000 ids cost ~25 chunked requests
// executed between two ledgers — tolerable for a rare recovery
// operation, while an unbounded list would stall tip-following.
const MaxReseedBatch = 5000

// MaxPauseTTL caps how long a single pause command can halt the
// pipeline. A longer stop is a deploy-time decision (scale to zero), not
// a runtime command that survives in nobody's terminal history.
const MaxPauseTTL = time.Hour

// DefaultPauseTTL applies when a pause arrives without a TTL.
const DefaultPauseTTL = 10 * time.Minute

// ErrInvalidCommand marks a command that failed validation. The admin
// surface answers 400 and never enqueues — a malformed command must
// never reach the ingest loop.
var ErrInvalidCommand = errors.New("invalid command")

// Command is one validated control-plane instruction, ready for the
// ingest loop to execute.
type Command struct {
	Kind Kind `json:"command"`
	// ContractID is the target escrow for track/refresh/remove.
	ContractID string `json:"contract_id,omitempty"`
	// ContractIDs is the bulk target list for reseed.
	ContractIDs []string `json:"contract_ids,omitempty"`
	// TTLSeconds bounds a pause. 0 means DefaultPauseTTL.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
	// RequestedBy is free-form provenance for the audit log (an operator
	// name, a runbook reference). Never trusted for authorization: the
	// bearer token is what authenticates, this is only a label.
	RequestedBy string `json:"requested_by,omitempty"`
}

// Validate enforces per-kind argument requirements.
func (c Command) Validate() error {
	switch c.Kind {
	case KindTrackEscrow, KindRefreshEscrow, KindRemoveEscrow:
		if c.ContractID == "" {
			return fmt.Errorf("%w: %s requires contract_id", ErrInvalidCommand, c.Kind)
		}
	case KindReseed:
		if len(c.ContractIDs) == 0 {
			return fmt.Errorf("%w: reseed requires contract_ids", ErrInvalidCommand)
		}
		if len(c.ContractIDs) > MaxReseedBatch {
			return fmt.Errorf("%w: reseed carries %d ids, max is %d — split the batch", ErrInvalidCommand, len(c.ContractIDs), MaxReseedBatch)
		}
	case KindPause:
		if c.TTLSeconds < 0 {
			return fmt.Errorf("%w: pause ttl_seconds must be >= 0", ErrInvalidCommand)
		}
		if ttl := time.Duration(c.TTLSeconds) * time.Second; ttl > MaxPauseTTL {
			return fmt.Errorf("%w: pause ttl %s exceeds the maximum %s", ErrInvalidCommand, ttl, MaxPauseTTL)
		}
	case KindResume, KindReconcile:
		// No arguments.
	default:
		return fmt.Errorf("%w: unknown command %q", ErrInvalidCommand, string(c.Kind))
	}
	return nil
}

// PauseTTL returns the effective pause duration: the requested TTL,
// defaulted and capped.
func (c Command) PauseTTL() time.Duration {
	ttl := time.Duration(c.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = DefaultPauseTTL
	}
	if ttl > MaxPauseTTL {
		ttl = MaxPauseTTL
	}
	return ttl
}

// String renders the command for the audit log: kind plus the arguments
// that matter, never more.
func (c Command) String() string {
	switch c.Kind {
	case KindReseed:
		return fmt.Sprintf("%s(%d ids)", c.Kind, len(c.ContractIDs))
	case KindPause:
		return fmt.Sprintf("%s(ttl=%s)", c.Kind, c.PauseTTL())
	case KindTrackEscrow, KindRefreshEscrow, KindRemoveEscrow:
		return fmt.Sprintf("%s(%s)", c.Kind, c.ContractID)
	default:
		return string(c.Kind)
	}
}
