package contracts

import (
	"context"
	"fmt"

	"github.com/Trustless-Work/Indexer/internal/entities"
	"github.com/Trustless-Work/Indexer/internal/indexer/processors"
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

func (p *EscrowProcessor) ProcessTransaction(ctx context.Context, op *processors.TransactionOperationWrapper) ([]entities.Escrow, error) {

	if op.OperationType() != xdr.OperationTypeInvokeHostFunction {
		log.Ctx(ctx).Debugf("ContractDeployProcessor: skipping operation type %s (not InvokeHostFunction)", op.OperationType().String())
		return nil, processors.ErrInvalidOpType
	}

	invokeHostOp := op.Operation.Body.MustInvokeHostFunctionOp()
	if invokeHostOp.HostFunction.Type != xdr.HostFunctionTypeHostFunctionTypeInvokeContract {
		return nil, nil
	}

	invokeArgs := invokeHostOp.HostFunction.MustInvokeContract()
	functionName := invokeArgs.FunctionName

	contractID, err := p.getContractIDFromAddress(invokeArgs.ContractAddress)
	if err != nil {
		return nil, fmt.Errorf("extracting contract ID: %w", err)
	}

	switch functionName {
	case "tw_new_single_release_escrow":
		escrow, err := ParseSingleReleaseEscrowArgs(invokeArgs.Args, contractID, p.networkPassphrase)
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
		escrow, err := ParseMultiReleaseEscrowArgs(invokeArgs.Args, contractID, p.networkPassphrase)
		if err != nil {
			return nil, fmt.Errorf("parsing multi release escrow: %w", err)
		}

		log.Ctx(ctx).Infof("Multi Release Escrow parsed successfully!")
		log.Ctx(ctx).Infof("Contract ID (predicted): %s", escrow.ContractID)
		log.Ctx(ctx).Infof("Deployer: %s", escrow.Deployer)
		log.Ctx(ctx).Infof("Factory Contract: %s", escrow.FactoryContract)
		log.Ctx(ctx).Infof("Title: %s", escrow.Title)
		log.Ctx(ctx).Infof("Description: %s", escrow.Description)
		log.Ctx(ctx).Infof("EngagementID: %s", escrow.EngagementID)
		log.Ctx(ctx).Infof("ServiceProvider: %s", escrow.Roles.ServiceProvider)
		log.Ctx(ctx).Infof("Milestones count: %d", len(escrow.Milestones))
		for i, m := range escrow.Milestones {
			log.Ctx(ctx).Infof("  Milestone[%d]: %s, Amount: %d, Receiver: %s", i, m.Description, m.Amount, m.Receiver)
		}

		return []entities.Escrow{*escrow}, nil

	case "fund_escrow":
		action, err := ParseFundEscrowArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing fund_escrow: %w", err)
		}
		log.Ctx(ctx).Infof("Fund Escrow detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Signer: %s", action.Signer)
		log.Ctx(ctx).Infof("Amount: %d", action.Amount)
		return nil, nil

	case "release_funds":
		action, err := ParseReleaseFundsArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing release_funds: %w", err)
		}
		log.Ctx(ctx).Infof("Release Funds detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Release Signer: %s", action.ReleaseSigner)
		return nil, nil

	case "update_escrow":
		action, err := ParseUpdateEscrowArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing update_escrow: %w", err)
		}
		log.Ctx(ctx).Infof("Update Escrow detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Platform Address: %s", action.PlatformAddress)
		log.Ctx(ctx).Infof("Updated Title: %s", action.EscrowProperties.Title)
		return nil, nil

	case "extend_contract_ttl":
		action, err := ParseExtendContractTTLArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing extend_contract_ttl: %w", err)
		}
		log.Ctx(ctx).Infof("Extend Contract TTL detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Platform Address: %s", action.PlatformAddress)
		log.Ctx(ctx).Infof("Ledgers to Extend: %d", action.LedgersToExtend)
		return nil, nil

	case "change_milestone_status":
		action, err := ParseChangeMilestoneStatusArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing change_milestone_status: %w", err)
		}
		log.Ctx(ctx).Infof("Change Milestone Status detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Milestone Index: %d", action.MilestoneIndex)
		log.Ctx(ctx).Infof("New Status: %s", action.NewStatus)
		log.Ctx(ctx).Infof("New Evidence: %s", action.NewEvidence)
		log.Ctx(ctx).Infof("Service Provider: %s", action.ServiceProvider)
		return nil, nil

	case "approve_milestone":
		action, err := ParseApproveMilestoneArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing approve_milestone: %w", err)
		}
		log.Ctx(ctx).Infof("Approve Milestone detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Milestone Index: %d", action.MilestoneIndex)
		log.Ctx(ctx).Infof("Approver: %s", action.Approver)
		return nil, nil

	case "resolve_dispute":
		action, err := ParseResolveDisputeArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing resolve_dispute: %w", err)
		}
		log.Ctx(ctx).Infof("Resolve Dispute detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Dispute Resolver: %s", action.DisputeResolver)
		for addr, amount := range action.Distributions {
			log.Ctx(ctx).Infof("  Distribution: %s -> %d", addr, amount)
		}
		return nil, nil

	case "dispute_escrow":
		action, err := ParseDisputeEscrowArgs(invokeArgs.Args, contractID)
		if err != nil {
			return nil, fmt.Errorf("parsing dispute_escrow: %w", err)
		}
		log.Ctx(ctx).Infof("Dispute Escrow detected!")
		log.Ctx(ctx).Infof("Escrow Contract: %s", action.ContractID)
		log.Ctx(ctx).Infof("Signer: %s", action.Signer)
	case "get_escrow":
		log.Ctx(ctx).Infof("Escrow query parsed successfully!")
		log.Ctx(ctx).Infof("Function: get_escrow")
		log.Ctx(ctx).Infof("Contract invoked: %s", factoryContractID)
		return nil, nil

	case "get_escrow_by_contract_id":
		contractID, err := parseGetEscrowByContractIDArgs(invokeArgs.Args)
		if err != nil {
			return nil, fmt.Errorf("parsing get_escrow_by_contract_id args: %w", err)
		}
		log.Ctx(ctx).Infof("Escrow query parsed successfully!")
		log.Ctx(ctx).Infof("Function: get_escrow_by_contract_id")
		log.Ctx(ctx).Infof("Contract invoked: %s", factoryContractID)
		log.Ctx(ctx).Infof("Contract ID: %s", contractID)
		return nil, nil

	case "get_multiple_escrow_balances":
		addresses, err := parseGetMultipleEscrowBalancesArgs(invokeArgs.Args)
		if err != nil {
			return nil, fmt.Errorf("parsing get_multiple_escrow_balances args: %w", err)
		}
		log.Ctx(ctx).Infof("Escrow query parsed successfully!")
		log.Ctx(ctx).Infof("Function: get_multiple_escrow_balances")
		log.Ctx(ctx).Infof("Contract invoked: %s", factoryContractID)
		log.Ctx(ctx).Infof("Addresses: %v", addresses)
		return nil, nil

	default:
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
