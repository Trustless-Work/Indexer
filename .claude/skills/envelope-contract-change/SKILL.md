---
name: envelope-contract-change
description: How to change what the Indexer publishes — envelope fields, schema_version, message ids, routing keys, exchange or fact types — without silently breaking the core API that consumes it. Use when touching internal/events, internal/sink/rabbitmq, docs/event-schema.md, or adding a new fact/processor whose output reaches the broker.
---

# Envelope contract change

The envelope is a **shared contract with `trustlesswork-core-api`**. That repo's
ingestion consumer (`src/presentation/ingestion`, `src/application/ingestion`) parses
exactly the shape in `docs/event-schema.md` and dedupes on `message_id`. The two repos
deploy independently, so a change here can be live for hours before the consumer knows.

## Non-negotiables

- **Thin indexer, smart consumer.** Forward raw XDR. If a change needs the indexer to
  decode escrow-specific data (amounts, milestones, roles), stop — that logic belongs in
  the consumer.
- **`message_id` is deterministic and is the idempotency key.** Changing how it is built
  (`NewMessageID`, `NewStateMessageID`, `NewGapMessageID` in
  `internal/events/envelope.go`) changes identity: replays stop deduping against
  history. Do it only with a schema bump and a stated migration for the consumer.
- **Total order key** (`ledger_seq`, `tx_index`, `event_index`) must remain present
  and comparable.

## Procedure

1. **Classify the change.**
   - *Additive* (new optional field, new fact `type` the consumer can ignore): minor
     bump of `CurrentSchemaVersion` (`1.1` → `1.2`).
   - *Breaking* (renamed/removed field, changed semantics, changed `message_id`
     derivation, new required field, new routing key the consumer must bind): major
     bump, and the consumer must ship first or in lockstep. Say which in the PR.
2. **Update `docs/event-schema.md` in the same PR** — field table, example JSON,
   routing keys. The spec and the code must never disagree at any commit. (Ignore the
   stale "Status" block at the top unless you are removing it.)
3. **Update `CHANGELOG.md` `[Unreleased]`** with the schema version and the
   consumer-facing effect.
4. **Cover it in `internal/events` tests** (shape, ids, version) and in
   `internal/sink/rabbitmq` tests if routing/headers changed.
5. **Replay is part of the contract.** `indexer replay` re-publishes events/deposits of
   a range with the SAME ids; the consumer must dedupe old and new copies. If the change
   makes replayed messages differ from originally published ones, that is breaking.
6. **Coordinate the consumer.** Open (or describe in the PR) the matching change in
   core-api and the deploy order. Never merge a breaking envelope change with a
   "the core will catch up" note.

## Done when

Schema version bumped correctly, spec + changelog updated in the same PR, tests cover
the new shape, and the PR body names the consumer change and the deploy order.
