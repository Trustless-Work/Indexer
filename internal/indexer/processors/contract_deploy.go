package processors

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/support/log"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/Trustless-Work/Indexer/internal/indexer/types"
)

// ContractDeployProcessor emits state changes for contract deployments.
type ContractDeployProcessor struct {
	networkPassphrase string
}

func NewContractDeployProcessor(networkPassphrase string) *ContractDeployProcessor {
	return &ContractDeployProcessor{
		networkPassphrase: networkPassphrase,
	}
}

func (p *ContractDeployProcessor) Name() string {
	return "contract_deploy"
}

// ProcessOperation emits a state change for each contract deployment (including subinvocations).
func (p *ContractDeployProcessor) ProcessOperation(ctx context.Context, op *TransactionOperationWrapper) ([]types.StateChange, error) {

	if op.OperationType() != xdr.OperationTypeInvokeHostFunction {
		log.Ctx(ctx).Debugf("ContractDeployProcessor: skipping operation type %s (not InvokeHostFunction)", op.OperationType().String())
		return nil, ErrInvalidOpType
	}
	invokeHostOp := op.Operation.Body.MustInvokeHostFunctionOp()

	opID := op.ID()
	builder := NewStateChangeBuilder(op.Transaction.Ledger.LedgerSequence(), op.LedgerClosed.Unix(), op.Transaction.Hash.HexString(), op.TransactionID()).
		WithOperationID(opID).
		WithCategory(types.StateChangeCategoryAccount).
		WithReason(types.StateChangeReasonCreate)

	deployedContractsMap := map[string]types.StateChange{}

	processCreate := func(fromAddr xdr.ContractIdPreimageFromAddress) error {
		contractID, err := calculateContractID(p.networkPassphrase, fromAddr)
		if err != nil {
			return fmt.Errorf("calculating contract ID: %w", err)
		}
		deployerAddr, err := fromAddr.Address.String()
		if err != nil {
			return fmt.Errorf("deployer address to string: %w", err)
		}

		stateChange := builder.Clone().WithAccount(contractID).WithDeployer(deployerAddr).Build()
		deployedContractsMap[contractID] = stateChange

		return nil
	}

	var walkInvocation func(inv xdr.SorobanAuthorizedInvocation) error
	walkInvocation = func(inv xdr.SorobanAuthorizedInvocation) error {
		switch inv.Function.Type {
		case xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeCreateContractHostFn:
			cc := inv.Function.MustCreateContractHostFn()
			if cc.ContractIdPreimage.Type == xdr.ContractIdPreimageTypeContractIdPreimageFromAddress {
				if err := processCreate(cc.ContractIdPreimage.MustFromAddress()); err != nil {
					return err
				}
			}
		case xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeCreateContractV2HostFn:
			cc := inv.Function.MustCreateContractV2HostFn()
			if cc.ContractIdPreimage.Type == xdr.ContractIdPreimageTypeContractIdPreimageFromAddress {
				if err := processCreate(cc.ContractIdPreimage.MustFromAddress()); err != nil {
					return err
				}
			}
		case xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn:
			// no-op
		}
		for _, sub := range inv.SubInvocations {
			if err := walkInvocation(sub); err != nil {
				return err
			}
		}
		return nil
	}

	hf := invokeHostOp.HostFunction
	switch hf.Type {
	case xdr.HostFunctionTypeHostFunctionTypeCreateContract:
		cc := hf.MustCreateContract()
		if cc.ContractIdPreimage.Type == xdr.ContractIdPreimageTypeContractIdPreimageFromAddress {
			if err := processCreate(cc.ContractIdPreimage.MustFromAddress()); err != nil {
				return nil, err
			}
		} else {
			log.Ctx(ctx).Debugf("CreateContract with non-FromAddress preimage type: %s (OpID: %d)", cc.ContractIdPreimage.Type.String(), opID)
		}
	case xdr.HostFunctionTypeHostFunctionTypeCreateContractV2:
		cc := hf.MustCreateContractV2()
		if cc.ContractIdPreimage.Type == xdr.ContractIdPreimageTypeContractIdPreimageFromAddress {
			if err := processCreate(cc.ContractIdPreimage.MustFromAddress()); err != nil {
				return nil, err
			}
		} else {
			log.Ctx(ctx).Debugf("CreateContractV2 with non-FromAddress preimage type: %s (OpID: %d)", cc.ContractIdPreimage.Type.String(), opID)
		}
	case xdr.HostFunctionTypeHostFunctionTypeUploadContractWasm:
		log.Ctx(ctx).Debugf("InvokeHostFunction type: UploadContractWasm (OpID: %d)", opID)
	case xdr.HostFunctionTypeHostFunctionTypeInvokeContract:
		cc := hf.MustInvokeContract()
		log.Ctx(ctx).Infof("Args: %v", cc.Args)
		// TODO: Hacer una correcta impresion de los Args de nuestros escrows.
		// TODO: Crear los structs correspondientes a los Escrows.
		// TODO: Almacenar los Escrows que se vayan encontrando.

		//log.Ctx(ctx).Debugf("InvokeHostFunction type: InvokeContract (OpID: %d)", opID)

	}

	for _, auth := range invokeHostOp.Auth {
		if err := walkInvocation(auth.RootInvocation); err != nil {
			return nil, err
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
		log.Infof("📊 ContractDeployProcessor Summary - OpID: %d | TxHash: %s | Total Contracts: %d | IDs: %v",
			opID,
			op.Transaction.Hash.HexString(),
			len(stateChanges),
			contractIDs,
		)
	}

	return stateChanges, nil
}
