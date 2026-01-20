package processors

import (
	"context"
	"fmt"

	"github.com/Trustless-Work/Indexer/internal/entities"
	"github.com/stellar/go-stellar-sdk/strkey"
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

func (p *EscrowProcessor) ProcessTransaction(ctx context.Context, op *TransactionOperationWrapper) ([]entities.Escrow, error) {

	if op.OperationType() != xdr.OperationTypeInvokeHostFunction {
		log.Ctx(ctx).Debugf("ContractDeployProcessor: skipping operation type %s (not InvokeHostFunction)", op.OperationType().String())
		return nil, ErrInvalidOpType
	}

	invokeHostOp := op.Operation.Body.MustInvokeHostFunctionOp()
	if invokeHostOp.HostFunction.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract {
		return nil, nil
	}

	invokeArgs := invokeHostOp.HostFunction.MustInvokeContract()
	functionName := invokeArgs.FunctionName

	// Extraer el contract ID del factory
	factoryContractID, err := p.getContractIDFromAddress(invokeArgs.ContractAddress)
	if err != nil {
		return nil, fmt.Errorf("extracting factory contract ID: %w", err)
	}

	//logger := log.Ctx(ctx).WithField("factory_contract", factoryContractID).WithField("function", functionName)

	// Procesar según la función
	switch functionName {
	case "tw_new_single_release_escrow":
		//return p.processSingleReleaseEscrow(ctx, logger, op, factoryContractID, invokeArgs.Args)
		log.Ctx(ctx).Infof("Single Release Escrow Finned!")
		log.Ctx(ctx).Infof("Factory Contract ID: %s", factoryContractID)
		log.Ctx(ctx).Infof("Escrow Args: %v", invokeArgs.Args)

		// TODO: Implementar el parseo de un slice a una estructura

	case "tw_new_multi_release_escrow":
		//return p.processMultiReleaseEscrow(ctx, logger, op, factoryContractID, invokeArgs.Args)
		log.Ctx(ctx).Infof("Multi Release Escrow Finned!")
		log.Ctx(ctx).Infof("Factory Contract ID: %s", factoryContractID)

		// TODO: Implementar el parseo de un slice a una estructura

	default:
		// No es una función que nos interese
		return nil, nil
	}

	return nil, nil
}

func (p *EscrowProcessor) Name() string {
	return "initialize_escrow"
}

func (p *EscrowProcessor) getContractIDFromAddress(addr xdr.ScAddress) (string, error) {
	if addr.Type != xdr.ScAddressTypeScAddressTypeContract {
		return "", fmt.Errorf("not a contract address")
	}

	contractHash := addr.MustContractId()
	return strkey.Encode(strkey.VersionByteContract, contractHash[:])
}
