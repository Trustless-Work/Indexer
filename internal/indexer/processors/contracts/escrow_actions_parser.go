package contracts

import (
	"fmt"

	"github.com/Trustless-Work/Indexer/internal/entities"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ParseFundEscrowArgs parses arguments for fund_escrow(signer, expected_escrow, amount)
func ParseFundEscrowArgs(args []xdr.ScVal, contractID string) (*entities.FundEscrowAction, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("fund_escrow: insufficient arguments: expected 3, got %d", len(args))
	}

	signer, err := extractAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("fund_escrow: parsing signer address: %w", err)
	}

	escrow := &entities.Escrow{}
	if err := parseEscrowData(args[1], escrow); err != nil {
		return nil, fmt.Errorf("fund_escrow: parsing expected_escrow: %w", err)
	}

	amount, err := extractI128FromScVal(args[2])
	if err != nil {
		return nil, fmt.Errorf("fund_escrow: parsing amount: %w", err)
	}

	return &entities.FundEscrowAction{
		ContractID:     contractID,
		Signer:         signer,
		ExpectedEscrow: escrow,
		Amount:         amount,
	}, nil
}

// ParseReleaseFundsArgs parses arguments for release_funds(release_signer)
func ParseReleaseFundsArgs(args []xdr.ScVal, contractID string) (*entities.ReleaseFundsAction, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("release_funds: insufficient arguments: expected 1, got %d", len(args))
	}

	releaseSigner, err := extractAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("release_funds: parsing release_signer address: %w", err)
	}

	return &entities.ReleaseFundsAction{
		ContractID:    contractID,
		ReleaseSigner: releaseSigner,
	}, nil
}

// ParseUpdateEscrowArgs parses arguments for update_escrow(platform_address, escrow_properties)
func ParseUpdateEscrowArgs(args []xdr.ScVal, contractID string) (*entities.UpdateEscrowAction, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("update_escrow: insufficient arguments: expected 2, got %d", len(args))
	}

	platformAddress, err := extractAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("update_escrow: parsing platform_address: %w", err)
	}

	escrow := &entities.Escrow{}
	if err := parseEscrowData(args[1], escrow); err != nil {
		return nil, fmt.Errorf("update_escrow: parsing escrow_properties: %w", err)
	}

	return &entities.UpdateEscrowAction{
		ContractID:       contractID,
		PlatformAddress:  platformAddress,
		EscrowProperties: escrow,
	}, nil
}

// ParseExtendContractTTLArgs parses arguments for extend_contract_ttl(platform_address, ledgers_to_extend)
func ParseExtendContractTTLArgs(args []xdr.ScVal, contractID string) (*entities.ExtendContractTTLAction, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("extend_contract_ttl: insufficient arguments: expected 2, got %d", len(args))
	}

	platformAddress, err := extractAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("extend_contract_ttl: parsing platform_address: %w", err)
	}

	ledgersToExtend, err := extractU32FromScVal(args[1])
	if err != nil {
		return nil, fmt.Errorf("extend_contract_ttl: parsing ledgers_to_extend: %w", err)
	}

	return &entities.ExtendContractTTLAction{
		ContractID:      contractID,
		PlatformAddress: platformAddress,
		LedgersToExtend: ledgersToExtend,
	}, nil
}

// ParseChangeMilestoneStatusArgs parses arguments for
// change_milestone_status(milestone_index, new_status, new_evidence, service_provider)
func ParseChangeMilestoneStatusArgs(args []xdr.ScVal, contractID string) (*entities.ChangeMilestoneStatusAction, error) {
	if len(args) < 4 {
		return nil, fmt.Errorf("change_milestone_status: insufficient arguments: expected 4, got %d", len(args))
	}

	milestoneIndex, err := extractI128FromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("change_milestone_status: parsing milestone_index: %w", err)
	}

	newStatus, err := extractStringFromScVal(args[1])
	if err != nil {
		return nil, fmt.Errorf("change_milestone_status: parsing new_status: %w", err)
	}

	// new_evidence is Option<String> — None is represented as ScvVoid
	newEvidence := ""
	if args[2].Type != xdr.ScValTypeScvVoid {
		evidence, err := extractStringFromScVal(args[2])
		if err != nil {
			return nil, fmt.Errorf("change_milestone_status: parsing new_evidence: %w", err)
		}
		newEvidence = evidence
	}

	serviceProvider, err := extractAddressFromScVal(args[3])
	if err != nil {
		return nil, fmt.Errorf("change_milestone_status: parsing service_provider: %w", err)
	}

	return &entities.ChangeMilestoneStatusAction{
		ContractID:      contractID,
		MilestoneIndex:  milestoneIndex,
		NewStatus:       newStatus,
		NewEvidence:     newEvidence,
		ServiceProvider: serviceProvider,
	}, nil
}

// ParseApproveMilestoneArgs parses arguments for approve_milestone(milestone_index, approver)
func ParseApproveMilestoneArgs(args []xdr.ScVal, contractID string) (*entities.ApproveMilestoneAction, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("approve_milestone: insufficient arguments: expected 2, got %d", len(args))
	}

	milestoneIndex, err := extractI128FromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("approve_milestone: parsing milestone_index: %w", err)
	}

	approver, err := extractAddressFromScVal(args[1])
	if err != nil {
		return nil, fmt.Errorf("approve_milestone: parsing approver: %w", err)
	}

	return &entities.ApproveMilestoneAction{
		ContractID:     contractID,
		MilestoneIndex: milestoneIndex,
		Approver:       approver,
	}, nil
}

// ParseResolveDisputeArgs parses arguments for resolve_dispute(dispute_resolver, distributions)
func ParseResolveDisputeArgs(args []xdr.ScVal, contractID string) (*entities.ResolveDisputeAction, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("resolve_dispute: insufficient arguments: expected 2, got %d", len(args))
	}

	disputeResolver, err := extractAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("resolve_dispute: parsing dispute_resolver: %w", err)
	}

	// distributions is Map<Address, i128> — keys are Addresses, not Symbols
	entries, err := extractMapFromScVal(args[1])
	if err != nil {
		return nil, fmt.Errorf("resolve_dispute: parsing distributions map: %w", err)
	}

	distributions := make(map[string]uint64, len(entries))
	for i, entry := range entries {
		addr, err := extractAddressFromScVal(entry.Key)
		if err != nil {
			return nil, fmt.Errorf("resolve_dispute: parsing distribution address[%d]: %w", i, err)
		}
		amount, err := extractI128FromScVal(entry.Val)
		if err != nil {
			return nil, fmt.Errorf("resolve_dispute: parsing distribution amount[%d]: %w", i, err)
		}
		distributions[addr] = amount
	}

	return &entities.ResolveDisputeAction{
		ContractID:      contractID,
		DisputeResolver: disputeResolver,
		Distributions:   distributions,
	}, nil
}

// ParseDisputeEscrowArgs parses arguments for dispute_escrow(signer)
func ParseDisputeEscrowArgs(args []xdr.ScVal, contractID string) (*entities.DisputeEscrowAction, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("dispute_escrow: insufficient arguments: expected 1, got %d", len(args))
	}

	signer, err := extractAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("dispute_escrow: parsing signer address: %w", err)
	}

	return &entities.DisputeEscrowAction{
		ContractID: contractID,
		Signer:     signer,
	}, nil
}
