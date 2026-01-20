package entities

type Escrow struct {
	FactoryContract  string
	DeployerSalt     string
	WasmHash         string
	InitFunction     string
	Amount           uint64
	Description      string
	EngagementID     string
	Title            string
	PlatformFee      uint32
	ReceiverMemo     string
	Flags            EscrowFlags
	Roles            EscrowRoles
	Milestones       []Milestone
	TrustlineAddress string
}

type EscrowFlags struct {
	Disputed bool
	Released bool
	Resolved bool
}

type EscrowRoles struct {
	ServiceProvider string
	Receiver        string
	Approver        string
	ReleaseSigner   string
	DisputeResolver string
	PlatformAddress string
}

type Milestone struct {
	Description string
	Status      string
	Approved    bool
	Evidence    string
}
