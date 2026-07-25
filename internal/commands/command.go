// Package commands is the Indexer's control plane: the wire contract for
// operator/API commands, the AMQP consumer that receives them, and the
// authenticated admin HTTP surface.
//
// Every entry point — AMQP consumer, admin HTTP — does exactly one
// thing with a command: validate it and ENQUEUE it into a channel. The
// ingest loop drains that channel between ledgers and executes; it stays
// the single writer of the registry and the state file (the same
// invariant the discovery pass and the detector rely on). No command is
// ever applied from a second goroutine.
package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Kind names one command. Part of the wire contract with the core API
// and operators — treat values as frozen.
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
	// discovery/seed cannot silently re-add it. Admin-only.
	KindRemoveEscrow Kind = "remove_escrow"
	// KindPause halts ledger processing for TTL (bounded); the loop keeps
	// draining commands so resume works, /readyz reports 503 and the
	// heartbeat goes silent — a paused indexer is DELIBERATELY noisy, so
	// a forgotten pause cannot quietly recreate the outage. Admin-only.
	KindPause Kind = "pause"
	// KindResume lifts a pause before its TTL expires. Admin-only.
	KindResume Kind = "resume"
	// KindReconcile restarts the reconciliation sweep from the top with
	// the changed-since filter disarmed: the current state of every
	// tracked escrow is re-published over the following ledgers, budget
	// intact. Admin-only.
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

// ErrInvalidCommand marks a command that failed validation. AMQP
// consumers drop such messages (nack without requeue) — a malformed
// command must never crash-loop or clog the queue.
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
	// RequestedBy is free-form provenance for the audit log ("core-api",
	// an operator name). Never trusted for authorization.
	RequestedBy string `json:"requested_by,omitempty"`

	// Source records which entry point accepted the command ("amqp",
	// "admin"). Set by the receiving side, not the sender.
	Source string `json:"-"`
}

// Parse decodes and validates one wire command (the AMQP message body
// and the admin HTTP request body share this format).
func Parse(body []byte) (Command, error) {
	var cmd Command
	if err := json.Unmarshal(body, &cmd); err != nil {
		return Command{}, fmt.Errorf("%w: malformed JSON: %v", ErrInvalidCommand, err)
	}
	if err := cmd.Validate(); err != nil {
		return Command{}, err
	}
	return cmd, nil
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
