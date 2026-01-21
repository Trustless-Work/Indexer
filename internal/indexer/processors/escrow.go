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

func (p *EscrowProcessor) Name() string {
	return "initialize_escrow"
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
		escrow, err := ParseSingleReleaseEscrowArgs(invokeArgs.Args, factoryContractID, p.networkPassphrase)
		if err != nil {
			return nil, fmt.Errorf("parsing single release escrow: %w", err)
		}

		log.Ctx(ctx).Infof("Single Release Escrow parsed successfully!")
		log.Ctx(ctx).Infof("Contract ID (predicted): %s", escrow.ContractID)
		log.Ctx(ctx).Infof("Deployer: %s", escrow.Deployer)
		log.Ctx(ctx).Infof("Factory Contract: %s", escrow.FactoryContract)
		log.Ctx(ctx).Infof("Title: %s", escrow.Title)
		log.Ctx(ctx).Infof("Description: %s", escrow.Description)
		log.Ctx(ctx).Infof("Amount: %d", escrow.Amount)
		log.Ctx(ctx).Infof("EngagementID: %s", escrow.EngagementID)
		log.Ctx(ctx).Infof("ServiceProvider: %s", escrow.Roles.ServiceProvider)
		log.Ctx(ctx).Infof("Receiver: %s", escrow.Roles.Receiver)
		if len(escrow.Milestones) > 0 {
			log.Ctx(ctx).Infof("Milestones[0]: %s", escrow.Milestones[0].Description)
		}

		return []entities.Escrow{*escrow}, nil

	case "tw_new_multi_release_escrow":
		// TODO: Implementar parser para multi release escrow
		log.Ctx(ctx).Infof("Multi Release Escrow detected - not yet implemented")
		log.Ctx(ctx).Infof("Multi Release Args: %v", invokeArgs.Args)
		return nil, nil

	default:
		// No es una función que nos interese
		return nil, nil
	}
}

func (p *EscrowProcessor) getContractIDFromAddress(addr xdr.ScAddress) (string, error) {
	if addr.Type != xdr.ScAddressTypeScAddressTypeContract {
		return "", fmt.Errorf("not a contract address")
	}

	contractHash := addr.MustContractId()
	return strkey.Encode(strkey.VersionByteContract, contractHash[:])
}
