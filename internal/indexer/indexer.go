package indexer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Trustless-Work/Indexer/internal/indexer/types"
	"github.com/alitto/pond/v2"

	//"github.com/Trustless-Work/Indexer/internal/indexer/types"
	set "github.com/deckarep/golang-set/v2"
	"github.com/stellar/go-stellar-sdk/ingest"
)

type IndexerBufferInterface interface {
	PushTransaction(participant string, transaction types.Transaction)
	PushOperation(participant string, operation types.Operation, transaction types.Transaction)
	PushStateChange(transaction types.Transaction, operation types.Operation, stateChange types.StateChange)
	GetTransactionsParticipants() map[string]set.Set[string]
	GetOperationsParticipants() map[int64]set.Set[string]
	GetAllParticipants() []string
	GetNumberOfTransactions() int
	GetNumberOfOperations() int
	GetTransactions() []types.Transaction
	GetOperations() []types.Operation
	GetStateChanges() []types.StateChange
	GetTrustlineChanges() []types.TrustlineChange
	GetContractChanges() []types.ContractChange
	PushContractChange(contractChange types.ContractChange)
	PushTrustlineChange(trustlineChange types.TrustlineChange)
	MergeBuffer(other IndexerBufferInterface)
}

type Indexer struct {
	pool pond.Pool
}

func NewIndexer(networkPasspharse string) *Indexer {
	return &Indexer{}
}

// ProcessLedgerTransactions processes all transactions in a ledger in parallel.
// It collects transaction data (participants, operations, state changes) and populates the buffer in a single pass.
// Returns the total participant count for metrics.
func (i *Indexer) ProcessLedgerTransactions(ctx context.Context, transactions []ingest.LedgerTransaction, ledgerBuffer IndexerBufferInterface) (int, error) {
	group := i.pool.NewGroupContext(ctx)

	txnBuffers := make([]*IndexerBuffer, len(transactions))
	participantCounts := make([]int, len(transactions))
	var errs []error
	errMu := sync.Mutex{}

	for idx, tx := range transactions {
		index := idx
		tx := tx
		group.Submit(func() {
			buffer := NewIndexerBuffer()
			count, err := i.processTransaction(ctx, tx, buffer)
			if err != nil {
				errMu.Lock()
				errs = append(errs, fmt.Errorf("processing transaction at ledger=%d tx=%d: %w", tx.Ledger.LedgerSequence(), tx.Index, err))
				errMu.Unlock()
				return
			}
			txnBuffers[index] = buffer
			participantCounts[index] = count
		})
	}

	if err := group.Wait(); err != nil {
		return 0, fmt.Errorf("waiting for transaction processing: %w", err)
	}
	if len(errs) > 0 {
		return 0, fmt.Errorf("processing transactions: %w", errors.Join(errs...))
	}

	// Merge buffers and count participants
	totalParticipants := 0
	for idx, buffer := range txnBuffers {
		ledgerBuffer.MergeBuffer(buffer)
		totalParticipants += participantCounts[idx]
	}

	return totalParticipants, nil
}

func (i *Indexer) processTransaction(ctx context.Context, tx ingest.LedgerTransaction, buffer *IndexerBuffer) (int, error) {
	fmt.Println("Estamos trabajando en processTransaction...")
	return 0, nil
}
