package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Trustless-Work/Indexer/internal/indexer"
	"github.com/Trustless-Work/Indexer/internal/utils"
	"github.com/alitto/pond/v2"
	"github.com/stellar/go-stellar-sdk/ingest"
	"github.com/stellar/go-stellar-sdk/ingest/ledgerbackend"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"
)

type IngestService interface {
	Run(ctx context.Context, startLedger uint32, endLedger uint32) error
}

var _ IngestService = (*ingestService)(nil)

type ingestService struct {
	rpcService        RPCService
	ledgerBackend     ledgerbackend.LedgerBackend
	networkPassphrase string
	getLedgersLimit   int
	ledgerIndexer     *indexer.Indexer
}

func NewIngestService(
	rpcService RPCService,
	ledgerBackend ledgerbackend.LedgerBackend,
	getLedgersLimit int,
	networkPassphrase string,
) (*ingestService, error) {
	// Create worker pool for the ledger indexer (parallel transaction processing within a ledger)
	ledgerIndexerPool := pond.NewPool(0)

	return &ingestService{
		rpcService:        rpcService,
		ledgerBackend:     ledgerBackend,
		networkPassphrase: networkPassphrase,
		getLedgersLimit:   getLedgersLimit,
		ledgerIndexer:     indexer.NewIndexer(networkPassphrase, ledgerIndexerPool),
	}, nil
}

func (m *ingestService) Run(ctx context.Context, startLedger uint32, endLedger uint32) error {

	// Prepare backend range
	err := m.prepareBackendRange(ctx, startLedger, endLedger)
	if err != nil {
		return fmt.Errorf("preparing backend range: %w", err)
	}
	currentLedger := startLedger
	log.Ctx(ctx).Infof("Starting ingestion loop from ledger: %d", currentLedger)
	for endLedger == 0 || currentLedger < endLedger {
		ledgerMeta, ledgerErr := m.ledgerBackend.GetLedger(ctx, currentLedger)
		if ledgerErr != nil {
			if endLedger > 0 && currentLedger > endLedger {
				log.Ctx(ctx).Infof("Backfill complete: processed ledgers %d to %d", startLedger, endLedger)
				return nil
			}
			log.Ctx(ctx).Warnf("Error fetching ledger %d: %v, retrying...", currentLedger, ledgerErr)
			time.Sleep(time.Second)
			continue
		}

		totalStart := time.Now()
		if processErr := m.processLedger(ctx, ledgerMeta); processErr != nil {
			return fmt.Errorf("processing ledger %d: %w", currentLedger, processErr)
		}

		log.Ctx(ctx).Infof("Processed ledger %d in %v", currentLedger, time.Since(totalStart))
		currentLedger++

	}
	return nil
}

// prepareBackendRange prepares the ledger backend with the appropriate range type.
// Returns the operating mode (live streaming vs backfill).
func (m *ingestService) prepareBackendRange(ctx context.Context, startLedger, endLedger uint32) error {
	var ledgerRange ledgerbackend.Range
	if endLedger == 0 {
		ledgerRange = ledgerbackend.UnboundedRange(startLedger)
		log.Ctx(ctx).Infof("Prepared backend with unbounded range starting from ledger %d", startLedger)
	} else {
		ledgerRange = ledgerbackend.BoundedRange(startLedger, endLedger)
		log.Ctx(ctx).Infof("Prepared backend with bounded range [%d, %d]", startLedger, endLedger)
	}

	if err := m.ledgerBackend.PrepareRange(ctx, ledgerRange); err != nil {
		return fmt.Errorf("preparing datastore backend unbounded range from %d: %w", startLedger, err)
	}
	return nil
}

// processLedger processes a single ledger through all ingestion phases.
// Phase 1: Get transactions from ledger
// Phase 2: Process transactions using Indexer (parallel within ledger)
// Phase 3: Insert all data into DB
func (m *ingestService) processLedger(ctx context.Context, ledgerMeta xdr.LedgerCloseMeta) error {
	ledgerSeq := ledgerMeta.LedgerSequence()

	// Phase 1: Get transactions from ledger
	transactions, err := m.getLedgerTransactions(ctx, ledgerMeta)
	if err != nil {
		return fmt.Errorf("getting transactions for ledger %d: %w", ledgerSeq, err)
	}

	// Phase 2: Process transactions using Indexer (parallel within ledger)
	buffer := indexer.NewIndexerBuffer()
	_, err = m.ledgerIndexer.ProcessLedgerTransactions(ctx, transactions, buffer)
	if err != nil {
		return fmt.Errorf("processing transactions for ledger %d: %w", ledgerSeq, err)
	}

	// Phase 3: Insert all data into DB
	//if err := m.ingestProcessedData(ctx, buffer); err != nil {
	//	return fmt.Errorf("ingesting processed data for ledger %d: %w", ledgerSeq, err)
	//}

	return nil
}

func (m *ingestService) getLedgerTransactions(ctx context.Context, xdrLedgerCloseMeta xdr.LedgerCloseMeta) ([]ingest.LedgerTransaction, error) {
	ledgerTxReader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(m.networkPassphrase, xdrLedgerCloseMeta)
	if err != nil {
		return nil, fmt.Errorf("creating ledger transaction reader: %w", err)
	}
	defer utils.DeferredClose(ctx, ledgerTxReader, "closing ledger transaction reader")

	transactions := make([]ingest.LedgerTransaction, 0)
	for {
		tx, err := ledgerTxReader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("reading ledger: %w", err)
		}

		transactions = append(transactions, tx)
	}

	return transactions, nil
}
