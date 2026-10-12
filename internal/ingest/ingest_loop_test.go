package ingest

// Loop-level tests for Ingest() itself — the ~450 lines that own the
// central invariant: THE CURSOR NEVER ADVANCES PAST AN UNPUBLISHED FACT.
// Everything here runs the REAL loop body (black-box through cfg) with a
// scripted pool and sink injected via the newPool/newSink seams; the
// ledgers are hand-built XDR carrying one contract event from a seeded
// escrow, so the publish path actually fires.
//
// The ledgers close far in the past on purpose: the loop sees itself as
// catching up, defers state fetches and skips the sweep, leaving exactly
// one publishable fact (the event) — the minimal fixture that exercises
// publish-then-save ordering.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Trustless-Work/Indexer/internal/config"
	"github.com/Trustless-Work/Indexer/internal/events"
	"github.com/Trustless-Work/Indexer/internal/sink"
	"github.com/Trustless-Work/Indexer/internal/state"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// loopSink records every published envelope and fails exactly once,
// on the failOn-th Publish call (1-based; 0 = never fail). onPublish, when
// set, observes each successful publish — tests use it to assert what the
// world looked like at publish time (e.g. "the cursor file did not exist
// yet").
type loopSink struct {
	mu        sync.Mutex
	published []events.Envelope
	calls     int
	failOn    int
	onPublish func(env events.Envelope)
	closed    bool
}

var errScriptedPublish = errors.New("scripted publish failure")

func (s *loopSink) Publish(_ context.Context, env events.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failOn != 0 && s.calls == s.failOn {
		return errScriptedPublish
	}
	s.published = append(s.published, env)
	if s.onPublish != nil {
		s.onPublish(env)
	}
	return nil
}

func (s *loopSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *loopSink) snapshot() []events.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]events.Envelope, len(s.published))
	copy(out, s.published)
	return out
}

// testEscrowID is a syntactically valid contract address derived from a
// fixed 32-byte payload, used both to seed the registry and as the
// event's emitter.
func testEscrowID(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	raw[0] = 0x42
	id, err := strkey.Encode(strkey.VersionByteContract, raw)
	if err != nil {
		t.Fatalf("encoding test escrow id: %v", err)
	}
	return id
}

// loopFixtureCloseTime is old enough that every ledger reads as historic
// (catching up): state fetches are deferred and the sweep never arms, so
// the only publishable fact is the contract event.
var loopFixtureCloseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// emptyLedgerMeta builds a V1 LedgerCloseMeta with no transactions.
func emptyLedgerMeta(t *testing.T, seq uint32) xdr.LedgerCloseMeta {
	t.Helper()
	return ledgerMeta(t, seq, nil, nil)
}

// eventLedgerMeta builds a V1 LedgerCloseMeta with one successful
// InvokeHostFunction transaction whose Soroban meta carries one contract
// event emitted by escrowID.
func eventLedgerMeta(t *testing.T, seq uint32, escrowID string) xdr.LedgerCloseMeta {
	t.Helper()

	raw, err := strkey.Decode(strkey.VersionByteContract, escrowID)
	if err != nil {
		t.Fatalf("decoding escrow id: %v", err)
	}
	var contractID xdr.ContractId
	copy(contractID[:], raw)

	topic := xdr.ScSymbol("initialized")
	event := xdr.ContractEvent{
		Type:       xdr.ContractEventTypeContract,
		ContractId: &contractID,
		Body: xdr.ContractEventBody{
			V: 0,
			V0: &xdr.ContractEventV0{
				Topics: []xdr.ScVal{{Type: xdr.ScValTypeScvSymbol, Sym: &topic}},
				Data:   xdr.ScVal{Type: xdr.ScValTypeScvVoid},
			},
		},
	}

	var sourceKey xdr.Uint256
	sourceKey[0] = 0x01
	contractAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &contractID}
	fnName := xdr.ScSymbol("initialize")
	env := xdr.TransactionEnvelope{
		Type: xdr.EnvelopeTypeEnvelopeTypeTx,
		V1: &xdr.TransactionV1Envelope{
			Tx: xdr.Transaction{
				SourceAccount: xdr.MuxedAccount{
					Type:    xdr.CryptoKeyTypeKeyTypeEd25519,
					Ed25519: &sourceKey,
				},
				Fee:    100,
				SeqNum: xdr.SequenceNumber(1),
				Cond:   xdr.Preconditions{Type: xdr.PreconditionTypePrecondNone},
				Operations: []xdr.Operation{{
					Body: xdr.OperationBody{
						Type: xdr.OperationTypeInvokeHostFunction,
						InvokeHostFunctionOp: &xdr.InvokeHostFunctionOp{
							HostFunction: xdr.HostFunction{
								Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
								InvokeContract: &xdr.InvokeContractArgs{
									ContractAddress: contractAddr,
									FunctionName:    fnName,
								},
							},
						},
					},
				}},
			},
		},
	}

	txHash, err := network.HashTransactionInEnvelope(env, testPassphrase)
	if err != nil {
		t.Fatalf("hashing fixture transaction: %v", err)
	}

	opResults := []xdr.OperationResult{{
		Code: xdr.OperationResultCodeOpInner,
		Tr: &xdr.OperationResultTr{
			Type: xdr.OperationTypeInvokeHostFunction,
			InvokeHostFunctionResult: &xdr.InvokeHostFunctionResult{
				Code:    xdr.InvokeHostFunctionResultCodeInvokeHostFunctionSuccess,
				Success: &xdr.Hash{},
			},
		},
	}}
	processing := xdr.TransactionResultMeta{
		Result: xdr.TransactionResultPair{
			TransactionHash: txHash,
			Result: xdr.TransactionResult{
				FeeCharged: 100,
				Result: xdr.TransactionResultResult{
					Code:    xdr.TransactionResultCodeTxSuccess,
					Results: &opResults,
				},
			},
		},
		TxApplyProcessing: xdr.TransactionMeta{
			V: 3,
			V3: &xdr.TransactionMetaV3{
				Operations: []xdr.OperationMeta{{}},
				SorobanMeta: &xdr.SorobanTransactionMeta{
					Events: []xdr.ContractEvent{event},
				},
			},
		},
	}

	return ledgerMeta(t, seq, []xdr.TransactionEnvelope{env}, []xdr.TransactionResultMeta{processing})
}

// ledgerMeta assembles the V1 LedgerCloseMeta shell shared by the empty
// and event fixtures. Hashes are synthetic but chain-consistent: ledger N
// hashes to a deterministic value and names N-1's as its parent.
func ledgerMeta(t *testing.T, seq uint32, txs []xdr.TransactionEnvelope, processing []xdr.TransactionResultMeta) xdr.LedgerCloseMeta {
	t.Helper()

	phases := []xdr.TransactionPhase{}
	if len(txs) > 0 {
		phases = append(phases, xdr.TransactionPhase{
			V: 0,
			V0Components: &[]xdr.TxSetComponent{{
				Type: xdr.TxSetComponentTypeTxsetCompTxsMaybeDiscountedFee,
				TxsMaybeDiscountedFee: &xdr.TxSetComponentTxsMaybeDiscountedFee{
					Txs: txs,
				},
			}},
		})
	}

	return xdr.LedgerCloseMeta{
		V: 1,
		V1: &xdr.LedgerCloseMetaV1{
			LedgerHeader: xdr.LedgerHeaderHistoryEntry{
				Hash: synthLedgerHash(seq),
				Header: xdr.LedgerHeader{
					LedgerSeq:          xdr.Uint32(seq),
					PreviousLedgerHash: synthLedgerHash(seq - 1),
					ScpValue: xdr.StellarValue{
						CloseTime: xdr.TimePoint(loopFixtureCloseTime.Unix()),
					},
				},
			},
			TxSet: xdr.GeneralizedTransactionSet{
				V: 1,
				V1TxSet: &xdr.TransactionSetV1{
					PreviousLedgerHash: synthLedgerHash(seq - 1),
					Phases:             phases,
				},
			},
			TxProcessing: processing,
		},
	}
}

// synthLedgerHash derives a deterministic fake ledger hash from seq so
// parent links stay consistent across fixtures.
func synthLedgerHash(seq uint32) xdr.Hash {
	var h xdr.Hash
	h[0] = byte(seq >> 24)
	h[1] = byte(seq >> 16)
	h[2] = byte(seq >> 8)
	h[3] = byte(seq)
	h[31] = 0xAB
	return h
}

// loopHarness wires a bounded 1..end run of the REAL Ingest() over
// scripted ledgers and a scripted sink, overriding the newPool/newSink
// seams for the duration of the test (restored via t.Cleanup, tests must
// not run in parallel).
type loopHarness struct {
	cfg       *config.Config
	sink      *loopSink
	statePath string
}

func newLoopHarness(t *testing.T, end uint32, metas map[uint32]xdr.LedgerCloseMeta, out *loopSink) *loopHarness {
	t.Helper()

	dir := t.TempDir()
	statePath := filepath.Join(dir, "indexer.state.json")

	seedPath := filepath.Join(dir, "seed.txt")
	if err := os.WriteFile(seedPath, []byte(testEscrowID(t)+"\n"), 0o600); err != nil {
		t.Fatalf("writing seed file: %v", err)
	}

	cfg := &config.Config{}
	cfg.Network.Name = "testnet"
	cfg.Network.Passphrase = testPassphrase
	cfg.RPC.URL = "https://scripted.invalid"
	cfg.RPC.RequestTimeout = time.Second
	cfg.RPC.LedgerFetchTimeout = time.Second
	cfg.Indexer.StartLedger = 1
	cfg.Indexer.EndLedger = end
	cfg.Indexer.GetLedgersLimit = 10
	cfg.Indexer.LedgerBackendType = "rpc"
	cfg.Indexer.SweepEnabled = false
	cfg.Escrow.SeedPath = seedPath
	cfg.Sink.Type = "noop" // irrelevant: newSink is overridden below
	cfg.State.Path = statePath
	cfg.Health.Enabled = false
	cfg.Logging.Level = "error"
	cfg.Logging.Format = "text"

	backend := &fakeBackend{getLedger: func(_ context.Context, seq uint32) (xdr.LedgerCloseMeta, error) {
		meta, ok := metas[seq]
		if !ok {
			return xdr.LedgerCloseMeta{}, errors.New("fixture has no ledger for requested sequence")
		}
		return meta, nil
	}}
	pool := testPool(
		[]string{cfg.RPC.URL},
		map[string]*fakeRPC{cfg.RPC.URL: {oldest: 1, latest: 1_000_000}},
		map[string]*fakeBackend{cfg.RPC.URL: backend},
	)

	prevPool, prevSink := newPool, newSink
	newPool = func(*config.Config) *rpcPool { return pool }
	newSink = func(*config.Config) (sink.Sink, error) { return out, nil }
	t.Cleanup(func() { newPool, newSink = prevPool, prevSink })

	return &loopHarness{cfg: cfg, sink: out, statePath: statePath}
}

func (h *loopHarness) stateFileExists() bool {
	_, err := os.Stat(h.statePath)
	return err == nil
}

// eventFixture returns metas for ledgers 1..end where every ledger is
// empty except eventAt, which carries the escrow event.
func eventFixture(t *testing.T, end, eventAt uint32) map[uint32]xdr.LedgerCloseMeta {
	t.Helper()
	metas := make(map[uint32]xdr.LedgerCloseMeta, end)
	for seq := uint32(1); seq <= end; seq++ {
		if seq == eventAt {
			metas[seq] = eventLedgerMeta(t, seq, testEscrowID(t))
		} else {
			metas[seq] = emptyLedgerMeta(t, seq)
		}
	}
	return metas
}

// TestIngest_BoundedRunPublishesThenSaves is the happy-path
// characterization: the run completes, the cursor lands exactly on the
// end ledger, the one event was published — and at publish time the
// cursor file did not exist yet (publish-before-save, observed from
// inside the publish itself).
func TestIngest_BoundedRunPublishesThenSaves(t *testing.T) {
	const end, eventAt = uint32(5), uint32(3)

	out := &loopSink{}
	h := newLoopHarness(t, end, eventFixture(t, end, eventAt), out)

	stateExistedAtPublish := false
	out.onPublish = func(events.Envelope) {
		if h.stateFileExists() {
			stateExistedAtPublish = true
		}
	}

	if err := Ingest(context.Background(), h.cfg); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	published := out.snapshot()
	if len(published) != 1 {
		t.Fatalf("expected exactly 1 published envelope (the event); got %d: %+v", len(published), published)
	}
	env := published[0]
	if env.Type != "event" || env.LedgerSeq != eventAt || env.ContractID != testEscrowID(t) {
		t.Errorf("unexpected envelope: type=%q ledger=%d contract=%q", env.Type, env.LedgerSeq, env.ContractID)
	}
	if env.MessageID == "" {
		t.Error("published envelope has no message id")
	}
	if stateExistedAtPublish {
		t.Error("cursor file existed at publish time: Save ran before Publish")
	}

	store, err := state.NewFileStore(h.statePath)
	if err != nil {
		t.Fatalf("opening state after run: %v", err)
	}
	defer func() { _ = store.Close() }()
	st, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("loading state after run: %v", err)
	}
	if st.LastLedgerSeq != end {
		t.Errorf("cursor = %d; want %d", st.LastLedgerSeq, end)
	}
	if st.Network != testPassphrase {
		t.Errorf("state network stamp = %q; want the passphrase", st.Network)
	}
	if len(st.EscrowContracts) != 1 || st.EscrowContracts[0] != testEscrowID(t) {
		t.Errorf("persisted watchlist = %v; want the seeded escrow", st.EscrowContracts)
	}
}

// TestIngest_PublishFailureLeavesCursorUnsaved is the invariant test: a
// fatal publish failure mid-range must abort the run BEFORE any state is
// persisted — the cursor never advances past an unpublished fact.
func TestIngest_PublishFailureLeavesCursorUnsaved(t *testing.T) {
	const end, eventAt = uint32(5), uint32(3)

	h := newLoopHarness(t, end, eventFixture(t, end, eventAt), &loopSink{failOn: 1})

	err := Ingest(context.Background(), h.cfg)
	if err == nil {
		t.Fatal("Ingest succeeded despite the publish failure")
	}
	if !errors.Is(err, errScriptedPublish) {
		t.Fatalf("Ingest error does not wrap the publish failure: %v", err)
	}
	if !strings.Contains(err.Error(), "publishing event") {
		t.Errorf("error does not identify the failed publish: %v", err)
	}

	if h.stateFileExists() {
		t.Fatal("cursor file exists after a publish failure: the cursor advanced past an unpublished fact")
	}
	if _, err := os.Stat(h.statePath[:len(h.statePath)-len(".json")] + ".watchlist.json"); err == nil {
		t.Error("watchlist file exists after a publish failure before any save")
	}
}

// TestIngest_PublishFailureOnFinalLedger pins the same invariant on the
// bounded run's LAST ledger, whose save is triggered by the isFinal rule
// rather than a checkpoint: failing its publish must still suppress the
// save.
func TestIngest_PublishFailureOnFinalLedger(t *testing.T) {
	const end = uint32(5)

	h := newLoopHarness(t, end, eventFixture(t, end, end), &loopSink{failOn: 1})

	if err := Ingest(context.Background(), h.cfg); err == nil {
		t.Fatal("Ingest succeeded despite the publish failure on the final ledger")
	}
	if h.stateFileExists() {
		t.Fatal("cursor file exists: the final ledger was saved despite its unpublished fact")
	}
}
