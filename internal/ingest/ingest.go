package ingest

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Trustless-Work/Indexer/internal/services"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
)

// LedgerBackendType represents the type of ledger backend to use
type LedgerBackendType string

const (
	// LedgerBackendTypeRPC uses RPC to fetch ledgers
	LedgerBackendTypeRPC LedgerBackendType = "rpc"
	// LedgerBackendTypeDatastore uses cloud storage (S3/GCS) to fetch ledgers
	LedgerBackendTypeDatastore LedgerBackendType = "datastore"
)

type Config struct {
	IngestionMode          string
	LatestLedgerCursorName string
	OldestLedgerCursorName string
	StartLedger            uint32
	EndLedger              uint32
	RPCURL                 string
	Network                string
	NetworkPassphrase      string
	GetLedgersLimit        int
	LedgerBackendType      LedgerBackendType
	// SkipTxMeta skips storing transaction metadata (meta_xdr) to reduce storage space
	SkipTxMeta bool
	// SkipTxEnvelope skips storing transaction envelope (envelope_xdr) to reduce storage space
	SkipTxEnvelope bool
	// EnableParticipantFiltering controls whether to filter ingested data by pre-registered accounts.
	// When false (default), all data is stored. When true, only data for pre-registered accounts is stored.
	EnableParticipantFiltering bool
	// BackfillWorkers limits concurrent batch processing during backfill.
	// Defaults to runtime.NumCPU(). Lower values reduce RAM usage.
	BackfillWorkers int
	// BackfillBatchSize is the number of ledgers processed per batch during backfill.
	// Defaults to 250. Lower values reduce RAM usage at cost of more DB transactions.
	BackfillBatchSize int
	// BackfillDBInsertBatchSize is the number of ledgers to process before flushing to DB.
	// Defaults to 50. Lower values reduce RAM usage at cost of more DB transactions.
	BackfillDBInsertBatchSize int
	// CatchupThreshold is the number of ledgers behind network tip that triggers fast catchup.
	// Defaults to 100.
	CatchupThreshold int
}

func Ingest(cfg Config) error {
	ctx := context.Background()

	ingestService, err := setupDeps(cfg)
	if err != nil {
		fmt.Println("Failed to setup deps: ", err)
	}

	if err = ingestService.Run(ctx, cfg.StartLedger, 0); err != nil {
		fmt.Println("Failed to start ingest: ", err)
	}

	return nil
}

func setupDeps(cfg Config) (services.IngestService, error) {

	httpClient := &http.Client{Timeout: 30 * time.Second}
	rpcService, err := services.NewRPCService(cfg.RPCURL, cfg.NetworkPassphrase, httpClient)
	if err != nil {
		return nil, fmt.Errorf("instantiating rpc service: %w", err)
	}

	ledgerBackend, err := NewLedgerBackend(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("creating ledger backend: %w", err)
	}

	// Create a factory function for parallel backfill (each batch needs its own backend)
	ledgerBackendFactory := func(ctx context.Context) (ledgerbackend.LedgerBackend, error) {
		return NewLedgerBackend(ctx, cfg)
	}

	ingestService, err := services.NewIngestService((services.IngestServiceConfig{
		IngestionMode:              cfg.IngestionMode,
		LatestLedgerCursorName:     cfg.LatestLedgerCursorName,
		OldestLedgerCursorName:     cfg.OldestLedgerCursorName,
		RPCService:                 rpcService,
		LedgerBackend:              ledgerBackend,
		LedgerBackendFactory:       ledgerBackendFactory,
		GetLedgersLimit:            cfg.GetLedgersLimit,
		Network:                    cfg.Network,
		NetworkPassphrase:          cfg.NetworkPassphrase,
		SkipTxMeta:                 cfg.SkipTxMeta,
		SkipTxEnvelope:             cfg.SkipTxEnvelope,
		EnableParticipantFiltering: cfg.EnableParticipantFiltering,
		BackfillWorkers:            cfg.BackfillWorkers,
		BackfillBatchSize:          cfg.BackfillBatchSize,
		BackfillDBInsertBatchSize:  cfg.BackfillDBInsertBatchSize,
		CatchupThreshold:           cfg.CatchupThreshold,
	}))
	if err != nil {
		return nil, fmt.Errorf("instantiating ingest service: %w", err)
	}

	return ingestService, nil
}
