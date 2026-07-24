package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Trustless-Work/Indexer/internal/state"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

const testPassphrase = "Test SDF Network ; September 2015"

// fakeRPC is a scriptable rpcAPI. Zero-value methods succeed with the
// pool's own passphrase and a wide-open ledger window.
type fakeRPC struct {
	passphrase string
	oldest     uint32
	latest     uint32
	netErr     error
	healthErr  error
	closed     bool
}

func (f *fakeRPC) GetNetwork(ctx context.Context) (protocol.GetNetworkResponse, error) {
	if f.netErr != nil {
		return protocol.GetNetworkResponse{}, f.netErr
	}
	p := f.passphrase
	if p == "" {
		p = testPassphrase
	}
	return protocol.GetNetworkResponse{Passphrase: p}, nil
}

func (f *fakeRPC) GetHealth(ctx context.Context) (protocol.GetHealthResponse, error) {
	if f.healthErr != nil {
		return protocol.GetHealthResponse{}, f.healthErr
	}
	return protocol.GetHealthResponse{OldestLedger: f.oldest, LatestLedger: f.latest}, nil
}

func (f *fakeRPC) GetLatestLedger(ctx context.Context) (protocol.GetLatestLedgerResponse, error) {
	return protocol.GetLatestLedgerResponse{Sequence: f.latest}, nil
}

func (f *fakeRPC) GetLedgerEntries(ctx context.Context, request protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	return protocol.GetLedgerEntriesResponse{}, nil
}

func (f *fakeRPC) Close() error {
	f.closed = true
	return nil
}

// fakeBackend records PrepareRange calls; GetLedger is driven by fn.
type fakeBackend struct {
	prepared  []ledgerbackend.Range
	prepErr   error
	getLedger func(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error)
	closed    bool
}

func (f *fakeBackend) GetLatestLedgerSequence(ctx context.Context) (uint32, error) { return 0, nil }

func (f *fakeBackend) GetLedger(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
	if f.getLedger != nil {
		return f.getLedger(ctx, seq)
	}
	return xdr.LedgerCloseMeta{}, errors.New("fakeBackend: no getLedger script")
}

func (f *fakeBackend) PrepareRange(ctx context.Context, r ledgerbackend.Range) error {
	if f.prepErr != nil {
		return f.prepErr
	}
	f.prepared = append(f.prepared, r)
	return nil
}

func (f *fakeBackend) IsPrepared(ctx context.Context, r ledgerbackend.Range) (bool, error) {
	return len(f.prepared) > 0, nil
}

func (f *fakeBackend) Close() error {
	f.closed = true
	return nil
}

// testPool builds a pool over the given URLs whose per-endpoint clients
// and backends come from the maps (keyed by URL). Missing entries get
// permissive defaults.
func testPool(urls []string, clients map[string]*fakeRPC, backends map[string]*fakeBackend) *rpcPool {
	return &rpcPool{
		urls:       urls,
		passphrase: testPassphrase,
		now:        func() time.Time { return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC) },
		newClient: func(rawURL string) rpcAPI {
			if c, ok := clients[rawURL]; ok {
				return c
			}
			return &fakeRPC{oldest: 1, latest: 1_000_000}
		},
		newBackend: func(rawURL string) (ledgerbackend.LedgerBackend, error) {
			if b, ok := backends[rawURL]; ok {
				return b, nil
			}
			return &fakeBackend{}, nil
		},
	}
}

func noGap(t *testing.T) onGapFunc {
	t.Helper()
	return func(gap state.Gap, clampedStart uint32) error {
		t.Fatalf("unexpected gap [%d, %d] (clamped start %d)", gap.FromLedger, gap.ToLedger, clampedStart)
		return nil
	}
}

func TestPoolConnect_SkipsMismatchedPassphrase(t *testing.T) {
	urls := []string{"https://wrong.example.org", "https://right.example.org"}
	clients := map[string]*fakeRPC{
		"https://wrong.example.org": {passphrase: "Public Global Stellar Network ; September 2015", oldest: 1, latest: 2000},
		"https://right.example.org": {oldest: 1, latest: 2000},
	}
	backends := map[string]*fakeBackend{"https://right.example.org": {}}
	p := testPool(urls, clients, backends)

	start, err := p.Connect(context.Background(), 1500, 0, noGap(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if start != 1500 {
		t.Errorf("start = %d, want 1500", start)
	}
	if p.CurrentHost() != "right.example.org" {
		t.Errorf("active endpoint = %s, want right.example.org", p.CurrentHost())
	}
	if !clients["https://wrong.example.org"].closed {
		t.Error("rejected endpoint's client must be closed")
	}
	if len(backends["https://right.example.org"].prepared) != 1 {
		t.Fatalf("adopted backend must be prepared exactly once; got %v", backends["https://right.example.org"].prepared)
	}
}

func TestPoolConnect_ClampRecordsGapBeforeAdoption(t *testing.T) {
	urls := []string{"https://short-retention.example.org"}
	clients := map[string]*fakeRPC{
		"https://short-retention.example.org": {oldest: 1000, latest: 2000},
	}
	p := testPool(urls, clients, nil)

	var gotGap *state.Gap
	var gotStart uint32
	start, err := p.Connect(context.Background(), 400, 0, func(gap state.Gap, clampedStart uint32) error {
		gotGap = &gap
		gotStart = clampedStart
		return nil
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if start != 1000 || gotStart != 1000 {
		t.Errorf("clamped start = %d (callback %d), want 1000", start, gotStart)
	}
	if gotGap == nil || gotGap.FromLedger != 400 || gotGap.ToLedger != 999 {
		t.Fatalf("gap = %+v, want [400, 999]", gotGap)
	}
}

func TestPoolConnect_GapCallbackFailureIsTerminal(t *testing.T) {
	// Persisting gap evidence is local state: when it fails, trying the
	// next endpoint must NOT happen — that would process ledgers with the
	// data-loss evidence unrecorded.
	urls := []string{"https://a.example.org", "https://b.example.org"}
	clients := map[string]*fakeRPC{
		"https://a.example.org": {oldest: 1000, latest: 2000},
		"https://b.example.org": {oldest: 1, latest: 2000},
	}
	p := testPool(urls, clients, nil)

	sentinel := errors.New("disk full")
	_, err := p.Connect(context.Background(), 400, 0, func(state.Gap, uint32) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the gap persistence error to surface, got %v", err)
	}
	if p.backend != nil {
		t.Error("no endpoint may be adopted after a terminal gap failure")
	}
}

func TestPoolConnect_ResolvesZeroStartFromEndpointTip(t *testing.T) {
	urls := []string{"https://tip.example.org"}
	clients := map[string]*fakeRPC{
		"https://tip.example.org": {oldest: 1, latest: 777},
	}
	p := testPool(urls, clients, nil)

	start, err := p.Connect(context.Background(), 0, 0, noGap(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if start != 777 {
		t.Errorf("start = %d, want the endpoint tip 777", start)
	}
}

func TestPoolConnect_BoundedRangeBeyondRetentionSkipsEndpoint(t *testing.T) {
	// The first endpoint's retention starts after the requested bounded
	// range: an archive fallback later in the pool must win instead of
	// the range degrading into a gap.
	urls := []string{"https://recent.example.org", "https://archive.example.org"}
	clients := map[string]*fakeRPC{
		"https://recent.example.org":  {oldest: 5000, latest: 9000},
		"https://archive.example.org": {oldest: 1, latest: 9000},
	}
	p := testPool(urls, clients, nil)

	start, err := p.Connect(context.Background(), 100, 200, noGap(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if start != 100 {
		t.Errorf("start = %d, want 100", start)
	}
	if p.CurrentHost() != "archive.example.org" {
		t.Errorf("active endpoint = %s, want archive.example.org", p.CurrentHost())
	}
}

func TestPoolConnect_AllFail_JoinsEveryEndpointError(t *testing.T) {
	urls := []string{"https://a.example.org", "https://b.example.org"}
	clients := map[string]*fakeRPC{
		"https://a.example.org": {netErr: errors.New("a is down")},
		"https://b.example.org": {healthErr: errors.New("b is sick")},
	}
	p := testPool(urls, clients, nil)

	_, err := p.Connect(context.Background(), 100, 0, noGap(t))
	if err == nil {
		t.Fatal("expected an error with every endpoint failing")
	}
	for _, want := range []string{"all 2 RPC endpoints failed", "a is down", "b is sick"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must mention %q; got: %v", want, err)
		}
	}
}

func TestPoolRotate_PrefersNextAndWrapsAround(t *testing.T) {
	urls := []string{"https://a.example.org", "https://b.example.org", "https://c.example.org"}
	var attempts []string
	p := testPool(urls, nil, nil)
	baseClient := p.newClient
	p.newClient = func(rawURL string) rpcAPI {
		attempts = append(attempts, rawURL)
		if rawURL == "https://b.example.org" {
			return &fakeRPC{netErr: errors.New("b refuses")}
		}
		return baseClient(rawURL)
	}

	if _, err := p.Connect(context.Background(), 100, 0, noGap(t)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	attempts = nil

	if _, err := p.Rotate(context.Background(), 100, 0, noGap(t)); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	// From active a: rotation must try b first (next in order), then c.
	if len(attempts) != 2 || attempts[0] != "https://b.example.org" || attempts[1] != "https://c.example.org" {
		t.Errorf("rotation order = %v, want [b, c]", attempts)
	}
	if p.CurrentHost() != "c.example.org" {
		t.Errorf("active endpoint = %s, want c.example.org", p.CurrentHost())
	}
}

func TestPoolRotate_SingleURLReconnectsSameEndpoint(t *testing.T) {
	urls := []string{"https://only.example.org"}
	first := &fakeBackend{}
	second := &fakeBackend{}
	handed := 0
	p := testPool(urls, nil, nil)
	p.newBackend = func(rawURL string) (ledgerbackend.LedgerBackend, error) {
		handed++
		if handed == 1 {
			return first, nil
		}
		return second, nil
	}

	if _, err := p.Connect(context.Background(), 100, 0, noGap(t)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := p.Rotate(context.Background(), 200, 0, noGap(t)); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !first.closed {
		t.Error("rotation must close the abandoned backend")
	}
	wantRange := ledgerbackend.UnboundedRange(200)
	if len(second.prepared) != 1 || second.prepared[0] != wantRange {
		t.Errorf("new backend prepared with %v, want [%v]", second.prepared, wantRange)
	}
}

func TestEndpointHost_NeverEchoesRawURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://user:secret@rpc.example.org/v1/key", "rpc.example.org"},
		{"https://rpc.example.org?apikey=xyz", "rpc.example.org"},
		{"::not a url::", "<unparseable-rpc-url>"},
	}
	for _, tt := range tests {
		if got := endpointHost(tt.raw); got != tt.want {
			t.Errorf("endpointHost(%q) = %q, want %q", tt.raw, got, tt.want)
		}
		if got := endpointHost(tt.raw); strings.Contains(got, "secret") || strings.Contains(got, "apikey") {
			t.Errorf("endpointHost(%q) leaked credentials: %q", tt.raw, got)
		}
	}
}

func TestFetchLedgerWithRetry_FrozenTipHitsStallBudget(t *testing.T) {
	// The SDK's GetLedger blocks internally at a frozen tip: it returns
	// only when the (attempt) context expires. The watchdog must convert
	// that into errTipStalled instead of waiting forever.
	backend := &fakeBackend{
		getLedger: func(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
			<-ctx.Done()
			return xdr.LedgerCloseMeta{}, ctx.Err()
		},
	}

	start := time.Now()
	_, err := fetchLedgerWithRetry(context.Background(), backend, 42, 50*time.Millisecond)
	if !errors.Is(err, errTipStalled) {
		t.Fatalf("expected errTipStalled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stall detection took %s; the budget was 50ms", elapsed)
	}
}

func TestFetchLedgerWithRetry_RejectStyleTipAccumulatesIntoStall(t *testing.T) {
	// Providers that REJECT beyond-tip requests (instead of blocking)
	// produce window errors; those waits must also consume the stall
	// budget or a frozen reject-style endpoint would spin forever.
	windowErr := fmt.Errorf("[-32600] start ledger (100) must be between the oldest ledger: 2 and the latest ledger: 99 for this rpc instance")
	backend := &fakeBackend{
		getLedger: func(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
			return xdr.LedgerCloseMeta{}, windowErr
		},
	}

	_, err := fetchLedgerWithRetry(context.Background(), backend, 100, tipWaitInterval)
	if !errors.Is(err, errTipStalled) {
		t.Fatalf("expected errTipStalled, got %v", err)
	}
}

func TestFetchLedgerWithRetry_TransientErrorsStillRetryToSuccess(t *testing.T) {
	calls := 0
	backend := &fakeBackend{
		getLedger: func(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
			calls++
			if calls < 2 {
				return xdr.LedgerCloseMeta{}, errors.New("transient network sneeze")
			}
			return xdr.LedgerCloseMeta{}, nil
		},
	}

	if _, err := fetchLedgerWithRetry(context.Background(), backend, 42, 30*time.Second); err != nil {
		t.Fatalf("expected recovery after a transient error, got %v", err)
	}
	if calls != 2 {
		t.Errorf("GetLedger calls = %d, want 2", calls)
	}
}

func TestFetchLedgerWithRetry_ParentCancellationIsNotAStall(t *testing.T) {
	backend := &fakeBackend{
		getLedger: func(ctx context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
			<-ctx.Done()
			return xdr.LedgerCloseMeta{}, ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := fetchLedgerWithRetry(ctx, backend, 42, time.Hour)
	if errors.Is(err, errTipStalled) {
		t.Fatalf("shutdown must not be reported as a stall; got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
