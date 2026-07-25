package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/Trustless-Work/Indexer/internal/commands"
	"github.com/Trustless-Work/Indexer/internal/health"
	"github.com/Trustless-Work/Indexer/internal/indexer/processors"
	"github.com/Trustless-Work/Indexer/internal/indexer/registry"
)

// fakeFetcher records requested ids and returns one canned state per id.
type fakeFetcher struct {
	requested [][]string
	err       error
}

func (f *fakeFetcher) FetchStates(ctx context.Context, ids []string, seq uint32, at time.Time) ([]processors.EscrowStateChange, error) {
	f.requested = append(f.requested, ids)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]processors.EscrowStateChange, 0, len(ids))
	for _, id := range ids {
		out = append(out, processors.EscrowStateChange{
			EscrowID:        id,
			StateChangeType: "updated",
			LedgerSeq:       seq,
			LedgerClosedAt:  at,
			RawXDR:          "AAAA",
		})
	}
	return out, nil
}

type fakeSweeper struct{ resets int }

func (f *fakeSweeper) Reset() { f.resets++ }

func newTestExecutor(t *testing.T) (*commandExecutor, *fakeFetcher, *scriptedSink, *fakeSweeper) {
	t.Helper()
	reg, err := registry.New(nil)
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	fetcher := &fakeFetcher{}
	out := &scriptedSink{}
	sweeper := &fakeSweeper{}
	return &commandExecutor{
		reg:      reg,
		detector: fetcher,
		outSink:  out,
		sweeper:  sweeper,
		tracker:  health.NewTracker("testnet"),
		network:  "testnet",
		now:      time.Now,
	}, fetcher, out, sweeper
}

func TestExecute_TrackFetchesAndPublishesImmediately(t *testing.T) {
	exec, fetcher, out, _ := newTestExecutor(t)

	mutated, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindTrackEscrow, ContractID: "C1"}, 100, time.Now())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !mutated {
		t.Error("tracking a new escrow must report a mutation")
	}
	if !exec.reg.IsEscrow("C1") {
		t.Error("C1 must be tracked")
	}
	if len(fetcher.requested) != 1 || fetcher.requested[0][0] != "C1" {
		t.Errorf("state fetch = %v, want [[C1]]", fetcher.requested)
	}
	if out.calls != 1 {
		t.Errorf("published %d envelopes, want 1", out.calls)
	}

	// Idempotent: tracking again is a no-op mutation-wise but still
	// refreshes state (the caller wanted it watched and current).
	mutated, err = exec.execute(context.Background(), commands.Command{Kind: commands.KindTrackEscrow, ContractID: "C1"}, 101, time.Now())
	if err != nil || mutated {
		t.Errorf("re-track: mutated=%v err=%v, want false/nil", mutated, err)
	}
}

func TestExecute_TrackClearsTombstone(t *testing.T) {
	exec, _, _, _ := newTestExecutor(t)
	exec.reg.Seed([]string{"C1"})
	exec.reg.Remove("C1")

	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindTrackEscrow, ContractID: "C1"}, 100, time.Now()); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !exec.reg.IsEscrow("C1") {
		t.Error("explicit track must clear the tombstone")
	}
	if len(exec.reg.RemovedSnapshot()) != 0 {
		t.Errorf("tombstones = %v, want none", exec.reg.RemovedSnapshot())
	}
}

func TestExecute_RefreshOnlyWorksForTracked(t *testing.T) {
	exec, fetcher, out, _ := newTestExecutor(t)

	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindRefreshEscrow, ContractID: "C1"}, 100, time.Now()); err != nil {
		t.Fatalf("refresh unknown: %v", err)
	}
	if len(fetcher.requested) != 0 || out.calls != 0 {
		t.Error("refreshing an untracked escrow must not fetch or publish")
	}

	exec.reg.Seed([]string{"C1"})
	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindRefreshEscrow, ContractID: "C1"}, 100, time.Now()); err != nil {
		t.Fatalf("refresh tracked: %v", err)
	}
	if out.calls != 1 {
		t.Errorf("published %d envelopes, want 1", out.calls)
	}
}

func TestExecute_ReseedSkipsTombstonedIds(t *testing.T) {
	exec, fetcher, out, _ := newTestExecutor(t)
	exec.reg.Seed([]string{"C9"})
	exec.reg.Remove("C9")

	mutated, err := exec.execute(context.Background(), commands.Command{
		Kind:        commands.KindReseed,
		ContractIDs: []string{"C1", "C2", "C9"},
	}, 100, time.Now())
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !mutated {
		t.Error("reseed adding escrows must report a mutation")
	}
	if exec.reg.IsEscrow("C9") {
		t.Error("reseed must not resurrect a tombstoned escrow")
	}
	if len(fetcher.requested) != 1 || len(fetcher.requested[0]) != 2 {
		t.Errorf("state fetch = %v, want one batch of the 2 tracked ids", fetcher.requested)
	}
	if out.calls != 2 {
		t.Errorf("published %d envelopes, want 2", out.calls)
	}
}

func TestExecute_RemoveTombstonesAndPersistsIntent(t *testing.T) {
	exec, _, _, _ := newTestExecutor(t)
	exec.reg.Seed([]string{"C1"})

	mutated, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindRemoveEscrow, ContractID: "C1"}, 100, time.Now())
	if err != nil || !mutated {
		t.Fatalf("remove: mutated=%v err=%v, want true/nil", mutated, err)
	}
	if exec.reg.IsEscrow("C1") {
		t.Error("C1 must not be tracked after remove")
	}
	// Discovery/seed must not bring it back.
	exec.reg.Seed([]string{"C1"})
	if exec.reg.IsEscrow("C1") {
		t.Error("seed resurrected a tombstoned escrow")
	}
}

func TestExecute_StateFetchFailureIsSkippableNotFatal(t *testing.T) {
	exec, fetcher, out, _ := newTestExecutor(t)
	fetcher.err = context.DeadlineExceeded

	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindTrackEscrow, ContractID: "C1"}, 100, time.Now()); err != nil {
		t.Fatalf("a flaky RPC read must not kill the loop; got %v", err)
	}
	if !exec.reg.IsEscrow("C1") {
		t.Error("the escrow must stay tracked; the sweep reconciles its state later")
	}
	if out.calls != 0 {
		t.Errorf("published %d envelopes, want 0", out.calls)
	}
}

func TestExecute_PauseResumeAndReconcile(t *testing.T) {
	exec, _, _, sweeper := newTestExecutor(t)

	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindPause, TTLSeconds: 60}, 100, time.Now()); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if !exec.paused() {
		t.Fatal("executor must report paused")
	}
	if ready, reason := exec.tracker.Ready(); ready || reason == "" {
		t.Errorf("a paused indexer must not be ready (got ready=%v reason=%q)", ready, reason)
	}

	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindResume}, 100, time.Now()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if exec.paused() {
		t.Fatal("resume must lift the pause")
	}

	if _, err := exec.execute(context.Background(), commands.Command{Kind: commands.KindReconcile}, 100, time.Now()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if sweeper.resets != 1 {
		t.Errorf("sweeper resets = %d, want 1", sweeper.resets)
	}
}

func TestHoldWhilePaused_ResumeCommandLiftsTheHold(t *testing.T) {
	exec, _, _, _ := newTestExecutor(t)
	exec.pausedUntil = time.Now().Add(time.Hour)
	exec.tracker.SetPaused(exec.pausedUntil)

	ch := make(chan commands.Command, 1)
	ch <- commands.Command{Kind: commands.KindResume}

	done := make(chan error, 1)
	go func() {
		done <- holdWhilePaused(context.Background(), ch, exec, 100, time.Now(), func() error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("holdWhilePaused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not lift the pause")
	}
}

func TestHoldWhilePaused_TTLExpiryAutoResumes(t *testing.T) {
	exec, _, _, _ := newTestExecutor(t)
	exec.pausedUntil = time.Now().Add(30 * time.Millisecond)
	exec.tracker.SetPaused(exec.pausedUntil)

	ch := make(chan commands.Command)
	done := make(chan error, 1)
	go func() {
		done <- holdWhilePaused(context.Background(), ch, exec, 100, time.Now(), func() error { return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("holdWhilePaused: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TTL expiry did not auto-resume")
	}
	if exec.paused() {
		t.Error("executor must not stay paused after TTL expiry")
	}
	if ready, _ := exec.tracker.Ready(); ready {
		// Not ready only because no ledger was processed in this test;
		// the pause reason itself must be gone.
		if _, reason := exec.tracker.Ready(); reason == "" || reason[:6] == "paused" {
			t.Errorf("pause reason must clear after auto-resume, got %q", reason)
		}
	}
}

func TestDrainCommands_ExecutesEverythingQueued(t *testing.T) {
	exec, _, _, sweeper := newTestExecutor(t)
	ch := make(chan commands.Command, 3)
	ch <- commands.Command{Kind: commands.KindTrackEscrow, ContractID: "C1"}
	ch <- commands.Command{Kind: commands.KindReconcile}
	ch <- commands.Command{Kind: commands.KindTrackEscrow, ContractID: "C2"}

	mutated, err := drainCommands(context.Background(), ch, exec, 100, time.Now())
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !mutated {
		t.Error("drain must report the registry mutations")
	}
	if exec.reg.Size() != 2 || sweeper.resets != 1 {
		t.Errorf("size=%d resets=%d, want 2 and 1", exec.reg.Size(), sweeper.resets)
	}
	if m, _ := drainCommands(context.Background(), ch, exec, 100, time.Now()); m {
		t.Error("an empty queue must drain as a no-op")
	}
}
