# Research: Strategies for Securely Forwarding Indexed Data to External REST API

**Issue:** #6  
**Author:** Research spike  
**Date:** 2026-02-28  
**Status:** Draft

---

This is a short spike to figure out how the Go indexer should push escrow events into the existing internal API in a way that is **secure**, **reliable**, and not over-engineered for the traffic we actually expect.

The goal here is to narrow things down so that the follow‑up implementation task is mostly wiring and polish, not re‑doing design work.

## 1. Data surface — what `escrow.go` actually emits

### 1.1 Where the data comes from

At a high level, `EscrowProcessor.ProcessTransaction` in `internal/indexer/processors/contracts/escrow.go` is called once per **transaction operation** and can return zero or more `entities.Escrow` values.

Under the hood it:

- Is invoked per operation via `ProcessTransaction(ctx context.Context, op *processors.TransactionOperationWrapper) ([]entities.Escrow, error)`.
- Filters to `InvokeHostFunction` operations, then to host functions of type `HostFunctionTypeInvokeContract`.
- Further narrows to two function names:
  - `tw_new_single_release_escrow`
  - `tw_new_multi_release_escrow`
- For those functions it calls:
  - `ParseSingleReleaseEscrowArgs(...)`
  - `ParseMultiReleaseEscrowArgs(...)`
- Both parsers end up constructing an `entities.Escrow` value and returning `[]entities.Escrow{*escrow}`.

The processor also logs some of the fields via the structured logger, mostly for debugging/visibility:

- For single release:
  - Contract ID, deployer, factory contract, title, description, amount, engagement ID, service provider, receiver, first milestone description.
- For multi release:
  - Contract ID, deployer, factory contract, title, description, engagement ID, service provider, milestone count, and each milestone (description, amount, receiver).

The important bit for this spike is that **our actual data surface is the returned `[]entities.Escrow`, not the log lines**. Logs are just a side‑channel and we should not depend on them.

### 1.2 Data structure (`entities.Escrow`)

`internal/entities/escrow.go` defines:

- **Escrow**
  - `ContractID string`
  - `EscrowType EscrowType` (`single_release` or `multi_release`)
  - `Deployer string`
  - `FactoryContract string`
  - `DeployerSalt string`
  - `WasmHash string`
  - `InitFunction string`
  - `Amount uint64` (only for single-release at escrow level)
  - `Description string`
  - `EngagementID string`
  - `Title string`
  - `PlatformFee uint32`
  - `ReceiverMemo string`
  - `Flags EscrowFlags` (state flags)
  - `Roles EscrowRoles` (service provider, receiver, approver, etc.)
  - `Milestones []Milestone`
  - `TrustlineAddress string`

- **EscrowFlags**
  - `Approved bool`
  - `Disputed bool`
  - `Released bool`
  - `Resolved bool`

  For **creation** events these flags will typically all be `false`. They become more relevant for future lifecycle events (releases, disputes, resolutions), which are out of scope for this spike but good to keep in mind for a more generic “escrow event” pipeline later.

- **EscrowRoles**
  - `ServiceProvider string`
  - `Receiver string`
  - `Approver string`
  - `ReleaseSigner string`
  - `DisputeResolver string`
  - `PlatformAddress string`

- **Milestone**
  - `Description string`
  - `Status string`
  - `Approved bool`
  - `Evidence string`
  - `Amount uint64`
  - `Flags *EscrowFlags`
  - `Receiver string`

This is already a good shape for a JSON payload to an internal REST API.

### 1.3 Event characteristics

- **Event-driven, not polled**:
  - `ProcessTransaction` is called as part of the indexer’s transaction-processing pipeline.
  - Emissions are driven by on-ledger contract invocation events, not by a timer or poller.
- **Frequency & volume**:
  - Each relevant operation yields **at most one `Escrow` per contract creation**.
  - Actual volume depends on:
    - Overall network transaction rate.
    - Fraction of transactions calling `tw_new_*_escrow` on the factory contract(s).
  - For now, we should assume **bursty but generally moderate throughput**, with the possibility of spikes if many escrows are opened in a short time.
- **Shape of data to forward**:
  - Each “event” is a fully-populated `entities.Escrow` snapshot at creation time.
  - Additional lifecycle changes (releases, disputes, etc.) are **not yet handled here**; this spike focuses purely on initial creation.

Because the indexer already returns `[]entities.Escrow` from the processor, the forwarding layer should hook into this **structured output** rather than inspecting or parsing the logs.

## 2. Transport mechanisms

### 2.1 Options considered

| Mechanism                      | Pattern                              | Pros                                                                                         | Cons / Risks                                                                                                  | Fit for current use case                                                                 |
|--------------------------------|--------------------------------------|----------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------|
| HTTP/REST (per-event)         | Synchronous, 1 HTTP call per escrow | Simple; uses existing internal API; easy to secure with TLS + auth; well-supported in Go     | Higher overhead per event; backpressure handling must be explicit; risk of blocking indexer if not decoupled | Good baseline; with buffering/backpressure it is usually sufficient                      |
| HTTP/REST (batched)           | Synchronous, batch POST of N escrows| Amortizes HTTP overhead; allows more efficient retries and backoff; API side can bulk-insert | Requires API contract for batches; more complex failure semantics (partial failures)                          | Strong candidate if volume is moderate-to-high and API team can support batch endpoints   |
| Message queue / broker (NATS, RabbitMQ, Kafka) | Asynchronous, fire-and-forget or ack-based | Decouples indexer from persistence; strong durability & at-least-once/ordered guarantees     | Requires deploying and operating broker infra; more moving parts; persistence happens in a separate service   | Great if multiple consumers or very high volume; heavier than needed for a single API     |
| gRPC                           | Synchronous RPC                      | Strong typing; streaming support; good tooling & observability                               | Requires gRPC server changes on API side; more complex than plain REST for simple event ingestion             | Good if API evolves towards microservices with gRPC; may be overkill for this first step |
| WebSocket                      | Bidirectional, long-lived connection| Lower per-message overhead; near-real-time push                                              | Requires connection management and reconnection logic; harder to operate and secure than plain HTTP           | Best for ultra low-latency streaming; adds complexity not strictly needed here            |

### 2.2 Observations

- The internal API **already exists**; it is most likely HTTP-based.
- Indexer events are **append-only creations** with no need for ultra-low-latency bi-directional communication.
- Reliability and idempotence (avoiding duplicate escrows) matter more than raw throughput.

Given these constraints, **HTTP/REST over TLS with optional batching and local buffering** is the most suitable primary approach. A message broker could be re-evaluated later if:

- Multiple downstream consumers emerge (analytics, audit, monitoring).
- Throughput becomes too high for a simple HTTP ingestion path.

## 3. Security considerations per transport

### 3.1 Authentication

Common patterns:

- **API key (static secret)**:
  - Simple to implement (\`Authorization: Bearer <token>\` or custom header).
  - Rotation requires coordination but is straightforward.
- **JWT / OIDC-issued tokens**:
  - Stronger story for identity and expiration.
  - Indexer either:
    - Uses a long-lived client credential to obtain short-lived JWTs.
    - Is issued a service account token by the platform (Kubernetes, etc.).
- **mTLS (mutual TLS)**:
  - Client and server authenticate each other via X.509 certificates.
  - Very strong for internal zero-trust networks; no bearer token to steal in transit.
  - More operationally complex (CA, cert rotation).

For an internal REST API, a pragmatic **first step** is:

- **Bearer token or signed JWT** for app-level auth.
- Optionally upgraded to **mTLS** if infra supports it (service mesh, SPIFFE, or internal PKI).

### 3.2 Transport encryption

- All traffic should use **TLS** (`https://`).
- In Go, use the standard `net/http` client with:
  - Reasonable timeouts (per-request and idle-connection).
  - System roots or a configured root CA bundle (for internal PKI).

### 3.3 Secret management from Go

Options:

- **Environment variables** (baseline):
  - Tokens / credentials are injected by the runtime environment (Kubernetes secrets, systemd, etc.).
  - The Go service reads them via `os.Getenv`.
- **Vault / Secrets Manager**:
  - Use dedicated libraries/SDKs to fetch secrets at startup or on rotation.
  - The repo already carries indirect AWS SDK dependencies, so **AWS Secrets Manager** is easy to add if running on AWS.
- **Configuration files**:
  - Less ideal due to risk of accidental commit; acceptable only when encrypted at rest and managed carefully.

Recommendation for first iteration:

- Use **env vars** for:
  - API base URL.
  - Auth token or client credentials.
- Optionally upgrade later to Vault or cloud secret managers if the deployment environment supports it.

### 3.4 Replay protection and idempotency

Since we care about **at-least-once delivery**, duplicates are possible under retries. To guard against replay abuse and duplicates:

- Define a stable, unique **idempotency key** for each escrow:
  - For example: `contract_id` (and possibly a type field like `event_type=escrow_created`).
- The API should:
  - Treat requests with the same idempotency key as **idempotent** (create-once, subsequent calls are no-ops and return the same logical result).
  - Optionally reject requests older than a configured time window if a timestamp is included.
- Optionally include:
  - **Timestamp** and **nonce** per request, signed or HMACed by the client, and checked by the server. This is more relevant for Internet-facing APIs; for an internal cluster, idempotency keys are usually enough.

## 4. Reliability & delivery guarantees

### 4.1 Failure modes to consider

- Internal API temporarily unavailable (5xx, connection refused, TLS issues).
- API responding slowly, causing backpressure.
- Indexer process restarts while there is a backlog of unsent events.
- Network partitions between indexer and API.

### 4.2 Delivery semantics

- **At-most-once**:
  - The indexer sends each escrow at most one time and does **not retry** on failure.
  - Simpler client, but failures result in **permanent data loss** for those escrows.
- **At-least-once**:
  - The indexer retries failed deliveries until either:
    - They succeed, or
    - A defined max age / attempt count is exceeded.
  - Can produce duplicates, so the API must be idempotent on the escrow key.

Given that escrows represent money and contracts, **at-least-once is strongly preferred**, with:

- Idempotent API behavior keyed by `ContractID` (and possibly network + escrow type).
- Logging and metrics for failed deliveries.

### 4.3 Local retry / queue mechanism

Recommended pattern inside the indexer:

- **In-memory buffer**:
  - A bounded `chan entities.Escrow` (or `chan []entities.Escrow`) where processors enqueue events.
  - One or more worker goroutines consume from the channel and perform HTTP calls.
  - If the channel is full, apply backpressure (block producer) or drop with metrics/logging (configurable).
- **Retries with backoff**:
  - Use `github.com/cenkalti/backoff/v4` (already in `go.mod`) to implement:
    - Exponential backoff with jitter.
    - Maximum elapsed time per event (e.g., 1–5 minutes).
  - Distinguish:
    - **Retryable errors** (network errors, 5xx).
    - **Non-retryable errors** (4xx validation failures).
- **Optional durable queue** (future enhancement):
  - If lost events on process crash are unacceptable, introduce a small, local disk-backed queue:
    - E.g., BoltDB/BadgerDB-based queue.
    - Or move to a dedicated message broker (Kafka, NATS) and let a separate consumer push to the API.

First iteration can likely accept:

- **In-memory at-least-once with retries**, understanding that:
  - A process crash may lose a small tail of events still in memory.
  - This should be validated with product/ops as an explicit trade-off.

## 5. Recommended approach

### 5.1 Summary

**Use HTTPS REST calls from the indexer to the existing internal API, with:**

- Structured JSON payloads derived from `entities.Escrow`.
- A small **in-memory, channel-based queue** feeding a dedicated forwarding worker.
- **At-least-once semantics** via retries and API-level idempotency on `ContractID`.
- **Bearer/JWT auth** (and eventually mTLS) plus TLS for transport security.

### 5.2 High-level design

1. **Forwarding interface**
   - Introduce a small abstraction in the indexer, e.g.:
     - `type EscrowSink interface { Publish(ctx context.Context, escrows []entities.Escrow) error }`
   - The existing processing pipeline, after getting `[]entities.Escrow` from processors, should call into this sink. Concretely: wherever `ProcessTransaction` is currently invoked and its `[]entities.Escrow` result is aggregated, that caller becomes responsible for wiring `sink.Publish(ctx, escrows)` (or `sink.Publish(ctx, batch)` if batching multiple transactions).
2. **HTTP sink implementation**
   - Implementation that:
     - Serializes escrows to JSON.
     - POSTs them to a configured endpoint, e.g. `POST /escrows` or `POST /escrow-events`.
     - Supports **batching**:
       - Either per-call (array of escrows) or small batches (e.g., up to 50–100 items or a max payload size).
3. **Worker & queue**
   - `EscrowSink` implementation enqueues events into an internal channel.
   - A background goroutine pulls from the channel and performs HTTP POSTs using a shared `*http.Client`.
   - Retries with exponential backoff on retryable failures.
4. **Context & observability**
   - Use the incoming `ctx` from `ProcessTransaction` to:
     - Attach trace/span information to outbound HTTP calls (via `otelhttp`).
     - Propagate correlation IDs if already present.

This design is **simple, incremental, and low-risk**, while giving room to evolve towards a broker-based architecture later.

## 6. Security checklist for the recommended HTTP approach

- **Transport**
  - [ ] All requests use `https://` with TLS enforced.
  - [ ] TLS configuration validates server certificates (no `InsecureSkipVerify` in production).
  - [ ] If using mTLS, client certificates are issued by a trusted internal CA and rotated regularly.
- **Authentication & authorization**
  - [ ] Indexer authenticates to the API using a Bearer token or JWT.
  - [ ] The API validates the token and authorizes the indexer’s actions (e.g., `role=escrow_indexer`).
  - [ ] Tokens have reasonable expirations and are rotated in a controlled way.
- **Secret handling**
  - [ ] Secrets (tokens, client cert keys) are never hard-coded; sourced from env vars or secret manager.
  - [ ] Secrets are not logged or exposed in error messages.
  - [ ] Local dev/test use separate, low-privilege credentials.
- **Replay & idempotency**
  - [ ] A unique idempotency key per escrow (e.g., `ContractID`) is included in the request.
  - [ ] API treats requests with the same key as idempotent and does not create duplicates.
  - [ ] Optional: requests include timestamp + nonce with server-side replay window enforcement.
- **Least privilege & network controls**
  - [ ] The indexer is allowed to call only the necessary API endpoints.
  - [ ] Network segmentation or firewall rules restrict access to the API from only trusted hosts.
- **Observability**
  - [ ] Access logs on the API capture who called which endpoint with which idempotency key.
  - [ ] Metrics and alerts exist for failed or unusually frequent calls from the indexer.

## 7. Go ecosystem recommendations

### 7.1 For the HTTP-based approach

- **Transport & JSON**
  - `net/http` (stdlib) for HTTP client.
  - `encoding/json` or a faster drop-in (e.g., `github.com/goccy/go-json`) if needed later.
- **Retries & backoff**
  - `github.com/cenkalti/backoff/v4`:
    - Already present as an indirect dependency.
    - Good for exponential backoff with jitter and maximum elapsed time.
- **Context & traces**
  - `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`:
    - Use `otelhttp.NewTransport` or `otelhttp.NewClient` to wrap the HTTP client.
    - Preserves and emits tracing data propagated via `ctx`.
- **Authentication**
  - For JWTs, `github.com/golang-jwt/jwt/v5` is a widely used library.
  - For simple bearer tokens, stdlib is sufficient (set `Authorization` header).
- **Configuration**
  - `os.Getenv` for environment-based configuration is likely enough for now.

### 7.2 If a broker is adopted later

If the architecture evolves towards a message bus:

- **NATS**
  - `github.com/nats-io/nats.go` – simple, fast pub/sub.
  - Good for light-weight internal eventing.
- **RabbitMQ**
  - `github.com/rabbitmq/amqp091-go` – mature AMQP client.
  - Suitable when queues, routing keys, and consumer groups are needed.
- **Kafka**
  - `github.com/segmentio/kafka-go` – idiomatic Go client.
  - Strong for high-throughput, partitioned event streams with durable retention.

These would typically sit **between** the indexer and the persistence API, not replace the API itself.

## 8. Minimal Go proof-of-concept (HTTP sink)

Below is a minimal, illustrative implementation sketch of an HTTP-based escrow sink with retries. This is not production-ready, but shows how it could fit into the indexer:

```go
package forwarding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Trustless-Work/Indexer/internal/entities"
	"github.com/cenkalti/backoff/v4"
)

type EscrowHTTPSink struct {
	client    *http.Client
	endpoint  string
	authToken string
	queue     chan []entities.Escrow
}

func NewEscrowHTTPSink(endpoint, authToken string, queueSize int) *EscrowHTTPSink {
	return &EscrowHTTPSink{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		endpoint:  endpoint,
		authToken: authToken,
		queue:     make(chan []entities.Escrow, queueSize),
	}
}

// Publish is called from the indexer pipeline.
func (s *EscrowHTTPSink) Publish(ctx context.Context, escrows []entities.Escrow) error {
	select {
	case s.queue <- escrows:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run should be started in a goroutine at service startup.
func (s *EscrowHTTPSink) Run(ctx context.Context) {
	for {
		select {
		case batch := <-s.queue:
			s.sendWithRetry(ctx, batch)
		case <-ctx.Done():
			return
		}
	}
}

func (s *EscrowHTTPSink) sendWithRetry(ctx context.Context, batch []entities.Escrow) {
	payload, err := json.Marshal(batch)
	if err != nil {
		// In practice: log and drop or send to dead letter.
		return
	}

	op := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if s.authToken != "" {
			req.Header.Set("Authorization", "Bearer "+s.authToken)
		}

		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 500 {
			// Retry on server errors.
			return fmt.Errorf("server error: %s", resp.Status)
		}
		if resp.StatusCode >= 400 {
			// Client errors are not retried.
			return backoff.Permanent(fmt.Errorf("client error: %s", resp.Status))
		}
		return nil
	}

	bo := backoff.NewExponentialBackOff()
	bo.MaxElapsedTime = 2 * time.Minute
	_ = backoff.Retry(op, backoff.WithContext(bo, ctx))
}
```

In the real implementation, we would:

- Add structured logging and metrics.
- Wire this sink into wherever the indexer currently aggregates `[]entities.Escrow`.
- Ensure idempotency handling and error responses are aligned with the internal API’s contract.

## 9. Open questions / risks for implementation

- **Exact API contract**
  - What is the existing internal API’s interface for persisting escrows?
  - Does it already accept a structure similar to `entities.Escrow`, or will a mapping layer be required?
  - Can the API support batch ingestion (array of escrows) and idempotency keys?
- **Throughput & SLOs**
  - Expected average and peak escrow creation rates?
  - Latency and durability requirements (e.g., “escrow creation must be persisted within X seconds”).
- **Failure tolerance**
  - Is it acceptable to lose a small tail of events on indexer crash (in-memory queue only)?
  - If not, should we:
    - Implement a disk-backed local queue, or
    - Adopt a message broker as an intermediary?
- **Security environment**
  - Is the deployment environment already using:
    - A service mesh (e.g., mTLS by default)?
    - An identity provider for service-to-service auth (e.g., SPIFFE, OIDC)?
  - Which secret manager (if any) is standardized?
- **Future lifecycle events**
  - Will the same mechanism be reused for:
    - Escrow releases.
    - Disputes.
    - Milestone updates.
  - If yes, we may want a slightly more generic “escrow event” schema on the API side.

Clarifying these points with the API owners and platform/infra team will de-risk the implementation and inform precise configuration choices (batch sizes, retry policies, secret sources, etc.).

