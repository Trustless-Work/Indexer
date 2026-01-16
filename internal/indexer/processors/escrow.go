package processors

import (
	"context"

	"github.com/Trustless-Work/Indexer/internal/indexer/types"
	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ContractDeployProcessor emits state changes for contract deployments.
type EscrowProcessor struct {
	networkPassphrase string
}

func NewEscrowProcessor(networkPassphrase string) *EscrowProcessor {
	return &EscrowProcessor{
		networkPassphrase: networkPassphrase,
	}
}

func (p *EscrowProcessor) Name() string {
	return "initialize_escrow"
}

func (p *EscrowProcessor) ProcessOperation(ctx context.Context, op *TransactionOperationWrapper) ([]types.StateChange, error) {

	if op.OperationType() != xdr.OperationTypeInvokeHostFunction {
		log.Ctx(ctx).Debugf("ContractDeployProcessor: skipping operation type %s (not InvokeHostFunction)", op.OperationType().String())
		return nil, ErrInvalidOpType
	}
	invokeHostOp := op.Operation.Body.MustInvokeHostFunctionOp()

	//opID := op.ID()
	//builder := NewStateChangeBuilder(op.Transaction.Ledger.LedgerSequence(), op.LedgerClosed.Unix(), op.Transaction.Hash.HexString(), op.TransactionID()).
	//	WithOperationID(opID).
	//	WithCategory(types.StateChangeCategoryAccount).
	//	WithReason(types.StateChangeReasonCreate)

	deployedContractsMap := map[string]types.StateChange{}

	//processCreate := func(fromAddr xdr.ContractIdPreimageFromAddress) error {
	//	contractID, err := calculateContractID(p.networkPassphrase, fromAddr)
	//	if err != nil {
	//		return fmt.Errorf("calculating contract ID: %w", err)
	//	}
	//	deployerAddr, err := fromAddr.Address.String()
	//	if err != nil {
	//		return fmt.Errorf("deployer address to string: %w", err)
	//	}
	//
	//	stateChange := builder.Clone().WithAccount(contractID).WithDeployer(deployerAddr).Build()
	//	deployedContractsMap[contractID] = stateChange
	//
	//	return nil
	//}

	hf := invokeHostOp.HostFunction
	switch hf.Type {
	case xdr.HostFunctionTypeHostFunctionTypeInvokeContract:
		cc := hf.MustInvokeContract()

		switch cc.FunctionName {
		case "tw_new_single_release_escrow":
			log.Ctx(ctx).Infof("Single Release Escrow Finned!")

		case "tw_new_multi_release_escrow":
			log.Ctx(ctx).Infof("Multi Release Escrow Finned!")
		}

	}

	stateChanges := make([]types.StateChange, 0, len(deployedContractsMap))
	for _, sc := range deployedContractsMap {
		stateChanges = append(stateChanges, sc)
	}

	if len(stateChanges) > 0 {
		contractIDs := make([]string, 0, len(stateChanges))
		for contractID := range deployedContractsMap {
			contractIDs = append(contractIDs, contractID)
		}
	}

	return stateChanges, nil
}
