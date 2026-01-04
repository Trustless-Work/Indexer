package ingest

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
	"github.com/stellar/go-stellar-sdk/support/log"
)

func NewLedgerBackend(ctx context.Context, cfg Config) (ledgerbackend.LedgerBackend, error) {
	switch cfg.LedgerBackendType {
	case LedgerBackendTypeDatastore:
		//return newDatastoreLedgerBackend(ctx, cfg)
		// TODO: Implement the DatastoreBackend
		return nil, fmt.Errorf("datastore backend not supported yet")
	case LedgerBackendTypeRPC:
		return newRPCLedgerBackend(cfg)
	default:
		return nil, fmt.Errorf("unsupported ledger backend type: %s", cfg.LedgerBackendType)
	}
}

func newRPCLedgerBackend(cfg Config) (ledgerbackend.LedgerBackend, error) {
	backend := ledgerbackend.NewRPCLedgerBackend(ledgerbackend.RPCLedgerBackendOptions{
		RPCServerURL: cfg.RPCURL,
		BufferSize:   uint32(cfg.GetLedgersLimit),
	})
	log.Infof("Using RPCLedgerBackend for ledger ingestion with buffer size %d", cfg.GetLedgersLimit)
	return backend, nil
}
