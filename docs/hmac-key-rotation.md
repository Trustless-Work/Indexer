# HMAC key rotation

How to rotate `ENVELOPE_HMAC_KEY` — the key signing every published
envelope (`x-tw-sig` header) — without parking the whole stream in the
consumer's DLQ.

## Why the ORDER is the whole procedure

Publisher confirms come from the **broker**, not the consumer: if the
core API rejects signatures, the Indexer never notices — confirms keep
arriving, the cursor keeps advancing, and every envelope lands in the
DLQ. A rotation done in the wrong order while the core enforces
verification is therefore a full outage of the read-model pipeline that
looks perfectly healthy from the Indexer's side. Recoverable (replay +
DLQ redrive), but an incident. The order below makes that impossible.

## Key identity on the wire

Alongside `x-tw-sig`, every signed message carries `x-tw-sig-kid`: the
first 8 bytes of `SHA-256(key)`, lowercase hex (16 chars), where `key`
is the exact raw string of `ENVELOPE_HMAC_KEY` — same no-decoding rule
as the HMAC itself. The fingerprint reveals nothing about the key
(preimage resistance) and lets the consumer hold several keys at once
and pick the right one per message. Compute it for any key with:

```bash
printf '%s' "$ENVELOPE_HMAC_KEY" | shasum -a 256 | cut -c1-16
```

## Rotation procedure

Preconditions: the new key is ≥32 bytes (boot refuses shorter) and was
generated, not typed (e.g. `openssl rand -hex 32`).

1. **Core API first: ADD the new key to its verify-set** (keep the old
   one). Deploy. From this moment the consumer accepts messages signed
   with either key, selected by `x-tw-sig-kid` (fallback: try all keys
   in the set when the header is absent).
2. **Rotate the Indexer**: set the new `ENVELOPE_HMAC_KEY`, redeploy.
   New messages carry the new kid; confirm in the core's logs that
   verification still passes.
3. **Redrive anything parked**: messages in the DLQ signed with the old
   key still verify — the old key is still in the set. Redrive before
   step 4.
4. **Core API last: REMOVE the old key** from the verify-set. Deploy.
   The window closes.

Never run step 2 before step 1 while the core is in enforce mode. If it
happens anyway: the stream parks in the DLQ while the cursor advances —
fix the verify-set first, then redrive the DLQ; `indexer replay` covers
any range the redrive cannot.

## Consumer requirements (core-api mirror — implement there)

The core's verifier (`src/presentation/ingestion`) needs:

- A **verify-set**: an ordered list of accepted keys (today: one env
  var; the set can be `ENVELOPE_HMAC_KEYS` comma-separated or
  equivalent), each with its fingerprint precomputed at boot using the
  derivation above.
- **Selection by kid**: when `x-tw-sig-kid` is present, verify against
  the matching key only; unknown kid = verification failure (same
  handling as an invalid signature for the current mode).
- **Fallback without kid**: messages published before this change carry
  `x-tw-sig` but no kid — try every key in the set. This also means the
  kid header is additive: a consumer that ignores it entirely keeps
  working with a single key.
- The existing verification modes (off / log / enforce) are unchanged;
  the set and the kid only change WHICH key verifies a given message.
- Cross-repo test vectors: mirror the Indexer's
  (`internal/sink/rabbitmq/rabbitmq_test.go`) — same key, same body,
  same signature, same fingerprint on both sides.

## Enforce-mode rollout (one-time, after both sides ship)

1. Core in **log** mode; confirm zero verification failures over a
   representative window (include a replay).
2. Core switches to **enforce**.
3. Indexer sets `SINK_REQUIRE_SIGNING=true` so a future deploy that
   loses the env var refuses to boot instead of silently publishing
   unsigned into rejection. The flag's default stays false — flipping
   the default in a release would turn any deploy missing the key into
   an outage; setting it is an explicit per-environment decision made
   AFTER the core enforces.
