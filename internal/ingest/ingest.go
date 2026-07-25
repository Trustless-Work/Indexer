// Package ingest is the composition root and live loop for the Indexer.
//
// State after the 2026-05-21 cleanup: the filter-and-forward pipeline
// (detector / events / publisher / envelope-sink / state) was removed,
// and the processor-based core in internal/indexer is once again the
// single source of truth. This loop fetches ledgers from the configured
// RPC backend, runs each one through the processor pipeline, and logs a
// per-ledger summary of the populated buffer.
//
// Delivery to a sink is intentionally NOT wired yet. This is a clean
// starting point to refine the processor core from: the buffer is built
// every ledger and summarized; routing it to RabbitMQ is the next step.
//
// The caller (cmd/ingest.go) owns ctx, signal handling and config
// loading. Ingest itself never reads env vars.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/Trustless-Work/Indexer/internal/commands"
	"github.com/Trustless-Work/Indexer/internal/config"
	"github.com/Trustless-Work/Indexer/internal/events"
	"github.com/Trustless-Work/Indexer/internal/health"
	"github.com/Trustless-Work/Indexer/internal/indexer"
	"github.com/Trustless-Work/Indexer/internal/indexer/processors"
	"github.com/Trustless-Work/Indexer/internal/indexer/registry"
	"github.com/Trustless-Work/Indexer/internal/sink"
	sinkfactory "github.com/Trustless-Work/Indexer/internal/sink/factory"
	"github.com/Trustless-Work/Indexer/internal/state"
	"github.com/Trustless-Work/Indexer/internal/utils"
	sdkingest "github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const (
	// sweepMaxLedgerAge gates the reconciliation sweep to the tip: a
	// ledger older than this means we are catching up, and every key of
	// RPC budget belongs to closing the lag, not to reconciling state
	// that the catch-up itself will refresh.
	sweepMaxLedgerAge = 30 * time.Second
	// maxLedgerFetchRetries caps how many transient failures we accept
	// when fetching a single ledger before giving up.
	maxLedgerFetchRetries = 10
	// initialRetryBackoff is the wait before the first retry. It doubles
	// on every failure up to maxRetryBackoff.
	initialRetryBackoff = time.Second
	// maxRetryBackoff caps the per-attempt wait so shutdown stays snappy.
	maxRetryBackoff = 30 * time.Second
	// tipWaitInterval is the pause between polls when the RPC rejects a
	// request for a ledger it has not closed yet (beyond-tip). Kept short:
	// freshness at the tip is the product requirement.
	tipWaitInterval = time.Second
	// tipWaitWarnEvery is how many consecutive beyond-tip waits pass
	// between warnings. At 1s per wait, 30 waits ≈ 30s of a stalled tip —
	// worth surfacing, since downstream freshness depends on it.
	tipWaitWarnEvery = 30
	// tipStallAfter is the floor of the stall budget: how long a fetch may
	// go without producing a ledger before the endpoint is declared
	// stalled and rotated away from. The chain closes a ledger every
	// ~5-6s, so 60s of no progress is a frozen endpoint beyond doubt.
	// The watchdog needs this deadline because the SDK's GetLedger BLOCKS
	// internally at the tip (polling getHealth every 2s, forever): a
	// frozen endpoint keeps answering getHealth with the same stale tip
	// and no error ever reaches the loop.
	tipStallAfter = 60 * time.Second
	// tipStallSlack pads the ledger-fetch HTTP timeout when it exceeds
	// tipStallAfter, so a catch-up batch that legitimately uses its full
	// HTTP budget errors out as itself instead of as a fake stall.
	tipStallSlack = 10 * time.Second
)

// errTipStalled reports that an endpoint spent the whole stall budget
// without producing the requested ledger while claiming to be healthy.
// The loop rotates to the next endpoint; with the whole pool stalled the
// rotation cycles harmlessly until the chain (or a provider) moves again.
var errTipStalled = errors.New("RPC tip stalled")

// Ingest is the entry point of the Indexer pipeline. It blocks until ctx
// cancellation or a terminal error.
//
// Semantics:
//   - INDEXER_END_LEDGER == 0: unbounded (live mode); the loop runs until
//     ctx is cancelled or a terminal error fires.
//   - INDEXER_END_LEDGER != 0: bounded (backfill); the loop exits after
//     processing end inclusive.
func Ingest(ctx context.Context, cfg *config.Config) error {
	log.Ctx(ctx).Info(cfg.String())

	// State store: persists the cursor (and, later, the escrow set). The
	// flock makes a second instance fail fast instead of double-publishing.
	store, err := state.NewFileStore(cfg.State.Path)
	if err != nil {
		return fmt.Errorf("opening state store: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			log.Ctx(ctx).Warnf("closing state store: %v", cerr)
		}
	}()

	startLedger := cfg.Indexer.StartLedger
	endLedger := cfg.Indexer.EndLedger

	// Resume from the persisted cursor + escrow set if we have one. A
	// network mismatch is fatal — point at the right state file.
	// Gaps ride along unchanged: they are append-only evidence and every
	// Save below must carry them forward or the record is lost.
	var persistedEscrows []string
	var persistedRemoved []string
	var gaps []state.Gap
	var persistedLastSeq uint32
	persistedHash := ""
	resumed := false
	switch loaded, lerr := store.Load(ctx); {
	case errors.Is(lerr, state.ErrStateNotFound):
		log.Ctx(ctx).Info("No state file — starting fresh")
	case lerr != nil:
		return fmt.Errorf("loading state: %w", lerr)
	default:
		if loaded.Network != "" && loaded.Network != cfg.Network.Passphrase {
			return fmt.Errorf("state network mismatch: state=%q, configured=%q", loaded.Network, cfg.Network.Passphrase)
		}
		startLedger = loaded.LastLedgerSeq + 1
		persistedEscrows = loaded.EscrowContracts
		persistedRemoved = loaded.RemovedEscrows
		gaps = loaded.Gaps
		persistedLastSeq = loaded.LastLedgerSeq
		persistedHash = loaded.LastLedgerHash
		resumed = true
		log.Ctx(ctx).Infof("Resuming from persisted cursor: next ledger %d", startLedger)
	}

	// Escrow registry: identifies "our" contracts by approved WASM hash.
	// Populated by the discovery pass (and, later, an API seed). Built
	// before the start-ledger clamp so a clamp can persist the full
	// state (cursor + escrows + gap) in one atomic write.
	reg, err := registry.New(cfg.Escrow.ApprovedWasmHashes)
	if err != nil {
		return fmt.Errorf("building escrow registry: %w", err)
	}

	// Repopulate the registry: from persisted state on resume, otherwise
	// from the optional seed file (escrows created before the indexed
	// range). Discovery keeps adding new escrows from here on. Tombstones
	// restore FIRST so a stale entry in the persisted escrow list cannot
	// outlive its own removal.
	if resumed {
		reg.SeedRemoved(persistedRemoved)
		reg.Seed(persistedEscrows)
		log.Ctx(ctx).Infof("Restored %d escrows from state (%d tombstoned)", reg.Size(), len(persistedRemoved))
	} else {
		seedIDs, serr := state.LoadSeed(cfg.Escrow.SeedPath)
		if serr != nil {
			return fmt.Errorf("loading escrow seed: %w", serr)
		}
		if len(seedIDs) > 0 {
			reg.Seed(seedIDs)
			log.Ctx(ctx).Infof("Seeded %d escrows from %s", reg.Size(), cfg.Escrow.SeedPath)
		}
	}

	// Sink: where detected facts are delivered (noop or rabbitmq). Built
	// before the RPC pool connects because gap evidence recorded by a
	// connect-time clamp is published right after connecting. The
	// backpressure wrapper makes a full queue WAIT (retrying in place,
	// cursor untouched) instead of killing the process; only envelope
	// bugs, broken topology and a dead connection stay fatal.
	rawSink, err := sinkfactory.New(cfg)
	if err != nil {
		return fmt.Errorf("building sink: %w", err)
	}
	defer func() {
		if cerr := rawSink.Close(); cerr != nil {
			log.Ctx(ctx).Warnf("closing sink: %v", cerr)
		}
	}()
	outSink := newBackpressureSink(rawSink)

	// RPC endpoint pool: RPC_URL plus RPC_FALLBACK_URLS, one endpoint
	// active at a time, feeding both the ledger backend and the state
	// fetches. Connecting to an endpoint verifies its network passphrase
	// and clamps the start ledger against the window that endpoint
	// actually serves — without the clamp, a cursor that fell out of
	// retention makes PrepareRange fail deterministically and the process
	// crash-loops until a human intervenes (the 2026-07-22 incident).
	// Skipping forward loses data, so the skipped range is recorded as a
	// Gap and persisted BEFORE any ledger is processed — evidence for a
	// later backfill. onGap is that persistence hook, shared by boot and
	// every later rotation.
	pool := newRPCPool(cfg)
	defer func() {
		if cerr := pool.Close(); cerr != nil {
			log.Ctx(ctx).Warnf("closing RPC pool: %v", cerr)
		}
	}()
	onGap := func(gap state.Gap, clampedStart uint32) error {
		gaps = append(gaps, gap)
		// No LastLedgerHash on purpose: a clamp breaks the hash chain.
		return store.Save(ctx, snapshotState(cfg, reg, clampedStart-1, "", gaps))
	}

	startLedger, err = pool.Connect(ctx, startLedger, endLedger, onGap)
	if err != nil {
		return fmt.Errorf("connecting to an RPC endpoint: %w", err)
	}

	// Republish every recorded gap after every successful connect
	// (at-least-once): a crash between recording a gap and publishing it
	// must not lose the signal. The message id is deterministic
	// ("gap:{net}:{from}:{to}"), so the consumer treats repeats as
	// no-ops; the list shrinks only when an operator deletes a replayed
	// gap from the state file.
	if err := publishGaps(ctx, outSink, cfg.Network.Name, gaps); err != nil {
		return err
	}

	// State detector: fetches the current DataKey::Escrow entry of each
	// escrow that had activity in a ledger via getLedgerEntries — the
	// canonical Soroban way to read contract state. It reads through the
	// pool, so state fetches follow every endpoint rotation.
	stateDetector := processors.NewEscrowStateDetector(pool, reg)

	// Reconciliation sweep: rotates over the whole watchlist so removals
	// and post-gap drift are noticed without waiting for activity.
	sweeper := processors.NewSweeper(reg)

	ledgerIndexer := indexer.NewIndexer(reg)

	// Progress tracker + health server. The tracker always exists (the
	// loop reports to it unconditionally — no nil checks in the hot
	// path); the HTTP server only binds when enabled. Its lifetime is
	// tied to ctx, same as the loop it observes.
	tracker := health.NewTracker(cfg.Network.Name)
	tracker.SetRPCEndpoint(pool.CurrentHost())

	// Command plane: AMQP consumer and admin HTTP handlers VALIDATE and
	// ENQUEUE only; the loop drains this channel between ledgers and
	// executes, staying the single writer of registry and state.
	cmdCh := make(chan commands.Command, commandQueueSize)
	executor := &commandExecutor{
		reg:      reg,
		detector: stateDetector,
		outSink:  outSink,
		sweeper:  sweeper,
		tracker:  tracker,
		network:  cfg.Network.Name,
		now:      time.Now,
	}
	if cfg.Sink.Type == "rabbitmq" {
		go commands.Consume(ctx, commands.ConsumerConfig{
			URL:      cfg.RabbitMQ.URL,
			Exchange: cfg.RabbitMQ.CommandsExchange,
			Network:  cfg.Network.Name,
		}, cmdCh)
	}

	if cfg.Health.Enabled {
		var admin http.Handler
		if cfg.AdminToken != "" {
			admin = commands.AdminHandler(cfg.AdminToken, commands.EnqueueInto(cmdCh), reg)
		}
		go health.Serve(ctx, fmt.Sprintf(":%d", cfg.Health.Port), tracker, admin)
	}

	// Outbound dead-man's switch: a throttled ping after processed
	// ledgers; the external monitor alerts on SILENCE, covering every
	// way the loop can stop. Nil (no-op) when HEALTH_HEARTBEAT_URL is
	// unset, so the call below needs no branching.
	heartbeat := health.NewHeartbeat(cfg.Health.HeartbeatURL)

	// Chain-continuity anchor: on a resume EXACTLY at cursor+1, the first
	// fetched ledger must link back (by parent hash) to the ledger this
	// state file last processed. A clamped or manually moved cursor
	// legitimately breaks the hash chain, so the check only arms when the
	// resume is contiguous and the state carries a hash. lastLedgerHash
	// tracks the same anchor in memory so an endpoint rotation can re-arm
	// it: the first ledger served by a NEW provider must descend from the
	// last one processed, or the fallback is serving a different chain.
	expectedPrevHash := ""
	if resumed && persistedHash != "" && startLedger == persistedLastSeq+1 {
		expectedPrevHash = persistedHash
	}
	lastLedgerHash := expectedPrevHash

	// Stall budget for a single ledger fetch: how long GetLedger may sit
	// without producing a ledger before the endpoint is declared stalled
	// and rotated away from. Must exceed the ledger-fetch HTTP timeout,
	// or a slow-but-legal catch-up batch would be misread as a stall.
	stallBudget := tipStallAfter
	if b := cfg.RPC.LedgerFetchTimeout + tipStallSlack; b > stallBudget {
		stallBudget = b
	}

	// pendingStates defers state fetches while catching up: fetching would
	// read the TIP state but stamp it with a historic ledger (the audit's
	// wrong-intermediate-balances defect). The set flushes in one batch on
	// the ledger where the loop reaches the tip again.
	pendingStates := make(map[string]struct{})
	lastCheckpoint := startLedger - 1

	currentLedger := startLedger
	log.Ctx(ctx).Infof("Starting ingestion loop from ledger %d (end=%d)", startLedger, endLedger)

	for endLedger == 0 || currentLedger <= endLedger {
		if err := ctx.Err(); err != nil {
			log.Ctx(ctx).Infof("Ingestion loop stopped at ledger %d: %v", currentLedger, err)
			return nil
		}

		meta, err := fetchLedgerWithRetry(ctx, pool.Backend(), currentLedger, stallBudget)
		if err != nil {
			if ctx.Err() != nil {
				log.Ctx(ctx).Infof("Ingestion loop stopped at ledger %d: %v", currentLedger, ctx.Err())
				return nil
			}
			// The endpoint exhausted its bounded retries (or stalled at
			// the tip): rotate through the pool. Retries are bounded PER
			// endpoint; the process dies only when every endpoint in the
			// pool failed, and the supervisor's restart retries the pool
			// from the top.
			log.Ctx(ctx).Warnf("RPC endpoint %s gave up on ledger %d: %v — rotating to the next endpoint", pool.CurrentHost(), currentLedger, err)
			resumeFrom, rerr := pool.Rotate(ctx, currentLedger, endLedger, onGap)
			if rerr != nil {
				return fmt.Errorf("fetching ledger %d: %v; RPC failover exhausted: %w", currentLedger, err, rerr)
			}
			tracker.SetRPCEndpoint(pool.CurrentHost())
			if resumeFrom == currentLedger {
				// Contiguous hand-off: verify the new provider's first
				// ledger descends from the last one processed, so a
				// fallback serving a different chain cannot poison the
				// read-model (same rule as a contiguous resume).
				expectedPrevHash = lastLedgerHash
			} else {
				// The new endpoint's retention forced a clamp: the hash
				// chain is legitimately broken and the skipped range is
				// already recorded as a gap by onGap.
				currentLedger = resumeFrom
				expectedPrevHash = ""
			}
			if err := publishGaps(ctx, outSink, cfg.Network.Name, gaps); err != nil {
				return err
			}
			continue
		}

		// One-shot divergence check on the first ledger of a contiguous
		// resume (see the anchor above the loop).
		if expectedPrevHash != "" {
			if err := verifyChainContinuity(expectedPrevHash, meta); err != nil {
				return err
			}
			expectedPrevHash = ""
		}

		started := time.Now()
		facts, activeEscrows, err := processLedger(ctx, ledgerIndexer, cfg.Network.Passphrase, cfg.Indexer.GetLedgersLimit, meta)
		if err != nil {
			return fmt.Errorf("processing ledger %d: %w", currentLedger, err)
		}

		// Publish each detected event/deposit. Broker backpressure never
		// surfaces here (the sink wrapper retries in place, holding the
		// cursor); an error IS one of the fatal classes — envelope bug,
		// unroutable topology, dead connection — and returning is right:
		// advancing past it would be silent data loss.
		for _, ev := range facts {
			if err := outSink.Publish(ctx, events.FromEscrowEvent(cfg.Network.Name, ev)); err != nil {
				return fmt.Errorf("publishing event %s:%d at ledger %d: %w", ev.TxHash, ev.EventIndex, currentLedger, err)
			}
		}

		// Fetch current state for each escrow with activity, then publish —
		// but NOT while catching up: getLedgerEntries reads the TIP state,
		// and stamping that with the historic ledger being replayed would
		// publish wrong intermediate balances (Sprint 5). Deferred escrows
		// flush in one correctly-stamped batch upon reaching the tip; a
		// bounded backfill never reaches it and suppresses them entirely
		// (the live indexer's sweep owns reconciliation).
		ledgerClosedAt := time.Unix(meta.LedgerCloseTime(), 0).UTC()
		catchingUp := time.Since(ledgerClosedAt) > catchUpMinAge
		var states []processors.EscrowStateChange
		if catchingUp {
			for _, id := range activeEscrows {
				pendingStates[id] = struct{}{}
			}
		} else {
			ids := activeEscrows
			if len(pendingStates) > 0 {
				ids = drainPending(pendingStates, activeEscrows)
				log.Ctx(ctx).Infof("Caught up to the tip at ledger %d: fetching deferred state for %d escrows", currentLedger, len(ids))
			}
			states, err = stateDetector.FetchStates(ctx, ids, currentLedger, ledgerClosedAt)
			if err != nil {
				return fmt.Errorf("fetching state at ledger %d: %w", currentLedger, err)
			}
			for _, sc := range states {
				if err := outSink.Publish(ctx, events.FromStateChange(cfg.Network.Name, sc)); err != nil {
					return fmt.Errorf("publishing state %s at ledger %d: %w", sc.EscrowID, currentLedger, err)
				}
			}
		}

		// Reconciliation sweep: one budgeted batch per ledger, only at the
		// tip. A fetch failure here is SKIP, never fatal — reconciliation
		// is best-effort by design and the rotation covers the same ids
		// again next pass; turning a sweep 429 into a crash-loop would be
		// worse than not sweeping at all. Publish failures stay fatal:
		// sink loss is a different failure class than a flaky RPC read.
		if cfg.Indexer.SweepEnabled && time.Since(ledgerClosedAt) <= sweepMaxLedgerAge {
			batch, passDone := sweeper.NextBatch(currentLedger, stateDetector.KeyCost, processors.MaxLedgerEntryKeys)
			if len(batch) > 0 {
				sweepStates, serr := stateDetector.FetchStatesSince(ctx, batch, currentLedger, ledgerClosedAt, sweeper.ModifiedSince())
				if serr != nil {
					log.Ctx(ctx).Warnf("Reconciliation sweep skipped a batch of %d escrows: %v", len(batch), serr)
				} else {
					for _, sc := range sweepStates {
						if err := outSink.Publish(ctx, events.FromStateChange(cfg.Network.Name, sc)); err != nil {
							return fmt.Errorf("publishing sweep state %s at ledger %d: %w", sc.EscrowID, currentLedger, err)
						}
					}
					if passDone {
						log.Ctx(ctx).Infof("Reconciliation sweep pass complete at ledger %d — watchlist=%d", currentLedger, reg.Size())
					}
				}
			}
		}

		// Persist the cursor only after every fact in this ledger was
		// published. On a crash between publish and save we reprocess the
		// window since the last save and the consumer dedupes by message_id
		// (at-least-once). At the tip that window is one ledger; while
		// catching up, saves space out to every checkpointInterval ledgers
		// — the state file is rewritten and fsynced whole per save, and at
		// ~80 ledgers/s per-ledger saves are pure write amplification.
		if shouldCheckpoint(catchingUp, endLedger != 0 && currentLedger == endLedger, currentLedger, lastCheckpoint) {
			if err := store.Save(ctx, snapshotState(cfg, reg, currentLedger, meta.LedgerHash().HexString(), gaps)); err != nil {
				return fmt.Errorf("saving state at ledger %d: %w", currentLedger, err)
			}
			lastCheckpoint = currentLedger
		}

		// age is how far behind the chain this ledger's data is by the time
		// we finished publishing it — the freshness number the API depends
		// on. At the tip it should sit around one close interval (~5-6s);
		// a growing age means we are falling behind (or catching up).
		log.Ctx(ctx).Infof("Processed ledger %d in %v (age %s) — known_escrows=%d escrow_events=%d state_changes=%d",
			currentLedger, time.Since(started), time.Since(ledgerClosedAt).Round(100*time.Millisecond), reg.Size(), len(facts), len(states))

		tracker.RecordLedger(health.Progress{
			LedgerSeq:      currentLedger,
			LedgerClosedAt: ledgerClosedAt,
			Duration:       time.Since(started),
			KnownEscrows:   reg.Size(),
			Events:         len(facts),
			StateChanges:   len(states),
			Gaps:           len(gaps),
		})
		heartbeat.Beat(ctx)

		lastLedgerHash = meta.LedgerHash().HexString()

		// Control plane: execute queued commands now that this ledger is
		// fully published and persisted — between ledgers is the ONE spot
		// where mutating the registry cannot race the processors. A
		// mutation persists immediately: commands were acked at enqueue,
		// so durability is on us.
		saveNow := func() error {
			return store.Save(ctx, snapshotState(cfg, reg, currentLedger, lastLedgerHash, gaps))
		}
		if mutated, cerr := drainCommands(ctx, cmdCh, executor, currentLedger, ledgerClosedAt); cerr != nil {
			return fmt.Errorf("executing command at ledger %d: %w", currentLedger, cerr)
		} else if mutated {
			if err := saveNow(); err != nil {
				return fmt.Errorf("persisting command result at ledger %d: %w", currentLedger, err)
			}
		}
		// An operator pause holds HERE — after persist, before the next
		// fetch — still draining commands so resume works, and loudly:
		// readyz 503 + heartbeat silence + periodic warnings until the
		// TTL auto-resumes.
		if executor.paused() {
			if err := holdWhilePaused(ctx, cmdCh, executor, currentLedger, ledgerClosedAt, saveNow); err != nil {
				if ctx.Err() != nil {
					log.Ctx(ctx).Infof("Ingestion loop stopped while paused at ledger %d: %v", currentLedger, ctx.Err())
					return nil
				}
				return fmt.Errorf("while paused at ledger %d: %w", currentLedger, err)
			}
		}

		currentLedger++
	}

	log.Ctx(ctx).Infof("Backfill complete: processed ledgers %d to %d", startLedger, endLedger)
	if len(pendingStates) > 0 {
		// Deliberate: a bounded backfill replays HISTORY — its job is the
		// event/deposit trail. Emitting tip-state stamped with historic
		// ledgers is the exact defect this run mode used to have; the live
		// indexer's reconciliation sweep owns current state.
		log.Ctx(ctx).Infof("Backfill suppressed state snapshots for %d escrows (historic stamping would be wrong); the live indexer's sweep reconciles them", len(pendingStates))
	}
	return nil
}

// processLedger reads a ledger's transactions and runs them through the
// indexer, returning the detected escrow events. Delivery is the caller's
// concern (none today).
func processLedger(
	ctx context.Context,
	ledgerIndexer *indexer.Indexer,
	networkPassphrase string,
	limitHint int,
	meta xdr.LedgerCloseMeta,
) ([]processors.EscrowEvent, []string, error) {
	transactions, err := readLedgerTransactions(ctx, networkPassphrase, limitHint, meta)
	if err != nil {
		return nil, nil, fmt.Errorf("reading transactions: %w", err)
	}

	facts, activeEscrows, err := ledgerIndexer.ProcessLedger(ctx, transactions)
	if err != nil {
		return nil, nil, err
	}
	return facts, activeEscrows, nil
}

// readLedgerTransactions slurps a ledger's transactions into memory using
// the SDK reader. The limit hint pre-sizes the slice to avoid repeated
// growth on busy ledgers.
func readLedgerTransactions(
	ctx context.Context,
	networkPassphrase string,
	limitHint int,
	meta xdr.LedgerCloseMeta,
) ([]sdkingest.LedgerTransaction, error) {
	reader, err := sdkingest.NewLedgerTransactionReaderFromLedgerCloseMeta(networkPassphrase, meta)
	if err != nil {
		return nil, fmt.Errorf("creating ledger transaction reader: %w", err)
	}
	defer utils.DeferredClose(ctx, reader, "closing ledger transaction reader")

	if limitHint <= 0 {
		limitHint = 64
	}
	transactions := make([]sdkingest.LedgerTransaction, 0, limitHint)
	for {
		tx, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading transaction: %w", err)
		}
		transactions = append(transactions, tx)
	}
	return transactions, nil
}

// prepareBackendRange tells the backend which range we plan to fetch so
// buffered backends (like the RPC reader) can pre-warm.
func prepareBackendRange(ctx context.Context, backend ledgerbackend.LedgerBackend, startLedger, endLedger uint32) error {
	var ledgerRange ledgerbackend.Range
	if endLedger == 0 {
		ledgerRange = ledgerbackend.UnboundedRange(startLedger)
		log.Ctx(ctx).Infof("Prepared backend with unbounded range from ledger %d", startLedger)
	} else {
		ledgerRange = ledgerbackend.BoundedRange(startLedger, endLedger)
		log.Ctx(ctx).Infof("Prepared backend with bounded range [%d, %d]", startLedger, endLedger)
	}

	if err := backend.PrepareRange(ctx, ledgerRange); err != nil {
		return fmt.Errorf("preparing range from %d: %w", startLedger, err)
	}
	return nil
}

// snapshotState assembles the persisted record from the live pieces —
// the one place that knows every field a Save must carry (forgetting
// gaps or tombstones at one call site would silently erase them).
func snapshotState(cfg *config.Config, reg *registry.Registry, lastSeq uint32, lastHash string, gaps []state.Gap) state.State {
	return state.State{
		Network:         cfg.Network.Passphrase,
		LastLedgerSeq:   lastSeq,
		LastLedgerHash:  lastHash,
		EscrowContracts: reg.Snapshot(),
		RemovedEscrows:  reg.RemovedSnapshot(),
		Gaps:            gaps,
	}
}

// publishGaps republishes every recorded gap through the sink. Message
// ids are deterministic ("gap:{net}:{from}:{to}"), so the consumer
// treats repeats as no-ops — the call is safe after every (re)connect.
func publishGaps(ctx context.Context, outSink sink.Sink, network string, gaps []state.Gap) error {
	for _, g := range gaps {
		if err := outSink.Publish(ctx, events.FromGap(network, g.FromLedger, g.ToLedger, g.Reason, g.DetectedAt)); err != nil {
			return fmt.Errorf("publishing gap evidence [%d, %d]: %w", g.FromLedger, g.ToLedger, err)
		}
	}
	return nil
}

// fetchLedgerWithRetry wraps GetLedger with bounded exponential backoff.
// It honours ctx cancellation between attempts and gives up after
// maxLedgerFetchRetries failures; the caller (the failover loop) treats
// any error as "this endpoint is done" and rotates the pool.
//
// Window errors get special handling because some RPC providers reject a
// request for the next ledger with a "must be between oldest and latest"
// error instead of blocking until it closes (observed live on mainnet
// providers). Beyond-tip is normal tip-following — wait briefly without
// consuming an attempt. Below-retention is deterministic — no number of
// retries can fix it, so fail immediately with an actionable message; a
// deeper-retention endpoint in the pool may still serve the ledger.
//
// stallBudget is the tip watchdog. It bounds the two ways a stalled
// endpoint can hold the loop without ever failing: the SDK's GetLedger
// blocking internally at a frozen tip (bounded per attempt via a context
// deadline — the mechanism the SDK documents for this), and the
// reject-style providers above accumulating beyond-tip waits. Exceeding
// the budget returns errTipStalled so the caller rotates instead of
// waiting on a frozen endpoint forever.
func fetchLedgerWithRetry(ctx context.Context, backend ledgerbackend.LedgerBackend, ledgerSeq uint32, stallBudget time.Duration) (xdr.LedgerCloseMeta, error) {
	backoff := initialRetryBackoff
	var lastErr error
	attempt := 1
	var tipWaited time.Duration

	for attempt <= maxLedgerFetchRetries {
		if err := ctx.Err(); err != nil {
			return xdr.LedgerCloseMeta{}, err
		}

		attemptCtx, cancel := context.WithTimeout(ctx, stallBudget)
		meta, err := backend.GetLedger(attemptCtx, ledgerSeq)
		stalled := attemptCtx.Err() != nil && ctx.Err() == nil
		cancel()
		if err == nil {
			return meta, nil
		}

		// Our per-attempt deadline expired while the caller's ctx is
		// still alive: the endpoint sat on the request for the whole
		// stall budget without producing the ledger or an error.
		if stalled {
			return xdr.LedgerCloseMeta{}, fmt.Errorf("%w: no ledger %d from this endpoint within %s", errTipStalled, ledgerSeq, stallBudget)
		}

		// Context cancellation is not transient — surface immediately.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return xdr.LedgerCloseMeta{}, err
		}

		switch class, oldest, latest := classifyWindowError(err, ledgerSeq); class {
		case windowBeyondTip:
			tipWaited += tipWaitInterval
			if tipWaited >= stallBudget {
				return xdr.LedgerCloseMeta{}, fmt.Errorf("%w: ledger %d still beyond this endpoint's tip (%d) after %s", errTipStalled, ledgerSeq, latest, tipWaited)
			}
			if int(tipWaited/tipWaitInterval)%tipWaitWarnEvery == 0 {
				log.Ctx(ctx).Warnf("Ledger %d still beyond RPC tip (%d) after ~%s — tip may be stalled",
					ledgerSeq, latest, tipWaited)
			}
			select {
			case <-ctx.Done():
				return xdr.LedgerCloseMeta{}, ctx.Err()
			case <-time.After(tipWaitInterval):
			}
			continue
		case windowBelowRetention:
			return xdr.LedgerCloseMeta{}, fmt.Errorf(
				"ledger %d is below the RPC retention window (oldest available: %d): retrying against this endpoint cannot help — a deeper-retention fallback, a state-file reset, or an archive backfill can: %w",
				ledgerSeq, oldest, err)
		}

		lastErr = err
		log.Ctx(ctx).Warnf("Error fetching ledger %d (attempt %d/%d): %v, retrying in %v",
			ledgerSeq, attempt, maxLedgerFetchRetries, err, backoff)

		select {
		case <-ctx.Done():
			return xdr.LedgerCloseMeta{}, ctx.Err()
		case <-time.After(backoff):
		}

		if backoff < maxRetryBackoff {
			backoff *= 2
			if backoff > maxRetryBackoff {
				backoff = maxRetryBackoff
			}
		}
		attempt++
	}

	return xdr.LedgerCloseMeta{}, fmt.Errorf("giving up after %d attempts: %w", maxLedgerFetchRetries, lastErr)
}

// windowErrorClass classifies an RPC "requested ledger outside my window"
// error relative to the ledger we asked for.
type windowErrorClass int

const (
	// windowNone: not a window error (or unparseable) — treat as transient.
	windowNone windowErrorClass = iota
	// windowBeyondTip: the ledger has not closed yet on this RPC.
	windowBeyondTip
	// windowBelowRetention: the ledger fell out of the RPC's retention.
	windowBelowRetention
)

// windowErrRe matches the stellar-rpc window error, e.g.:
//
//	[-32600] start ledger (63613791) must be between the oldest ledger: 2
//	and the latest ledger: 63613790 for this rpc instance
//
// The requested sequence is taken from the caller, not the message, so the
// match tolerates format drift around it.
var windowErrRe = regexp.MustCompile(`oldest ledger:?\s*(\d+).*?latest ledger:?\s*(\d+)`)

// classifyWindowError inspects err and, when it is a window error, returns
// where the requested ledger sits relative to the advertised window.
func classifyWindowError(err error, requested uint32) (windowErrorClass, uint32, uint32) {
	m := windowErrRe.FindStringSubmatch(err.Error())
	if m == nil {
		return windowNone, 0, 0
	}
	oldest64, oerr := strconv.ParseUint(m[1], 10, 32)
	latest64, lerr := strconv.ParseUint(m[2], 10, 32)
	if oerr != nil || lerr != nil {
		return windowNone, 0, 0
	}
	oldest, latest := uint32(oldest64), uint32(latest64)
	switch {
	case requested > latest:
		return windowBeyondTip, oldest, latest
	case requested < oldest:
		return windowBelowRetention, oldest, latest
	}
	return windowNone, oldest, latest
}
