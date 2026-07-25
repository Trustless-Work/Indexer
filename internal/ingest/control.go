package ingest

import (
	"context"
	"time"

	"github.com/Trustless-Work/Indexer/internal/commands"
	"github.com/Trustless-Work/Indexer/internal/events"
	"github.com/Trustless-Work/Indexer/internal/health"
	"github.com/Trustless-Work/Indexer/internal/indexer/processors"
	"github.com/Trustless-Work/Indexer/internal/indexer/registry"
	"github.com/Trustless-Work/Indexer/internal/sink"
	"github.com/stellar/go-stellar-sdk/support/log"
)

const (
	// commandQueueSize buffers commands between their arrival (AMQP
	// consumer, admin HTTP) and the loop's between-ledgers drain. Sized
	// for bursts, not backlog: at one drain per ledger (~5s) a full
	// buffer clears in one pass.
	commandQueueSize = 64
	// pauseWarnEvery paces the "still paused" warnings. A paused indexer
	// must stay noisy — a forgotten pause quietly recreating the outage
	// is the exact failure mode the TTL and this warning exist for.
	pauseWarnEvery = 30 * time.Second
)

// stateFetcher is the slice of the state detector the command executor
// needs; *processors.EscrowStateDetector satisfies it, tests fake it.
type stateFetcher interface {
	FetchStates(ctx context.Context, ids []string, ledgerSeq uint32, closedAt time.Time) ([]processors.EscrowStateChange, error)
}

// sweepResetter is the slice of the sweeper the reconcile command needs.
type sweepResetter interface {
	Reset()
}

// commandExecutor applies validated commands to the pipeline's mutable
// pieces. It runs ONLY on the ingest goroutine (the loop drains and
// executes between ledgers), preserving the single-writer invariant on
// the registry and the state file; the AMQP consumer and the admin
// handlers never touch either — they just enqueue.
type commandExecutor struct {
	reg      *registry.Registry
	detector stateFetcher
	outSink  sink.Sink
	sweeper  sweepResetter
	tracker  *health.Tracker
	network  string
	now      func() time.Time

	pausedUntil time.Time
}

// execute applies one command. It returns whether the registry mutated
// (the caller persists state) and an error ONLY for the fatal publish
// classes — a failed state FETCH is logged and skipped, the same
// skip-not-crash policy as the reconciliation sweep, because the sweep
// rotation will cover the escrow again anyway.
func (e *commandExecutor) execute(ctx context.Context, cmd commands.Command, lastSeq uint32, lastClosedAt time.Time) (bool, error) {
	started := e.now()
	mutated := false
	outcome := "ok"
	var err error

	switch cmd.Kind {
	case commands.KindTrackEscrow:
		if e.reg.Track(cmd.ContractID) {
			mutated = true
		} else {
			outcome = "already tracked"
		}
		err = e.fetchAndPublish(ctx, []string{cmd.ContractID}, lastSeq, lastClosedAt)

	case commands.KindRefreshEscrow:
		if !e.reg.IsEscrow(cmd.ContractID) {
			outcome = "not tracked — use track_escrow"
		} else {
			err = e.fetchAndPublish(ctx, []string{cmd.ContractID}, lastSeq, lastClosedAt)
		}

	case commands.KindReseed:
		before := e.reg.Size()
		e.reg.Seed(cmd.ContractIDs)
		added := e.reg.Size() - before
		mutated = added > 0
		if added < len(cmd.ContractIDs) {
			outcome = "partial: tombstoned or duplicate ids skipped"
		}
		// Fetch only what is actually tracked now — Seed skipped
		// tombstones on purpose and publishing them would undo a removal
		// downstream.
		tracked := make([]string, 0, len(cmd.ContractIDs))
		for _, id := range cmd.ContractIDs {
			if e.reg.IsEscrow(id) {
				tracked = append(tracked, id)
			}
		}
		err = e.fetchAndPublish(ctx, tracked, lastSeq, lastClosedAt)

	case commands.KindRemoveEscrow:
		if !e.reg.Remove(cmd.ContractID) {
			outcome = "was not tracked (tombstone recorded)"
		}
		mutated = true

	case commands.KindPause:
		e.pausedUntil = e.now().Add(cmd.PauseTTL())
		e.tracker.SetPaused(e.pausedUntil)
		outcome = "paused until " + e.pausedUntil.UTC().Format(time.RFC3339)

	case commands.KindResume:
		if e.pausedUntil.IsZero() || !e.now().Before(e.pausedUntil) {
			outcome = "was not paused"
		}
		e.pausedUntil = time.Time{}
		e.tracker.SetPaused(time.Time{})

	case commands.KindReconcile:
		e.sweeper.Reset()
		outcome = "sweep restarted from the top, changed-since filter disarmed"
	}

	if err != nil {
		outcome = "FAILED: " + err.Error()
	}
	// The audit line: every executed command leaves exactly one record
	// with source, arguments, outcome and duration.
	log.Ctx(ctx).Infof("Command executed: %s (source=%s requested_by=%q) -> %s [%s]",
		cmd, cmd.Source, cmd.RequestedBy, outcome, e.now().Sub(started).Round(time.Millisecond))
	return mutated, err
}

// fetchAndPublish reads the current chain state of ids and publishes it
// stamped with the last processed ledger. Fetch failures are skippable
// (warn + nil); publish failures are the caller's fatal classes.
func (e *commandExecutor) fetchAndPublish(ctx context.Context, ids []string, lastSeq uint32, lastClosedAt time.Time) error {
	if len(ids) == 0 || lastSeq == 0 {
		return nil
	}
	states, err := e.detector.FetchStates(ctx, ids, lastSeq, lastClosedAt)
	if err != nil {
		log.Ctx(ctx).Warnf("Command state fetch skipped for %d escrows: %v — the sweep will reconcile them", len(ids), err)
		return nil
	}
	for _, sc := range states {
		if perr := e.outSink.Publish(ctx, events.FromStateChange(e.network, sc)); perr != nil {
			return perr
		}
	}
	return nil
}

// paused reports whether an operator pause is in effect.
func (e *commandExecutor) paused() bool {
	return !e.pausedUntil.IsZero() && e.now().Before(e.pausedUntil)
}

// drainCommands executes every queued command without blocking. Returns
// whether any command mutated the registry (the caller persists state).
func drainCommands(ctx context.Context, ch <-chan commands.Command, exec *commandExecutor, lastSeq uint32, lastClosedAt time.Time) (bool, error) {
	mutated := false
	for {
		select {
		case cmd := <-ch:
			m, err := exec.execute(ctx, cmd, lastSeq, lastClosedAt)
			mutated = mutated || m
			if err != nil {
				return mutated, err
			}
		default:
			return mutated, nil
		}
	}
}

// holdWhilePaused blocks ledger processing while a pause is in effect,
// still draining commands so resume (and everything else) keeps working.
// It returns when the pause is lifted, its TTL expires (auto-resume,
// loudly), or ctx ends. onMutation persists registry changes made while
// paused.
func holdWhilePaused(ctx context.Context, ch <-chan commands.Command, exec *commandExecutor, lastSeq uint32, lastClosedAt time.Time, onMutation func() error) error {
	if !exec.paused() {
		return nil
	}
	log.Ctx(ctx).Warnf("Ledger processing PAUSED until %s — /readyz reports 503 and the heartbeat is silent while this holds", exec.pausedUntil.UTC().Format(time.RFC3339))
	lastWarn := exec.now()

	for exec.paused() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cmd := <-ch:
			m, err := exec.execute(ctx, cmd, lastSeq, lastClosedAt)
			if err != nil {
				return err
			}
			if m {
				if err := onMutation(); err != nil {
					return err
				}
			}
		case <-time.After(time.Second):
		}
		if exec.now().Sub(lastWarn) >= pauseWarnEvery {
			log.Ctx(ctx).Warnf("Still paused (until %s) — resume with POST /admin/resume", exec.pausedUntil.UTC().Format(time.RFC3339))
			lastWarn = exec.now()
		}
	}

	if !exec.pausedUntil.IsZero() {
		log.Ctx(ctx).Warnf("Pause TTL expired — auto-resuming ledger processing (a longer stop belongs in the deploy, not in a runtime command)")
		exec.pausedUntil = time.Time{}
		exec.tracker.SetPaused(time.Time{})
	}
	return nil
}
