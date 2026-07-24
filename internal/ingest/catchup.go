package ingest

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// catchUpMinAge decides when the loop treats itself as CATCHING UP: a
// ledger closed longer ago than this is historic replay, not the tip.
// ~240 ledgers at the ~5s close cadence — short outages (a redeploy, a
// broker blip) stay on the normal per-ledger path; only real backlogs
// switch modes.
const catchUpMinAge = 20 * time.Minute

// checkpointInterval is how many ledgers may pass between cursor saves
// while catching up. The state file is rewritten and fsynced WHOLE on
// every save; at catch-up speed (~80 ledgers/s measured) saving per
// ledger turns into pure write amplification. A crash reprocesses at
// most this window — absorbed by the consumer's dedupe (at-least-once).
const checkpointInterval = 100

// shouldCheckpoint says whether the cursor must be persisted after the
// current ledger. At the tip: always (a restart must not lose the spot).
// Catching up: every checkpointInterval ledgers, plus the final ledger
// of a bounded backfill so the run's end is durable.
func shouldCheckpoint(catchingUp, isFinal bool, currentLedger, lastCheckpoint uint32) bool {
	return !catchingUp || isFinal || currentLedger-lastCheckpoint >= checkpointInterval
}

// drainPending merges the deferred escrow set with the current ledger's
// active ones, empties the set, and returns the union sorted (stable
// publish order). Called once, on the ledger where the loop reaches the
// tip again.
func drainPending(pending map[string]struct{}, active []string) []string {
	for _, id := range active {
		pending[id] = struct{}{}
	}
	out := make([]string, 0, len(pending))
	for id := range pending {
		out = append(out, id)
	}
	sort.Strings(out)
	clear(pending)
	return out
}

// verifyChainContinuity checks that meta — the first ledger fetched on a
// contiguous resume — actually descends from the last ledger this state
// file processed. The chain gives this for free: every header carries
// its parent's hash, so no extra RPC call is needed. A mismatch means
// the RPC serves a different chain than the state was built on (wrong
// RPC_URL, or a network reset that kept the passphrase) — processing
// would corrupt the read-model with cross-chain data, so it is fatal
// with the remediation in the message (Sprint 5: fail fast and clear,
// never stall).
func verifyChainContinuity(expectedPrevHash string, meta xdr.LedgerCloseMeta) error {
	got := meta.PreviousLedgerHash().HexString()
	if strings.EqualFold(got, expectedPrevHash) {
		return nil
	}
	return fmt.Errorf(
		"state diverged: ledger %d does not descend from the last processed ledger (state recorded hash %s, chain reports previous hash %s) — RPC_URL serves a different chain than this state file, or the network was reset; archive the state file and start fresh",
		meta.LedgerSequence(), expectedPrevHash, got)
}
