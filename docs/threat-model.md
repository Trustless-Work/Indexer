# Threat model

Scope: the Trustless Work Indexer process — what it trusts, who can attack
it, which defenses exist in code today, and which limits are deliberate
decisions rather than omissions. Every claim in this document was verified
against the code on 2026-10-12; if you change a referenced mechanism,
change this document in the same PR. **A ceiling documented with numbers
is a decision; a ceiling an auditor discovers is a finding** — that rule
is why this file exists.

## What this service protects

1. **Integrity and completeness of the published fact stream**: every
   fact from every ledger that concerns us reaches the broker, exactly
   as the chain stated it, or the absence is recorded as durable gap
   evidence. A silent gap is a failure even when nothing was technically
   lost.
2. **The cursor and evidence state** (`STATE_PATH` + its watchlist
   file): the proof of what was and was not processed.
3. **Secrets**: the admin bearer token, the envelope HMAC key, broker
   credentials, the heartbeat URL.

## Trust assumptions

- **The RPC provider is honest-but-fallible, not malicious.** The
  Indexer defends against a *wrong* or *broken* provider (wrong network,
  forked serving, stalls, malformed data, false absences) but NOT
  against a provider that fabricates internally-consistent ledgers:
  identity comes from WASM hashes read via the same provider, so a fully
  compromised provider can fabricate facts for approved contracts. This
  is a deliberate scope decision — the mitigations are provider
  reputation (Validation Cloud + known fallbacks), fail-fast checks
  below, and replay against an independent archive endpoint as the
  recovery authority. Multi-provider cross-validation of content is
  explicitly out of scope today.
- **The health/admin port is never public.** It binds all interfaces
  but must only be reachable over the platform's private network. Every
  defense on that port is defence in depth behind this doctrine, not a
  substitute for it.
- **The broker is reached over private networking.** Plaintext
  `amqp://` is accepted with a loud boot warning for exactly this
  deployment shape; anywhere else, use `amqps://`.
- **One process, one state.** The flock on the state file enforces a
  single live writer; horizontal scaling is a non-goal (see Ceilings).

## Actors and defenses

### 1. Attacker who reaches the health port

Capabilities: HTTP to `/healthz`, `/readyz`, `/status`, `/metrics`,
`/admin/*`.

Defenses in code:
- `/admin/*` requires a bearer token compared in constant time
  (`internal/commands`); an empty token unmounts the surface entirely.
- Boot refuses a set-but-short token (<16 chars,
  `internal/config/validate.go`) — the token is the wall, so it must not
  be guessable.
- Server-wide read/write/idle timeouts (`internal/health/server.go`)
  close the slowloris hole gosec flagged as G112.
- Admin bodies are capped (1MB), reseed batches are capped, pause TTL is
  capped with noisy auto-resume; contract ids are strkey-validated at
  the edge.

Accepted residual risk: `/status` and `/metrics` answer without auth and
reveal operational details (RPC endpoint host, cursor, watchlist size).
Acceptable only because of the never-public doctrine.

**Deliberate non-implementation — request rate limiting**: behind the
platform proxy, per-IP limiting keys on a spoofable header or on the
proxy's shared address, and a global limiter hands an attacker an
operator-lockout weapon during the exact incidents where `/admin`
pause/reseed is needed; delay-on-failure holds a goroutine per request
and becomes its own DoS. Token entropy plus timeouts plus the private
network are the controls. Revisit only if the port's exposure model
changes.

### 2. Wrong, broken or compromised RPC provider

Defenses in code, in the order they fire:
- Network passphrase verified per endpoint at every connect/rotation
  (`internal/ingest/failover.go`); mismatch disqualifies the endpoint.
- Chain continuity by parent hash on contiguous resume AND on endpoint
  rotation (`internal/ingest/ingest.go`); a fallback serving a different
  chain is fatal, not tolerated.
- Failed transactions are skipped explicitly, resolving fee-bump
  wrappers to the inner result (`internal/indexer/indexer.go`) — facts
  are never derived from transactions the chain rejected.
- Panics from hostile or malformed XDR are recovered into the same
  fatal error path with ledger context and a stack trace, cursor
  untouched (`processLedger` recover frontier). Fail-stop, never
  fail-skip: a skipped ledger would be a silent gap.
- Removals require evidence: an empty `getLedgerEntries` answer never
  removes a batch wholesale — the plausibility guard suppresses
  implausible batches and counts them (`suppressed_removals` in
  `/status` and Prometheus).
- A stalled tip is detected by a watchdog and rotated away from; clamp
  gaps are recorded and persisted BEFORE adopting the clamped endpoint,
  and the process aborts if the evidence cannot be persisted
  (correctness over availability).

Accepted residual risk: a fully compromised provider serving an
internally consistent false chain from the first boot anchor. Recovery
authority: `indexer replay` against an independent archive endpoint plus
the consumer's own verification; prevention would need multi-provider
content cross-validation, which is out of scope (documented above).

### 3. Broker compromise / on-path network attacker

Defenses in code:
- Every published envelope carries `x-tw-sig` (HMAC-SHA256 over the
  exact body bytes, key shared with the consumer); the consumer verifies
  (`internal/sink/rabbitmq`). Forged or tampered bodies fail
  verification.
- Replayed or re-routed messages are absorbed by deterministic
  `message_id` dedupe and latest-wins ledger sequencing in the consumer.
- Boot refuses a set-but-short HMAC key (<32 bytes); boot warns loudly
  on plaintext `amqp://`.

Accepted residual risk: AMQP properties and routing keys are not signed
(body-internal identity makes tampering harmless); unsigned publishing
remains possible with an empty key until the consumer enforces —
rollout state, tracked by the hardening plan (W9: key rotation story).

### 4. Log reader

Defenses in code (`internal/config/string.go`): fields tagged secret
print `***`; URL userinfo passwords are redacted, including inside the
fallback-URL list; the heartbeat URL prints scheme://host only — its
path IS the secret that silences the dead-man's switch; endpoint hosts,
never full URLs, appear in runtime logs; validation errors state secret
LENGTHS, never values.

### 5. Operator error

The documented silent-loss modes and their state (CLAUDE.md holds the
authoritative list):
- `INDEXER_START_LEDGER=0` on a first boot starts at the tip and records
  no gap — set the start ledger explicitly on fresh state.
- A WASM hash missing from `ESCROW_APPROVED_WASM_HASHES` makes that
  contract version invisible (zero envelopes, zero logs).
- Deleting state while the process runs requires the atomic
  rm-and-kill procedure (ops runbook).
- Deleting only the cursor file is RECOVERED, not lossy: boot preserves
  the surviving watchlist (escrows, tombstones, gaps) with a warning; a
  corrupt orphan watchlist refuses boot.

## Declared operational ceilings (numbers, not surprises)

- **Reconciliation sweep is O(watchlist)**: one `getLedgerEntries`
  request (200 keys) per processed ledger at the tip. Full-pass latency
  ≈ N/200 ledgers: ~5 minutes at 10k escrows, ~50 minutes at 100k, ~8
  hours at 1M (4x on the first pass after boot, before keys are
  learned). Detection latency for silent removals grows linearly with
  the product. If the watchlist approaches 100k, revisit the budget or
  shard the product across indexers.
- **Publish throughput is confirm-bound**: publisher confirms are
  awaited per message (the A2 correctness fix), so throughput ≈ 1 /
  broker-RTT per stream — ~1k msg/s on same-network Railway, ~50/s at
  20ms RTT. Irrelevant at the tip; the ceiling shows during dense-range
  replays. The known mitigation (batched confirms with the cursor at
  the last contiguous confirmed message) is deliberate future work, not
  an oversight.
- **One process, vertical scaling only**: the flock enforces a single
  writer; there is no leader election or sharding. A restart is a
  pause, not a loss (at-least-once + cursor). A dead broker is a fatal
  exit by design — the supervisor restarts, and gap evidence plus the
  cursor make the outage recoverable, never silent.
- **Mainnet ledger weight**: `INDEXER_GET_LEDGERS_LIMIT=10` (~2.65MB per
  ledger); deep replays are hours, not minutes, and the retention clamp
  known-limit in CLAUDE.md applies.

## Review rule

This document states only what the code does TODAY, verified at the
date above. Aspirational statements are forbidden here — doc/code drift
is audit ammunition. When an auditor asks "what happens if X", the
answer should be a section of this file or a line of code it references;
if it is neither, the gap belongs in `docs/hardening-plan.md` as work,
not here as prose.
