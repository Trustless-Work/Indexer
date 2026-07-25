package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stellar/go-stellar-sdk/support/log"
)

const (
	// consumerRedialInitialBackoff paces reconnection attempts after the
	// command connection drops; doubles up to consumerRedialMaxBackoff.
	consumerRedialInitialBackoff = 5 * time.Second
	consumerRedialMaxBackoff     = time.Minute
)

// ConsumerConfig wires the AMQP command consumer.
type ConsumerConfig struct {
	// URL is the AMQP connection string (same broker as the sink, but the
	// consumer dials its OWN connection: publisher and consumer sharing a
	// connection is an AMQP anti-pattern, and a publish-side failure —
	// which is fatal for the pipeline — must not tear down command
	// consumption, nor the other way around).
	URL string
	// Exchange is the direct exchange commands are published to
	// (stellar.commands). The consumer declares it durable.
	Exchange string
	// Network selects the binding: queue indexer.commands.<network>,
	// routing key <network>. One indexer instance per network consumes
	// only its own commands.
	Network string
}

// QueueName returns the durable queue this consumer owns.
func (c ConsumerConfig) QueueName() string {
	return "indexer.commands." + c.Network
}

// Consume receives commands from the broker and enqueues them into out
// until ctx ends. It blocks — run it in a goroutine.
//
// Delivery semantics: a message is acked after the parsed command is
// ENQUEUED (not executed) — at-least-once into the channel, which is
// safe because every command is idempotent. Malformed or unknown
// commands are nacked WITHOUT requeue and logged: a poison message must
// not clog the queue or crash-loop the consumer.
//
// Resilience: the consumer is auxiliary — it must never take the
// pipeline down. Any connection/channel failure is logged and redialed
// with backoff, forever; meanwhile the admin HTTP surface keeps
// accepting commands.
func Consume(ctx context.Context, cfg ConsumerConfig, out chan<- Command) {
	backoff := consumerRedialInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := consumeOnce(ctx, cfg, out)
		if ctx.Err() != nil {
			return
		}
		log.Ctx(ctx).Warnf("Command consumer disconnected: %v — redialing in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < consumerRedialMaxBackoff {
			backoff *= 2
			if backoff > consumerRedialMaxBackoff {
				backoff = consumerRedialMaxBackoff
			}
		}
	}
}

// consumeOnce runs one connection lifetime: dial, declare topology,
// consume until the channel dies or ctx ends.
func consumeOnce(ctx context.Context, cfg ConsumerConfig, out chan<- Command) error {
	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}

	// The consumer owns the command topology end to end (the sink's
	// exchange-only rule applies to the EVENTS exchange, where consumers
	// bind their own queues; here the indexer IS the consumer).
	if err := ch.ExchangeDeclare(cfg.Exchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %q: %w", cfg.Exchange, err)
	}
	// Quorum from birth (the queue shipped with the same release): no
	// classic→quorum migration event, matching the core's queues.
	queue := cfg.QueueName()
	if _, err := ch.QueueDeclare(queue, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return fmt.Errorf("declare queue %q: %w", queue, err)
	}
	if err := ch.QueueBind(queue, cfg.Network, cfg.Exchange, false, nil); err != nil {
		return fmt.Errorf("bind %q to %q with key %q: %w", queue, cfg.Exchange, cfg.Network, err)
	}

	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %q: %w", queue, err)
	}
	log.Ctx(ctx).Infof("Command consumer listening on %s (exchange %s, key %s)", queue, cfg.Exchange, cfg.Network)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			cmd, perr := Parse(d.Body)
			if perr != nil {
				log.Ctx(ctx).Errorf("Dropping unusable command message: %v (body %d bytes)", perr, len(d.Body))
				_ = d.Nack(false, false)
				continue
			}
			cmd.Source = "amqp"
			select {
			case out <- cmd:
				_ = d.Ack(false)
			case <-ctx.Done():
				// Unacked: the broker redelivers after reconnect —
				// at-least-once, absorbed by idempotency.
				return ctx.Err()
			}
		}
	}
}
