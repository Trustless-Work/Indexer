package ingest

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Trustless-Work/Indexer/internal/services"
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
	StartLedger       uint32
	EndLedger         uint32
	RPCURL            string
	NetworkPassphrase string
	GetLedgersLimit   int
	LedgerBackendType LedgerBackendType
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

	ingestService, err := services.NewIngestService(rpcService, ledgerBackend, cfg.GetLedgersLimit, cfg.NetworkPassphrase)
	if err != nil {
		return nil, fmt.Errorf("instantiating ingest service: %w", err)
	}

	return ingestService, nil
}
