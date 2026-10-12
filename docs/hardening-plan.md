# Audit-hardening plan

Status: **in progress** · Created: 2026-10-12 · Source: full internal audit of this
repo + cross-comparison with the Sierpe indexer + a second-order risk simulation of
every proposed fix. This document is the single source of truth for the hardening
work: it records not only WHAT to implement but the exact design that survived the
simulation, and the **anti-designs** — implementations that look equivalent but make
the system worse. Follow it literally; when a design here conflicts with an
"obvious" simpler implementation, the design here wins because the simpler one was
already analyzed and rejected for a stated reason.

## How to execute this plan

- **One PR per work item**, branched from `main`. Never bundle items: a
  "security hardening" mega-PR is unreviewable, and an unreviewable PR is itself
  the most likely vector for introducing a vulnerability.
- **Order matters.** The dependency chain is: W1 → W2, and W3 before any item
  that touches the ingest loop (W6, W7). W4/W9 need the pre-flight checks below.
- Tick the checkboxes of an item **in the same PR** that implements it.
- Definition of done per CLAUDE.md: `gofmt -l .` prints nothing, `go vet ./...`,
  `CGO_ENABLED=0 go build ./...`, `go test ./...` green (run `-race` locally),
  `CHANGELOG.md` `[Unreleased]` entry for anything operational.
- **Wire contract**: only W9 changes what the core API sees on the broker
  (additive header). W4 may force a coordinated key rotation (see pre-flight).
  Anything else must leave envelope shape, routing keys, exchange and
  `message_id`s untouched — if an item seems to require otherwise, stop and
  re-read `.claude/skills/envelope-contract-change`.
- The pending `security/envelope-hmac-signing` branch (commit `adef38b`) merges
  first; W4 and W9 build on it.

## Pre-flight verifications (manual, against Railway — record results in the PR)

- [ ] `ADMIN_TOKEN` currently set in prod is ≥16 chars (else W4 deploys will crash-loop until it is rotated).
- [ ] `RABBITMQ_URL` scheme in prod: `amqp://` or `amqps://` (decides whether W4's warning fires on day one). Evidence so far: `docs/railway-deployment.md` documents `amqp://...@RAILWAY_PRIVATE_DOMAIN:5672/` — expect the warning to fire; confirm against the live service variables.
- [ ] `ENVELOPE_HMAC_KEY` is ≥32 bytes **in both repos** (else W4 forces a lockstep key rotation with core-api — plan it as a W9-style rotation, never a unilateral change).

## Status tracker

| Item | Title | Status | PR |
|------|-------|--------|----|
| W1 | Dead code removal & truthful metadata | ✅ | `chore/w1-dead-code-and-metadata` |
| W2 | CI parity (race, staticcheck, gosec, govulncheck) | ✅ | `ci/w2-parity` |
| W3 | Loop invariant test (publish-fails ⇒ no Save) | ✅ | `test/w3-loop-invariant` |
| W4 | Secret & transport validation | ✅ | `security/w4-secret-validation` |
| W5 | HTTP server timeouts | ✅ | folded into `ci/w2-parity` (see W5 note) |
| W6 | State Load: orphan-watchlist merge | ✅ | `fix/w6-orphan-watchlist` |
| W7 | Chain-data robustness (recover, tx success, strkey) | ✅ | `security/w7-chain-data-robustness` |
| W8 | Threat model document | ✅ | `docs/w8-threat-model` |
| W9 | HMAC key rotation (cross-repo) | ☐ | |

---

## W1 — Dead code removal & truthful metadata

**Finding:** dead code and stale metadata are what an auditor uses to argue the
process is unreliable; the unused `datastore` stub also makes transitive
`aws-sdk-go-v2` govulncheck findings reachable.

**Design:**
- Delete the `datastore` ledger backend stub: the `"datastore"` case in
  `internal/ingest/ledger_backend.go` (returns "not implemented"), the
  `validLedgerBackendTypes` entry in `internal/config/validate.go:12-15`, and the
  `internal/sink`-unrelated `datastore` references in config docs. Update the
  CLAUDE.md "Known limits" paragraph that mentions it (otherwise this PR creates
  new doc/code drift — the exact defect this plan exists to remove).
- Delete dead code in `internal/utils/`: `sql.go`, `http_client.go`,
  `GetAccountLedgerKey`, `GetTrustlineLedgerKey`, `GetContractDataEntryLedgerKey`,
  `SanitizeUTF8`, `IsEmpty`, `UnwrapInterfaceToPointer`, `PointOf`,
  `IsValidStellarAddress`. **KEEP `DeferredClose` (used) and `IsContractAddress`
  (`utils.go:186`) — W7 depends on it.**
- Remove `Config.StrictMode` (`internal/config/config.go:41`): parsed, printed,
  read by nothing. `env/v11` ignores undeclared env vars, so deployments that set
  `STRICT_MODE` are unaffected; note the removal in CHANGELOG anyway.
- Fix the version plumbing: the Dockerfile `-X` ldflag targets
  `github.com/Trustless-Work/Indexer/internal/ingest.Version`, which does not
  exist (silently ignored). Add `var Version = "dev"` at that exact path, expose
  it as a new additive `version` field in `/status`, and fix the Dockerfile
  comment that falsely claims `/status` reports it. Smoke-verify with a local
  `docker build` that the baked binary reports the value.
- Fix stale docs: the package doc at `internal/ingest/ingest.go:1-16` still says
  "Delivery to a sink is intentionally NOT wired yet" (it is the heart of the
  service); the stale "Status" opening block of `docs/event-schema.md`.
- Add `indexer.state.watchlist.json` (and its `.tmp`) to `.gitignore`.

**Checklist:**
- [x] datastore stub + enum + docs removed, CLAUDE.md known-limits updated
- [x] utils pruned (DeferredClose + IsContractAddress kept)
- [x] StrictMode removed + CHANGELOG note (also purged from .env.example and the Railway guide)
- [x] `Version` var exists at the ldflag path; `/status` returns it; Dockerfile comment now true as written; stamp smoke-checked by building with the Dockerfile's exact ldflags and finding the value in the binary
- [x] ingest.go package doc + event-schema.md Status block corrected (CLAUDE.md's "stale status" note removed accordingly)
- [x] .gitignore covers watchlist files
- [x] grep shows no remaining references to anything deleted

## W2 — CI parity

**Finding:** CI runs only fmt/vet/build/test. An auditor will run gosec and
staticcheck themselves; every hit then looks "uncontrolled". Running them first
and justifying the excludes converts findings into non-findings.

**Design:** extend `.github/workflows/ci.yml` with `go test -race ./...`,
staticcheck, gosec and govulncheck (unpinned `@latest` — deliberate, so CI fails
on a stale toolchain).

**Anti-design (ordering):** landing this BEFORE W1 makes govulncheck flag
`aws-sdk-go-v2` vulns through the unreachable datastore stub → red CI on
unrelated PRs → pressure for blanket excludes → alert fatigue that normalizes
red CI. **W1 must merge first.**

**Excludes policy:** centralized in the CI config with a dated justification
comment per exclude — never inline `#nosec` scattered through the code. Expected
excludes: G104 (deliberate `_ =` patterns), G115 (uint32 ledger-sequence
arithmetic; a per-site review is deferred work, mirror the Sierpe comment
convention), G304 (`STATE_PATH` is operator-controlled by design).

**Checklist:**
- [x] W1 merged first
- [x] `-race` in CI
- [x] staticcheck green
- [x] gosec green with centralized, dated, justified excludes only (G115, G118, G301, G302, G304 — reasons in ci.yml; G112 was real and got FIXED instead, see W5)
- [x] govulncheck green — required upgrading deps to fixed versions (amqp091-go 1.11→1.13, x/net 0.47→0.60, grpc 1.64→1.83, otel 1.45, aws eventstream/s3), which pulled the module to Go 1.26 (Dockerfile + README updated). 19 reachable vulnerabilities cleared; the 5 remaining are in unreachable code and do not fail the scan.

## W3 — Loop invariant test (publish-fails ⇒ no Save)

**Finding:** `Ingest()` (~450 lines, `internal/ingest/ingest.go`) owns the
central invariant — the cursor never advances past an unpublished fact — and has
zero direct tests. The invariant is currently enforced only by review.

**Design — black-box, NO refactor:** the seams already exist. Build the test
from: a fake ledger backend (the interface in `internal/ingest/ledger_backend.go`),
a fake `sink.Sink` that records publishes and fails on the Nth call, a bounded
run (`INDEXER_END_LEDGER`), and a temp `STATE_PATH`. Assert:
1. When a publish fails, the cursor file is absent / still at the previous value.
2. Happy path: cursor equals the end ledger after a bounded run.
3. Publish order per ledger: events → states → sweep states → save (observable
   from the fake sink's recording plus state-file reads between ledgers).

**Anti-design:** extracting the per-ledger body of `Ingest()` to "make it
testable" first. The loop owns ALL the invariants; a botched extraction can
reorder publish/Save or drop an error-path `return`, introducing during the
refactor exactly the bug class the test exists to prevent. Characterization test
first, against the untouched code. Extract helpers only if a branch proves
unreachable, in its own later PR, with this test already green on both sides.

**Execution note (2026-10-12):** sink/pool injection proved genuinely unreachable
black-box (Ingest builds both from cfg; the sink factory only knows noop and
rabbitmq, and a noop sink cannot fail). Per this item's escape hatch, the minimal
concession was two package-level construction seams (`var newPool = newRPCPool`,
`var newSink = sinkfactory.New`) and the two call-site identifier swaps inside
Ingest — the loop BODY is untouched and the seams are inert in production. The
tests reuse the existing `fakeRPC`/`fakeBackend`/`testPool` fixtures from
failover_test.go and hand-built V1 LedgerCloseMeta XDR carrying one contract
event from a seeded escrow (ledgers close in the past so the run is catching-up:
no state fetches, no sweep — one publishable fact).

**Checklist:**
- [x] Test exists against the unrefactored loop (body untouched; two inert construction seams, see note)
- [x] Locally verified that an injected Save-before-publish mutation makes all three tests fail with the right messages; mutation reverted, not committed
- [x] Publish-order assertion included (the sink's publish callback observes that the cursor file does not exist yet at publish time)
- [x] Invariant pinned on both save triggers: mid-range publish failure and final-ledger (isFinal) publish failure leave no state file

## W4 — Secret & transport validation

**Finding:** `ADMIN_TOKEN` has zero validation (a 1-char token boots);
`ENVELOPE_HMAC_KEY` has no minimum; `HEALTH_HEARTBEAT_URL` prints in full at
boot (`config.go:192` has no secret tag, and heartbeat providers put the secret
in the URL *path*, which `string.go` does not redact); nothing warns on a
plaintext `amqp://` broker URL.

**Design (each point is load-bearing):**
- `ADMIN_TOKEN`: valid iff **empty OR length ≥16**. Empty is deliberate
  semantics — it disables the admin surface entirely (unmounted at
  `ingest.go:295`). Rejecting empty would force a token to exist where none is
  needed, *enlarging* the attack surface.
- `ENVELOPE_HMAC_KEY`: valid iff **empty OR ≥32 bytes** (the key is fed to HMAC
  as-is; bytes = string length). Empty = unsigned mode, the deliberate rollout
  path — preserve it.
- `HeartbeatURL`: redact the **path only** — print `scheme://host`. Never render
  the whole value as `***`: a typo'd URL would become undiagnosable from logs and
  the dead-man's switch would silently never fire. Implement in
  `internal/config/string.go` (a path-redacting variant next to
  `redactURLPassword`).
- RabbitMQ URL: **loud boot WARNING** when the scheme is `amqp://`. Never a hard
  error: Railway private networking may legitimately terminate without TLS, and
  forcing `amqps` invites the worse outcome (self-signed cert +
  `InsecureSkipVerify` to "make it work").

**Anti-designs rejected by simulation:** naive `len < 16` (kills the empty-token
semantics); hard `amqps` requirement (outage or InsecureSkipVerify); whole-value
heartbeat redaction (undiagnosable dead-man's switch); merging without the
pre-flight checks (crash-loop in prod caused by the fix itself).

**Checklist:**
- [ ] Pre-flight checks done and recorded in the PR (token length, broker scheme, HMAC key length both repos) — MANUAL: the operator verifies against Railway before merging; commands are in the PR description
- [x] empty-OR-≥16 / empty-OR-≥32 validations with boot-refusal tests for short-but-nonempty and pass tests for empty, plus a test that validation errors never echo the secret value
- [x] Heartbeat URL renders host, hides path (`secret:"path"` tag); malformed values hide entirely; tests on the config dump
- [x] amqp:// warning at sink construction (warning, never an error — the simulation's InsecureSkipVerify rationale is in the code comment)
- [x] CHANGELOG migration note (operators with short secrets must rotate before deploying)

## W5 — HTTP server timeouts

**Finding (new, from the simulation):** `internal/health/server.go:71` builds
`http.Server` with **no ReadHeaderTimeout/ReadTimeout/WriteTimeout/IdleTimeout**
→ slowloris on the health/admin port today.

**Design:** set all four timeouts (conservative values; health endpoints respond
in microseconds, admin bodies are capped at 1MB — single-digit seconds are fine).

**Deliberate NON-implementation — admin rate limiter:** analyzed and rejected.
Per-IP limiting behind Railway's proxy is ineffective (spoofable
`X-Forwarded-For`, shared proxy IP); a global limiter is an operator-lockout
weapon during the exact incidents where `/admin` pause/reseed is needed;
sleep-on-failure holds a goroutine per request and `http.Server` does not cap
connections (goroutine exhaustion). The compensating controls are W4's token
entropy + the never-public doctrine. Record this decision in W8's threat model.
If one is ever added anyway: global token bucket over `/admin` 401s only,
generous burst, never touching health routes (a mis-scoped limiter 429s
`/healthz` and Railway restarts the service).

**Execution note (2026-10-12):** folded into W2's PR instead of its own. Reason:
gosec (added by W2) flags the missing timeouts as G112, and the only alternatives
were shipping a linter config that excludes a KNOWN-REAL finding (the exact
alert-fatigue anti-pattern this plan prohibits) or an extra one-commit PR cycle
for a 10-line fix. The one-PR-per-item rule exists for reviewability; +10 lines
does not threaten it.

**Checklist:**
- [x] Four timeouts set on the server (ReadHeader 5s, Read 10s, Write 10s, Idle 60s — largest legitimate exchange is the 1MB-capped admin body)
- [x] Health/readiness probes verified unaffected (full test suite green after the change)
- [x] Rate-limiter decision referenced in W8 (threat-model.md, actor 1)

## W6 — State Load: orphan-watchlist merge

**Finding:** deleting only the cursor file while the watchlist file survives
(plausible "reset to tip" operator move) makes `FileStore.Load`
(`internal/state/file_store.go:112-118`) return `ErrStateNotFound` **without
reading the watchlist** → fresh boot → the first `Save` overwrites the watchlist,
silently destroying tombstones and gap evidence.

**Design — load/merge, NEVER refuse:** when the cursor file is missing and a
watchlist file exists, **load it** (escrows, tombstones, gaps), return zero
cursor, and log a loud warning. Re-tracking stale escrows is safe: the sweep
republishes their state and the consumer's dedupe/latest-wins absorbs it
(at-least-once makes loading too much safe and loading too little unsafe).

**Anti-design — refuse boot (the "obvious" fix, rejected):** `Save`
(`file_store.go:154-186`) writes **watchlist first, cursor second**. A crash
between the two writes on the first-ever Save legitimately produces
"watchlist exists, cursor missing". Today that reboot is benign (cursor never
persisted ⇒ full reprocess ⇒ same discoveries). Refusing boot converts that
benign crash window into a **permanent crash-loop** requiring manual state
surgery. Refuse-boot bricks deployments; merge recovers both cases.

**Also update:** the silent-loss list in CLAUDE.md and the
`.claude/skills/ops-runbook` skill (the mode changes from "destroys evidence
silently" to "recovered with a warning").

**Implementation note (2026-10-12):** `Load` returns the recovered contents
ALONGSIDE `ErrStateNotFound` (data + sentinel), so the cursor-missing semantics
stay intact for start-ledger resolution: a recovered boot must start like a fresh
one (configured start ledger), NOT resume from ledger 1 with a giant clamp gap.
The caller's `ErrStateNotFound` branch preserves the recovered escrows,
tombstones and gaps, checks the network stamp, and seeds the registry from the
watchlist instead of the seed file.

**Checklist:**
- [x] Orphan watchlist → loaded + warning (state-level tests plus a loop-level test running the real `Ingest()` against the incident shape: gap republished, event detected from a recovered escrow, tombstone survives the run, seed file does NOT override recovery)
- [x] No files → fresh start (test, unchanged behavior)
- [x] Both files → merged exactly as today (existing round-trip tests unchanged and green)
- [x] Corrupt orphan watchlist → `ErrStateCorrupted` (test)
- [x] CLAUDE.md + ops-runbook updated in the same PR (cursor-only delete is now documented as recovered; a FULL reset must remove the watchlist file too)
- [x] Incident regression pinned: the save-after-recovery that used to destroy the evidence now preserves it (test)

## W7 — Chain-data robustness

Land only after W3's test exists. Three sub-changes, one PR, each with a trap:

**7a. Per-ledger recover → fatal with context.** A deferred `recover()` around
per-ledger processing that converts an SDK panic (e.g. nil-union XDR getters,
`Operations[opIndex]` indexing on meta V4) into an error carrying ledger seq +
tx context, routed to the **existing fatal path**. The benefit is diagnostics
only — the crash-loop behavior is intentionally preserved.
**Anti-designs:** recover-and-skip creates a silent gap (violates the purpose of
the entire service — strictly worse than crashing); recover-and-retry-as-
transient creates an invisible infinite loop on a poison ledger with the process
looking alive. Additionally, after a panic the in-memory registry/sweeper state
is half-mutated — one more reason the only safe continuation is process exit.
The cursor-never-advances property already makes panics fail-stop, never lossy.

**7b. Explicit transaction-success check.** Skip failed transactions before
deriving any fact. Today the SDK (v0.7.3) already excludes failed-tx events
implicitly, so **no observable change in published facts is expected** — any
golden-test diff means the check is wrong.
**The trap is fee-bump:** reading the OUTER wrapper's result instead of the
inner transaction's discards successful transactions ⇒ lost escrow events ⇒ the
exact silent loss the check is meant to prevent. Use the SDK helper that
resolves fee-bump success (mirror Sierpe `internal/extract/extract.go:127-130`).
A fee-bump fixture (successful inner tx must still produce events) is
**mandatory** in the same PR.

**7c. strkey validation at the admin edge.** Validate contract ids in
`internal/commands/command.go:100-126` with `utils.IsContractAddress`
(C-strkey, kept alive by W1) — **not** `IsValidStellarAddress` (G-addresses;
would reject every contract id and brick the admin surface). Return 400.
Validate **only** at the admin edge: extending it to the persisted
watchlist/seed load would refuse boot on historical data. Also add a warn log at
the silent key-build drop in
`internal/indexer/processors/escrow_state.go:235` (today an invalid tracked id
is skipped silently forever).

**Execution notes (2026-10-12):** the recover frontier lives in `processLedger`
(already a separate function — the loop body stays untouched) and logs the stack
trace before returning through the existing fatal path. The panic fixture is
hostile event XDR (a Symbol topic with a nil union arm), which panics inside the
detector — a real chain-data shape, not a synthetic `panic()` call. One scope
decision beyond the letter of the plan: `remove` stays strkey-LENIENT, because it
is the only cleanup path for malformed ids accepted before this validation
existed — strictness there would make legacy garbage permanent.

**Checklist:**
- [x] W3 merged first
- [x] Panic-injection test reaches the fatal path with ledger context in the error, stack trace logged, cursor untouched (hostile-XDR fixture, not a synthetic panic)
- [x] No recover-and-continue anywhere (the frontier converts to the existing fatal error path only)
- [x] Success check uses `LedgerTransaction.Successful()` (verified in SDK source: TxSuccess OR TxFeeBumpInnerSuccess); fee-bump fixture green; failed-tx-with-events fixture publishes zero facts; existing fixtures unchanged
- [x] Admin track/refresh/reseed reject malformed ids with 400; valid C-address accepted; `remove` lenient for legacy cleanup (tested both ways); seed-file boot path unaffected (validation is admin-edge only)
- [x] queryEntries drop now logs, once per id per process (no sweep-pass spam)

## W8 — Threat model document

**Finding:** scope decisions that are not written down read as omissions to an
auditor. A ceiling documented with numbers is a decision; a ceiling the auditor
discovers is a finding.

**Design:** `docs/threat-model.md` covering:
- Trust assumptions: RPC is honest-but-fallible (not malicious); the health/admin
  port is never public; the broker is reached over private networking.
- Actors and defenses: external attacker on the health port (token entropy W4,
  timeouts W5, never-public doctrine, no rate limiter + why — W5 decision);
  malicious/compromised RPC (passphrase check, parent-hash continuity, removal
  plausibility guard, fail-stop panics; explicitly OUT of scope: multi-provider
  cross-validation — recovery authority is replay against archive); broker
  compromise (HMAC signing, dedupe, latest-wins); log readers (redaction
  inventory); operator error (silent-loss modes list + W6 recovery).
- Declared ceilings with numbers: sweep reconciliation is O(N) at 200 keys per
  ledger (10k escrows ≈ 5 min/pass, 100k ≈ 50 min); publish throughput is serial
  confirm-per-message = 1/RTT to the broker (the A2 correctness trade);
  single-process, vertical scaling, flock single-writer by design.
- **Rule:** every claim verified against code at the time of writing. No
  aspirational statements — doc/code drift is audit ammunition (it is the main
  defect found in the sibling project).

**Checklist:**
- [x] Document written, every claim code-verified (mechanisms cited by package; ceilings with numbers: sweep N/200 ledgers per pass, publish 1/RTT, single-writer flock, mainnet ledger weight)
- [x] W5 rate-limiter decision recorded (deliberate non-implementation with the lockout/spoofing/goroutine-exhaustion rationale)
- [x] Linked from README (Further reading) and CLAUDE.md (with the keep-in-same-PR rule)
- [x] Review rule written into the document itself: no aspirational statements; doc changes ride the PR that changes a cited mechanism

## W9 — HMAC key rotation (CROSS-REPO)

**Finding:** single key, no key id on the wire, no rotation procedure. Rotation
under consumer-side enforcement is an incident waiting to happen: **broker
confirms ≠ consumer verification** — if core enforces and the rotation order is
wrong, every envelope lands in the core DLQ while the indexer's cursor advances
normally; the indexer never notices. Recoverable (replay + redrive) but a full
incident.

**Design — in this order:**
1. **Rotation runbook first** (`docs/` here, mirrored in core-api): (a) add the
   new key to core's verify-set (dual-key window) → (b) rotate
   `ENVELOPE_HMAC_KEY` on the indexer → (c) remove the old key from core.
2. `x-tw-sig-kid` AMQP header: truncated SHA-256 fingerprint of the key (first
   8 bytes, hex). Additive — verifiers that ignore it are unaffected; a
   fingerprint leaks no key material. Follow
   `.claude/skills/envelope-contract-change`; mirror PR in
   `trustlesswork-core-api` (verify-set selected by kid, fallback try-all).
   Update `docs/event-schema.md` and the cross-repo test vectors on both sides.
3. `SINK_REQUIRE_SIGNING` flag (boot-refuse without a key), **default false**.
   It flips only after core enforces; changing the default in a release is an
   outage for any deploy missing the env var.

**Checklist:**
- [ ] Runbook written and reviewed (both repos)
- [ ] kid header emitted; cross-repo vectors green on both sides
- [ ] core-api mirror PR: dual-key verify by kid
- [ ] require-signing flag, default false, covered by test
- [ ] event-schema.md updated

---

## Phase 2 — confirmed findings, deliberately deferred (do not lose these)

Not part of the approved package; each is real and should become its own item
when picked up. Recorded here so nothing silently drops out of scope:

- [ ] **Replay/bounded-mode snapshot gating** (`internal/ingest/catchup.go:17`):
  the historic-stamping suppression is keyed only on ledger age (20 min), not on
  replay mode. A `replay` of a range closed <20 min ago fetches tip state and
  publishes snapshots stamped with historic ledgers — contradicting
  `cmd/ingest.go:60-66` and the README ("bounded run suppresses state snapshots
  by design"). Fix: gate on `cfg.Replay`/`EndLedger != 0` **in addition to** the
  age check; decide the <20-min live-resume case explicitly and document it.
- [ ] **Removal-guard minimum batch**
  (`internal/indexer/processors/escrow_state.go:57`, currently 10): one empty
  RPC answer for a 5–9-escrow batch publishes them all as false "removed"
  (self-heals next sweep pass). Lowering to ~3–4 raises false-suppression of
  legitimate mass removals — acceptable: suppression alerts
  (`suppressed_removals`) and reconciles; a false "removed" is the unforgivable
  direction.
- [ ] **Retention wall by real `getLedgers` behavior** (replace the
  `getHealth OldestLedger` clamp in `internal/ingest/failover.go:206-224` with a
  findWall-style binary search) — fixes the documented known limit where
  `replay` refuses ranges the endpoint actually serves. Sierpe's
  `ingest/backfill.go` has a working reference implementation.
- [ ] **Publish pipelining** (batched confirms, cursor at the last contiguous
  confirmed message) — only if replay throughput ever matters; today's serial
  confirm-per-message is a deliberate correctness trade with a 1/RTT ceiling.
- [ ] **logrus → slog** migration (logrus is in maintenance mode; auditors list
  it reflexively).
- [ ] **Tombstone retention policy**: the removed-escrow set grows monotonically
  for the lifetime of the product; define a bound or an archival rule.
- [ ] **G115 per-site review** (uint32 narrowing), lifting the blanket gosec
  exclude from W2.
