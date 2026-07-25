// Package sink is the Indexer's output abstraction. A Sink receives
// fully-formed envelopes (one per detected fact) and delivers them to a
// transport-specific destination (RabbitMQ today; Kafka, Postgres, etc.
// could be added without changing callers).
//
// Implementations MUST:
//   - be safe for sequential Publish calls from the ingest loop;
//   - return ErrSinkUnavailable (wrapped) when the transport is
//     unreachable;
//   - return ErrSinkPublishRejected (wrapped) when the broker rejects or
//     fails to confirm a publish but the connection remains usable
//     (backpressure — the one class callers may retry);
//   - return ErrSinkUnroutable (wrapped) when the broker accepted the
//     publish but nothing was bound to receive it (broken topology —
//     never retryable);
//   - return events.ErrEnvelopeInvalid (wrapped) when the envelope fails
//     validation;
//   - honour ctx for cancellation/timeouts.
package sink

import (
	"context"
	"errors"

	"github.com/Trustless-Work/Indexer/internal/events"
)

// Sink delivers one envelope at a time to a transport destination.
type Sink interface {
	// Publish delivers a single envelope. Returns nil only when delivery
	// can be claimed at-least-once (for RabbitMQ, a positive publisher
	// confirm). The caller owns retry policy; Publish itself does none.
	Publish(ctx context.Context, env events.Envelope) error
	// Close releases held resources. Idempotent; Publish must not be
	// called after Close.
	Close() error
}

var (
	// ErrSinkUnavailable signals the transport is unreachable (dial /
	// channel / publish failure). NOT retryable in-process: an AMQP
	// channel that failed is closed for good, so only a reconnect (today:
	// crash-restart) can recover.
	ErrSinkUnavailable = errors.New("sink unavailable")
	// ErrSinkPublishRejected signals the broker explicitly rejected the
	// publish (a nack — e.g. a full queue with a reject-publish overflow
	// policy — or a confirm timeout). The channel stays usable, so this
	// IS the class a caller may retry with backoff: backpressure, not
	// breakage.
	ErrSinkPublishRejected = errors.New("sink publish rejected")
	// ErrSinkUnroutable signals the broker accepted the publish but no
	// queue was bound to receive it (basic.return on a mandatory
	// publish). Deliberately distinct from ErrSinkPublishRejected:
	// retrying cannot fix missing topology — every retry would confirm
	// and come back returned, a silent hot loop — so callers must treat
	// it as fatal and alert.
	ErrSinkUnroutable = errors.New("sink publish unroutable")
)
