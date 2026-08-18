---
name: ops-runbook
description: Operating a running Indexer without a redeploy — reseed after state loss, replay a ledger range, pause/resume, reconcile, inspect status, reset local state — and the actions that cause silent data loss. Use when the user asks to fix a gap, recover the watchlist, redo a range, check why escrows are missing, or run anything against the Railway indexer.
---

# Ops runbook

Everything here is operator-driven. The agent prepares the exact commands and explains
what they will do; the user runs anything that touches Railway or holds a token.
`docs/control-plane.md` is the design; `README.md` documents `/status` fields.

## First: read the numbers

`GET /status` on the health port (`:8080`, private) — cursor, ledger age, active
`rpc_endpoint`, pause state, escrows tracked, publish totals, recorded gaps,
`suppressed_removals`, uptime. On Railway:
`railway ssh -- "wget -qO- localhost:8080/status"` (ONE quoted string; `railway ssh`
flattens arguments and drops inner quoting — no `sh -c`, no nested quotes).

Interpretation:
- `suppressed_removals > 0` → an RPC endpoint answered empty; look at the endpoint,
  not at the escrows (audit A3 guard).
- ledger age growing with a healthy endpoint → check the core's queue depth; the
  publisher waits on confirms and backpressure is by design.
- gaps recorded → those ranges were skipped ON PURPOSE with evidence; backfill with
  replay. Ranges NOT recorded (see below) are the dangerous ones.

## Silent-loss actions — never do these casually

- First boot / new volume with `INDEXER_START_LEDGER=0`: starts at the tip, indexes
  nothing before, records no gap. Set the start ledger explicitly on any fresh state.
- A WASM hash missing from `ESCROW_APPROVED_WASM_HASHES`: that contract version is
  invisible (zero envelopes, zero logs). Verify the list against the deployed contract
  hashes before concluding "no activity".
- `rm` the state file while the process runs: the loop rewrites it every ledger. Stop
  and remove atomically: `railway ssh -- "rm -f /var/lib/indexer/<state> && kill -TERM 1"`.
- Local: `make reset-state` wipes the docker state volume; `make run` and `make up`
  both bind :8080.

## Procedures

**Recover the watchlist after state loss** (control-plane.md "Recovering after state loss"):
1. In core-api: `scripts/export-escrow-seed.ts` → contract ids currently in the read-model.
2. `POST /admin/reseed` (bearer `ADMIN_TOKEN`) with those ids, or set `ESCROW_SEED_PATH`
   to an uploaded file for boot-time seeding.
3. The reconciliation sweep's next pass fetches state for all of them; confirm in
   `/status` (escrows tracked) and in the core's read-model.

**Backfill a range** (recorded gap, DLQ loss on the core side, or a re-project):
`bin/ingest replay --from N --to M` — runs NEXT TO the live indexer, persists nothing,
takes no lock, no heartbeat, events/deposits only (no state snapshots). It refuses ranges
no configured endpoint serves in full; today the check uses `getHealth.OldestLedger`
(known limit — some providers serve more via `getLedgers`). Point `RPC_FALLBACK_URLS` at
an archive provider for old history. Downstream dedupe absorbs duplicates.

**Track / remove / refresh one escrow:** `/admin/escrows` (POST/DELETE) — validates and
enqueues; `202` means queued, the loop executes between ledgers.

**Pause / resume:** `/admin/pause` (TTL-bounded, always expires) and `/admin/resume`.

**Force a reconciliation pass:** `POST /admin/reconcile`. Reconcile SETS (what should be
tracked vs what is), never replay events through the control plane.

**Never:** propose a broker-based command channel (removed, audit A1), expose the health
port publicly, or run two live indexers against the same state volume.

## Done when

The user has run the commands, `/status` reflects the expected change, and — if the goal
was data in the read-model — the core's tables show it (eventually consistent, ~1–2 s).
