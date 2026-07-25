package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Trustless-Work/Indexer/internal/config"
	"github.com/Trustless-Work/Indexer/internal/state"
	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// rpcAPI is the slice of the Soroban RPC client surface the pool needs.
// *rpcclient.Client satisfies it; tests substitute fakes.
type rpcAPI interface {
	GetNetwork(ctx context.Context) (protocol.GetNetworkResponse, error)
	GetHealth(ctx context.Context) (protocol.GetHealthResponse, error)
	GetLatestLedger(ctx context.Context) (protocol.GetLatestLedgerResponse, error)
	GetLedgerEntries(ctx context.Context, request protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
	Close() error
}

var _ rpcAPI = (*rpcclient.Client)(nil)

// onGapFunc is invoked when connecting to an endpoint requires clamping
// the start ledger past a range the endpoint does not retain. The caller
// must make the gap durable (append + persist) before returning: connect
// proceeds to live processing right after, and unrecorded skipped ledgers
// are silent data loss. A non-nil return aborts the whole failover cycle,
// not just this endpoint — failing to persist evidence is a local-state
// problem no other RPC can fix.
type onGapFunc func(gap state.Gap, clampedStart uint32) error

// terminalConnectError marks a connect failure that trying the next
// endpoint cannot fix (e.g. persisting gap evidence failed). The cycle
// aborts immediately instead of burning through the rest of the pool.
type terminalConnectError struct{ err error }

func (e *terminalConnectError) Error() string { return e.err.Error() }
func (e *terminalConnectError) Unwrap() error { return e.err }

// rpcPool owns the connection to "the RPC" as a rotating pool of
// endpoints: RPC_URL first, then RPC_FALLBACK_URLS in order. At any
// moment exactly one endpoint is active, feeding BOTH halves of the
// pipeline — the ledger backend the loop fetches from and the rpcclient
// used for getLedgerEntries state reads — so ledger data and state data
// can never come from two different providers mid-ledger.
//
// Not goroutine-safe by design: Connect/Rotate/Backend/GetLedgerEntries
// all run on the single ingest goroutine (the same invariant as the
// registry and the state detector).
type rpcPool struct {
	urls       []string
	passphrase string
	idx        int

	// requireFullRange (replay runs) turns a clamp into an endpoint
	// failure instead of a recorded gap: an explicitly requested range
	// must be served whole or not at all — a deeper-retention fallback
	// may still have it.
	requireFullRange bool

	backend ledgerbackend.LedgerBackend
	client  rpcAPI

	// Construction seams, injected by tests. Production wiring lives in
	// newRPCPool.
	newClient  func(rawURL string) rpcAPI
	newBackend func(rawURL string) (ledgerbackend.LedgerBackend, error)
	now        func() time.Time

	// fetchDuration, when set (see Instrument), observes each successful
	// GetLedger. Owned by the POOL — not per backend — because the SDK's
	// ledgerbackend.WithMetrics registers its summary on every call and
	// would panic with a duplicate on the first rotation. One summary
	// across every endpoint the pool ever adopts, same name, objectives
	// and semantics (successful fetches only) as the SDK's.
	fetchDuration prometheus.Summary
}

// newRPCPool builds the production pool from cfg. Blank fallback entries
// are dropped defensively; config.Validate already rejects them loudly.
func newRPCPool(cfg *config.Config) *rpcPool {
	urls := make([]string, 0, 1+len(cfg.RPC.FallbackURLs))
	urls = append(urls, strings.TrimSpace(cfg.RPC.URL))
	for _, u := range cfg.RPC.FallbackURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	return &rpcPool{
		urls:             urls,
		passphrase:       cfg.Network.Passphrase,
		requireFullRange: cfg.Replay,
		now:              time.Now,
		newClient: func(rawURL string) rpcAPI {
			return rpcclient.NewClient(rawURL, &http.Client{Timeout: cfg.RPC.RequestTimeout})
		},
		newBackend: func(rawURL string) (ledgerbackend.LedgerBackend, error) {
			return NewLedgerBackend(cfg, rawURL)
		},
	}
}

// Connect establishes the first working endpoint, starting at the pool's
// current position. Used at boot; a restart during a primary outage
// therefore comes up on a fallback instead of crash-looping against the
// dead primary. Returns the ledger to start from — the input resolved
// (0 → this endpoint's tip) and clamped against this endpoint's window.
func (p *rpcPool) Connect(ctx context.Context, from, end uint32, onGap onGapFunc) (uint32, error) {
	return p.cycle(ctx, p.idx, from, end, onGap)
}

// Rotate abandons the current endpoint and connects to the next one that
// works, wrapping around the pool; the endpoint just abandoned is retried
// last (with a single-URL pool that means an immediate reconnect — the
// pre-pool behaviour). Returns the ledger to resume from on the new
// endpoint: equal to from when the endpoint covers it, or clamped forward
// (gap recorded via onGap) when it does not.
func (p *rpcPool) Rotate(ctx context.Context, from, end uint32, onGap onGapFunc) (uint32, error) {
	p.closeSession(ctx)
	return p.cycle(ctx, p.idx+1, from, end, onGap)
}

// cycle tries every endpoint once, in pool order starting at first,
// returning on the first success. All endpoints failing is the terminal
// condition the caller escalates (exit → supervisor restart → new cycle).
func (p *rpcPool) cycle(ctx context.Context, first int, from, end uint32, onGap onGapFunc) (uint32, error) {
	var failures []error
	for off := 0; off < len(p.urls); off++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		i := (first + off) % len(p.urls)
		start, err := p.connectEndpoint(ctx, i, from, end, onGap)
		if err == nil {
			return start, nil
		}
		var terminal *terminalConnectError
		if errors.As(err, &terminal) {
			return 0, terminal.err
		}
		log.Ctx(ctx).Warnf("RPC endpoint %s rejected: %v", endpointHost(p.urls[i]), err)
		failures = append(failures, err)
	}
	return 0, fmt.Errorf("all %d RPC endpoints failed: %w", len(p.urls), errors.Join(failures...))
}

// connectEndpoint runs the full adoption sequence against p.urls[i]:
// verify the network passphrase, resolve a zero start from the tip, clamp
// the start against the endpoint's retention window, prepare the backend
// range, and only then record any clamp gap and swap the live session.
// The order matters: the gap is recorded only for the endpoint actually
// adopted, and PrepareRange processes nothing, so evidence still lands
// before any ledger is processed.
func (p *rpcPool) connectEndpoint(ctx context.Context, i int, from, end uint32, onGap onGapFunc) (uint32, error) {
	rawURL := p.urls[i]
	host := endpointHost(rawURL)
	client := p.newClient(rawURL)
	var backend ledgerbackend.LedgerBackend
	adopted := false
	defer func() {
		if adopted {
			return
		}
		_ = client.Close()
		if backend != nil {
			_ = backend.Close()
		}
	}()

	// Same rule as the original boot check: a passphrase mismatch would
	// produce silently wrong tx hashes on every ledger, so the endpoint is
	// unusable no matter how healthy it looks.
	netInfo, err := client.GetNetwork(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: verifying network: %w", host, err)
	}
	if netInfo.Passphrase != p.passphrase {
		return 0, fmt.Errorf("%s: serves network %q but NETWORK_PASSPHRASE is %q — fix RPC_URL/RPC_FALLBACK_URLS or NETWORK_PASSPHRASE", host, netInfo.Passphrase, p.passphrase)
	}

	// START_LEDGER unset (0) means "start from the network tip". Resolved
	// here, per endpoint, because the RPC rejects a PrepareRange starting
	// at 0 and the backend's own GetLatestLedgerSequence requires
	// PrepareRange first (chicken-and-egg).
	if from == 0 {
		latest, lerr := client.GetLatestLedger(ctx)
		if lerr != nil {
			return 0, fmt.Errorf("%s: resolving latest ledger: %w", host, lerr)
		}
		log.Ctx(ctx).Infof("START_LEDGER unset; starting from network tip %d (via %s)", latest.Sequence, host)
		from = latest.Sequence
	}

	window, err := client.GetHealth(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: fetching health: %w", host, err)
	}
	log.Ctx(ctx).Infof("RPC window on %s: oldest=%d latest=%d retention=%d ledgers",
		host, window.OldestLedger, window.LatestLedger, window.LedgerRetentionWindow)

	start, gap, err := clampStartLedger(from, window.OldestLedger, window.LatestLedger, p.now())
	if err != nil {
		return 0, fmt.Errorf("%s: %w", host, err)
	}
	// A bounded backfill this endpoint cannot serve is an endpoint
	// failure, not a gap: a deeper-retention fallback later in the pool
	// may still serve the range for real.
	if end != 0 && start > end {
		return 0, fmt.Errorf("%s: cannot serve bounded range [%d, %d]: its oldest available ledger is %d", host, from, end, window.OldestLedger)
	}
	if gap != nil && p.requireFullRange {
		return 0, fmt.Errorf("%s: cannot serve ledgers [%d, %d] of the requested range (oldest available: %d) — an archive endpoint can", host, gap.FromLedger, gap.ToLedger, window.OldestLedger)
	}

	backend, err = p.newBackend(rawURL)
	if err != nil {
		return 0, fmt.Errorf("%s: building ledger backend: %w", host, err)
	}
	if err := prepareBackendRange(ctx, backend, start, end); err != nil {
		return 0, fmt.Errorf("%s: %w", host, err)
	}

	if gap != nil {
		log.Ctx(ctx).Warnf("Start ledger %d is below the retention of %s (oldest %d): clamping forward — ledgers [%d, %d] are SKIPPED and recorded as a gap for later backfill",
			from, host, window.OldestLedger, gap.FromLedger, gap.ToLedger)
		if gerr := onGap(*gap, start); gerr != nil {
			return 0, &terminalConnectError{fmt.Errorf("recording retention gap [%d, %d]: %w", gap.FromLedger, gap.ToLedger, gerr)}
		}
	}

	adopted = true
	p.closeSession(ctx)
	if p.fetchDuration != nil {
		backend = timedBackend{LedgerBackend: backend, fetchDuration: p.fetchDuration}
	}
	p.backend, p.client, p.idx = backend, client, i
	log.Ctx(ctx).Infof("RPC endpoint active: %s (%d of %d in pool)", host, i+1, len(p.urls))
	return start, nil
}

// Instrument registers the ledger-fetch duration summary on reg and
// starts timing every backend the pool adopts from now on. Call once,
// before Connect.
func (p *rpcPool) Instrument(reg *prometheus.Registry) {
	summary := prometheus.NewSummary(prometheus.SummaryOpts{
		Namespace: "indexer", Subsystem: "ingest", Name: "ledger_fetch_duration_seconds",
		Help:       "duration of fetching ledgers from the ledger backend, sliding window = 10m",
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
	})
	reg.MustRegister(summary)
	p.fetchDuration = summary
}

// timedBackend observes successful GetLedger durations on the pool's
// shared summary (the SDK WithMetrics semantics, rotation-safe).
type timedBackend struct {
	ledgerbackend.LedgerBackend
	fetchDuration prometheus.Summary
}

func (b timedBackend) GetLedger(ctx context.Context, sequence uint32) (xdr.LedgerCloseMeta, error) {
	started := time.Now()
	lcm, err := b.LedgerBackend.GetLedger(ctx, sequence)
	if err != nil {
		return xdr.LedgerCloseMeta{}, err
	}
	b.fetchDuration.Observe(time.Since(started).Seconds())
	return lcm, nil
}

// Backend returns the active ledger backend. Only valid after a
// successful Connect.
func (p *rpcPool) Backend() ledgerbackend.LedgerBackend { return p.backend }

// GetLedgerEntries delegates to the active endpoint's client, so the
// state detector and the sweep follow every rotation automatically.
func (p *rpcPool) GetLedgerEntries(ctx context.Context, request protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	return p.client.GetLedgerEntries(ctx, request)
}

// CurrentHost names the active endpoint for logs and /status. Host only —
// provider URLs can carry credentials in userinfo, paths or query strings,
// none of which belong in logs.
func (p *rpcPool) CurrentHost() string { return endpointHost(p.urls[p.idx]) }

// Close releases the active session. Safe to call with none established.
func (p *rpcPool) Close() error {
	p.closeSession(context.Background())
	return nil
}

func (p *rpcPool) closeSession(ctx context.Context) {
	if p.backend != nil {
		if err := p.backend.Close(); err != nil {
			log.Ctx(ctx).Warnf("closing ledger backend for %s: %v", p.CurrentHost(), err)
		}
		p.backend = nil
	}
	if p.client != nil {
		if err := p.client.Close(); err != nil {
			log.Ctx(ctx).Warnf("closing RPC client for %s: %v", p.CurrentHost(), err)
		}
		p.client = nil
	}
}

// endpointHost reduces an endpoint URL to its host for display. Never
// returns the raw URL: if it does not parse, it returns a placeholder
// rather than risk echoing embedded credentials.
func endpointHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "<unparseable-rpc-url>"
	}
	return u.Host
}
