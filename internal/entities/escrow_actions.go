package entities

// FundEscrowAction represents the parsed payload of a fund_escrow call
type FundEscrowAction struct {
	ContractID     string
	Signer         string
	ExpectedEscrow *Escrow
	Amount         uint64
}

// ReleaseFundsAction represents the parsed payload of a release_funds call
type ReleaseFundsAction struct {
	ContractID    string
	ReleaseSigner string
}

// UpdateEscrowAction represents the parsed payload of an update_escrow call
type UpdateEscrowAction struct {
	ContractID       string
	PlatformAddress  string
	EscrowProperties *Escrow
}

// ExtendContractTTLAction represents the parsed payload of an extend_contract_ttl call
type ExtendContractTTLAction struct {
	ContractID      string
	PlatformAddress string
	LedgersToExtend uint32
}

// ChangeMilestoneStatusAction represents the parsed payload of a change_milestone_status call
type ChangeMilestoneStatusAction struct {
	ContractID      string
	MilestoneIndex  uint64
	NewStatus       string
	NewEvidence     string
	ServiceProvider string
}

// ApproveMilestoneAction represents the parsed payload of an approve_milestone call
type ApproveMilestoneAction struct {
	ContractID     string
	MilestoneIndex uint64
	Approver       string
}

// ResolveDisputeAction represents the parsed payload of a resolve_dispute call
type ResolveDisputeAction struct {
	ContractID      string
	DisputeResolver string
	Distributions   map[string]uint64
}

// DisputeEscrowAction represents the parsed payload of a dispute_escrow call
type DisputeEscrowAction struct {
	ContractID string
	Signer     string
}
