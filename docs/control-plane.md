# Control plane

Runtime operations on a running Indexer, without a redeploy: put an
escrow on the watchlist, re-publish one that looks stale downstream,
bulk-recover after state loss, stop the loop for a bounded window, or
force a full reconciliation pass.

## One door

Commands arrive through exactly one entry point: the authenticated
`/admin/*` HTTP surface on the health server, behind `ADMIN_TOKEN`
(bearer, compared in constant time). The health port must never be
public — the token is defence in depth behind the private network, not a
substitute for it.

Every endpoint only **validates and enqueues**. The ingest loop drains
the queue *between ledgers* and executes there, which is what keeps it
the single writer of the watchlist and the state file — the same
invariant the discovery pass and the state detector rely on. So `202
Accepted` means "queued", never "done"; the audit log line carries the
execution outcome.

| Command | Endpoint | What it does |
|---|---|---|
| `track_escrow` | `POST /admin/escrows` | Add to the watchlist (clearing any tombstone), then fetch and publish current state. Idempotent. |
| `refresh_escrow` | `POST /admin/escrows/{id}/refresh` | Re-fetch and re-publish an already tracked escrow. The "this looks stale downstream" answer. |
| `remove_escrow` | `DELETE /admin/escrows/{id}` | Tombstone: stops tracking, and discovery/seed cannot silently re-add it. |
| `reseed` | `POST /admin/reseed` | Bulk-add after state loss (≤5000 ids), then publish each one's state. |
| `pause` / `resume` | `POST /admin/pause`, `/admin/resume` | Halt processing for a bounded TTL (≤1h, auto-resume). Deliberately noisy: `/readyz` reports 503 and the heartbeat goes silent. |
| `reconcile` | `POST /admin/reconcile` | Restart the sweep from the top with the changed-since filter disarmed. |
| — | `GET /admin/registry` | The tracked and tombstoned sets. |

## Why there is no AMQP command channel

There used to be a second door: a consumer on the `stellar.commands`
exchange, so the core API could announce a freshly deployed escrow
instead of waiting for on-chain discovery. It was removed in the A1
audit fix (2026-07), for two reasons.

**It was unauthenticated.** `Command.Source` recorded which door a
command came through but was only ever read by a log line — nothing
branched on it. The four commands documented as "Admin-only"
(`remove_escrow`, `pause`, `resume`, `reconcile`) were therefore fully
reachable by anyone who could publish to the broker. With a single
shared AMQP credential, no TLS, and the default vhost, "anyone who could
publish" is a wider set than it sounds.

**It bought no measurable latency.** The justification on the core API
side was that announcing gets the escrow watched immediately, "instead
of whenever the next activity or sweep pass happens to notice it". That
premise does not hold, because *the deploy is itself activity*:

1. The deploy transaction creates the escrow's contract instance entry
   with an approved WASM hash, so the **discovery pass registers it**.
2. The same transaction emits `tw_init` **from the escrow**, so the
   **detection pass picks it up** — discovery runs first within a ledger
   precisely so this works (see `internal/indexer/indexer.go`).
3. The escrow lands in that ledger's active set, so its state is fetched
   and published stamped with that same ledger.

All of it inside the ledger the deploy landed in. The core API's own
attribution reconcile confirms the reading: it is gated on the `tw_init`
event, "right when the escrow first appears" — not on the announcement.

What the channel genuinely covered was narrower: an escrow whose WASM
hash is missing from `ESCROW_APPROVED_WASM_HASHES` (because `Track`
bypasses the hash check, unlike `Register`), and a deploy landing while
the indexer is far behind or inside a recorded gap. Both are better
served by the operator surface above, and the first is better served by
noticing the config drift in the first place — which is now an explicit
invariant:

> **Every factory WASM hash the API can deploy MUST be in
> `ESCROW_APPROVED_WASM_HASHES`.** Shipping a new contract version means
> adding its hash here, in the same release. This was always the design
> (identity by code, so a new version is a config change and not a code
> change), but the announcement used to paper over a missing entry —
> `Track` skips the hash check. Without it, an escrow whose hash is not
> approved is simply invisible: no discovery, no events, no state.

Checking it is a two-command diff — the API's factory hashes against the
Indexer's approved list:

```bash
railway variables --kv | grep WASM_HASH                       # the API's factories
railway variables --service Indexer --kv | grep ESCROW_APPROVED  # the approved set
```

Verified 2026-07-28: the four production factory hashes (single-release
v1/v2, multi-release v1/v2) are all present, alongside two older
versions.

## If automated reconciliation is needed later

This is a plausible future need — data lost along the way (a gap, a
state-file loss, a divergence) has to be reconciled somehow, and doing
it by hand does not scale forever. Two guidelines if that day comes:

**Pull, don't push.** Have the Indexer ask a source it authenticates,
on its own schedule, rather than accepting pushed commands from a
channel whose publishers it cannot verify. That inverts the trust
direction: the Indexer decides when to reconcile and against whom, and a
compromised broker credential no longer grants control over the
pipeline. It also covers the failure modes better — a fire-and-forget
announcement at deploy time cannot help with a gap detected a week
later, whereas a periodic reconcile can.

**Reconcile sets, not events.** The useful question is "which escrows
should I be watching that I am not?", answered against the full set. A
stream of individual commands re-creates the same delivery-guarantee
problems the event pipeline already solved once.

Whatever the mechanism, the invariant above is not negotiable: a new
entry point still only validates and enqueues, and the ingest loop still
executes between ledgers as the single writer.

## Recovering after state loss, today

Two steps, both operator-driven:

1. Export the watchlist from the core API's database
   (`scripts/export-escrow-seed.ts` in `trustlesswork-core-api`).
2. `POST /admin/reseed` with the resulting contract ids.
