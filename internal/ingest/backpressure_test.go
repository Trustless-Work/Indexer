package ingest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Trustless-Work/Indexer/internal/events"
	"github.com/Trustless-Work/Indexer/internal/sink"
)

// scriptedSink returns its errs in order, then succeeds forever.
type scriptedSink struct {
	errs  []error
	calls int
}

func (s *scriptedSink) Publish(ctx context.Context, env events.Envelope) error {
	s.calls++
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

func (s *scriptedSink) Close() error { return nil }

// testBackpressureSink wraps inner with instant waits, recording each
// backoff duration the production policy would have slept.
func testBackpressureSink(inner sink.Sink, waits *[]time.Duration) *backpressureSink {
	b := newBackpressureSink(inner)
	b.wait = func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return ctx.Err()
	}
	return b
}

func TestBackpressure_RetriesRejectionsUntilAccepted(t *testing.T) {
	rejected := fmt.Errorf("%w: broker nacked m1", sink.ErrSinkPublishRejected)
	inner := &scriptedSink{errs: []error{rejected, rejected, rejected}}
	var waits []time.Duration
	b := testBackpressureSink(inner, &waits)

	if err := b.Publish(context.Background(), events.Envelope{MessageID: "m1"}); err != nil {
		t.Fatalf("expected the publish to survive backpressure, got %v", err)
	}
	if inner.calls != 4 {
		t.Errorf("inner Publish calls = %d, want 4", inner.calls)
	}
	// Exponential 1s -> 2s -> 4s; the cap only matters deeper in.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d = %s, want %s", i, waits[i], want[i])
		}
	}
}

func TestBackpressure_BackoffCapsAtMax(t *testing.T) {
	rejected := fmt.Errorf("%w: confirm timeout", sink.ErrSinkPublishRejected)
	errs := make([]error, 9)
	for i := range errs {
		errs[i] = rejected
	}
	inner := &scriptedSink{errs: errs}
	var waits []time.Duration
	b := testBackpressureSink(inner, &waits)

	if err := b.Publish(context.Background(), events.Envelope{MessageID: "m1"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// 1,2,4,8,16,32,60,60,60 — doubling clamps at publishRetryMaxBackoff.
	last := waits[len(waits)-1]
	if last != publishRetryMaxBackoff {
		t.Errorf("final backoff = %s, want cap %s", last, publishRetryMaxBackoff)
	}
	for _, w := range waits {
		if w > publishRetryMaxBackoff {
			t.Errorf("backoff %s exceeded the cap", w)
		}
	}
}

func TestBackpressure_FatalClassesPassThroughImmediately(t *testing.T) {
	fatal := []error{
		fmt.Errorf("%w: dial: connection refused", sink.ErrSinkUnavailable),
		fmt.Errorf("%w: broker returned m1 unroutable", sink.ErrSinkUnroutable),
		fmt.Errorf("%w: missing type", events.ErrEnvelopeInvalid),
	}
	for _, ferr := range fatal {
		inner := &scriptedSink{errs: []error{ferr}}
		var waits []time.Duration
		b := testBackpressureSink(inner, &waits)

		err := b.Publish(context.Background(), events.Envelope{MessageID: "m1"})
		if !errors.Is(err, errors.Unwrap(ferr)) {
			t.Errorf("expected %v to pass through, got %v", ferr, err)
		}
		if inner.calls != 1 {
			t.Errorf("%v: inner calls = %d, want 1 (no retry)", ferr, inner.calls)
		}
		if len(waits) != 0 {
			t.Errorf("%v: unexpected backoff waits %v", ferr, waits)
		}
	}
}

func TestBackpressure_ShutdownDuringWaitReturnsPromptly(t *testing.T) {
	rejected := fmt.Errorf("%w: broker nacked m1", sink.ErrSinkPublishRejected)
	// Endless rejections: only ctx can end this publish.
	inner := &scriptedSink{errs: []error{rejected, rejected, rejected, rejected}}
	b := newBackpressureSink(inner)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := b.Publish(ctx, events.Envelope{MessageID: "m1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s; the wait must honour ctx", elapsed)
	}
}
