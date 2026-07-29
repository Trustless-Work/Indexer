package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/Trustless-Work/Indexer/internal/events"
	"github.com/Trustless-Work/Indexer/internal/sink"
	"github.com/stellar/go-stellar-sdk/support/log"
)

const (
	// publishRetryInitialBackoff is the first wait after a broker
	// rejection; it doubles per retry up to publishRetryMaxBackoff.
	publishRetryInitialBackoff = time.Second
	// publishRetryMaxBackoff caps the wait between publish retries. The
	// retry count itself is deliberately UNCAPPED: a full queue is the
	// consumer's problem to drain, and the indexer's job is to wait
	// without advancing the cursor — idempotent message ids plus the
	// consumer's dedupe make indefinite republishing safe.
	publishRetryMaxBackoff = 60 * time.Second
)

// backpressureSink wraps a sink.Sink with the loop's retry policy (the
// Sink contract says the caller owns retries). Only ErrSinkPublishRejected
// — a nack from a full queue — is retried, with 1s→60s backoff and no
// attempt cap: the broker is alive and will drain. A slow broker never
// reaches here: the sink waits for each message's own confirmation rather
// than timing out (audit A2), so this path now sees genuine rejections
// only.
// Everything else passes through as fatal, each for its own reason:
//
//   - events.ErrEnvelopeInvalid: a bug in the producer, never transient.
//   - sink.ErrSinkUnroutable: broken topology; every retry would confirm
//     and come straight back returned — a silent hot loop, not recovery.
//   - sink.ErrSinkUnavailable: the AMQP channel is closed for good (the
//     library never reuses a failed channel), so in-process retries can
//     never succeed; the crash-restart owns the redial, and boot resumes
//     from the persisted cursor.
//
// While a publish is blocked no ledgers are processed, so /readyz goes
// 503 and the outbound heartbeat falls silent — the external monitor
// alerts while the loop waits. That alerting is the prerequisite that
// makes in-process waiting acceptable at all (the audit rule: never
// retry quietly before observability exists).
type backpressureSink struct {
	inner          sink.Sink
	initialBackoff time.Duration
	maxBackoff     time.Duration
	// wait blocks for d or until ctx ends; injected so tests do not
	// sleep for real.
	wait func(ctx context.Context, d time.Duration) error
}

var _ sink.Sink = (*backpressureSink)(nil)

// newBackpressureSink wraps inner with the production retry policy.
func newBackpressureSink(inner sink.Sink) *backpressureSink {
	return &backpressureSink{
		inner:          inner,
		initialBackoff: publishRetryInitialBackoff,
		maxBackoff:     publishRetryMaxBackoff,
		wait:           waitCtx,
	}
}

// Publish delivers env, absorbing broker backpressure by retrying in
// place. It returns nil only after the inner sink accepted the envelope,
// ctx errors on shutdown, and the fatal classes above unchanged.
func (b *backpressureSink) Publish(ctx context.Context, env events.Envelope) error {
	backoff := b.initialBackoff
	started := time.Now()
	for attempt := 1; ; attempt++ {
		err := b.inner.Publish(ctx, env)
		if err == nil {
			if attempt > 1 {
				log.Ctx(ctx).Infof("Broker accepted %s after %d attempts (%s of backpressure)",
					env.MessageID, attempt, time.Since(started).Round(time.Second))
			}
			return nil
		}
		if !errors.Is(err, sink.ErrSinkPublishRejected) {
			return err
		}
		// Accumulated lag in every line: when this warning repeats, the
		// operator's first question is how far behind we are falling.
		log.Ctx(ctx).Warnf("Broker is pushing back on %s (attempt %d, %s blocked so far): %v — retrying in %s without advancing the cursor",
			env.MessageID, attempt, time.Since(started).Round(time.Second), err, backoff)
		if werr := b.wait(ctx, backoff); werr != nil {
			return werr
		}
		if backoff < b.maxBackoff {
			backoff *= 2
			if backoff > b.maxBackoff {
				backoff = b.maxBackoff
			}
		}
	}
}

// Close passes through to the wrapped sink.
func (b *backpressureSink) Close() error { return b.inner.Close() }

// waitCtx sleeps for d unless ctx ends first.
func waitCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
