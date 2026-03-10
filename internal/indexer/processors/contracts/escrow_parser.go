package contracts

import (
	"encoding/hex"
	"fmt"

	"github.com/Trustless-Work/Indexer/internal/entities"
	"github.com/Trustless-Work/Indexer/internal/indexer/processors"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ParseSingleReleaseEscrowArgs parses the arguments from tw_new_single_release_escrow
// Args structure:
// [0] deployer (Address)
// [1] wasm_hash (BytesN<32>)
// [2] salt (BytesN<32>)
// [3] init_fn (Symbol)
// [4] init_args (Vec<Val>) - contains escrow data
// [5] constructor_args (Vec<Val>) - empty
func ParseSingleReleaseEscrowArgs(args []xdr.ScVal, factoryContract string, networkPassphrase string) (*entities.Escrow, error) {
	return parseEscrowArgs(args, factoryContract, networkPassphrase, entities.EscrowTypeSingleRelease)
}

// ParseMultiReleaseEscrowArgs parses the arguments from tw_new_multi_release_escrow
// Args structure is identical to single release
func ParseMultiReleaseEscrowArgs(args []xdr.ScVal, factoryContract string, networkPassphrase string) (*entities.Escrow, error) {
	return parseEscrowArgs(args, factoryContract, networkPassphrase, entities.EscrowTypeMultiRelease)
}

// parseEscrowArgs is the common function that parses escrow arguments for both single and multi release types
func parseEscrowArgs(args []xdr.ScVal, factoryContract string, networkPassphrase string, escrowType entities.EscrowType) (*entities.Escrow, error) {
	if len(args) < 5 {
		return nil, fmt.Errorf("insufficient arguments: expected at least 5, got %d", len(args))
	}

	escrow := &entities.Escrow{
		FactoryContract: factoryContract,
		EscrowType:      escrowType,
	}

	// Parse deployer address (Args[0])
	deployerAddr, err := extractScAddressFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("parsing deployer address: %w", err)
	}
	deployerStr, err := deployerAddr.String()
	if err != nil {
		return nil, fmt.Errorf("converting deployer to string: %w", err)
	}
	escrow.Deployer = deployerStr

	// Parse wasm_hash (Args[1])
	wasmHash, err := extractBytesFromScVal(args[1])
	if err != nil {
		return nil, fmt.Errorf("parsing wasm_hash: %w", err)
	}
	escrow.WasmHash = wasmHash

	// Parse salt (Args[2]) - need raw bytes for contract ID calculation
	saltBytes, err := extractRawBytesFromScVal(args[2])
	if err != nil {
		return nil, fmt.Errorf("parsing salt: %w", err)
	}
	escrow.DeployerSalt = hex.EncodeToString(saltBytes)

	// Calculate contract ID from deployer + salt + network
	contractID, err := deriveContractID(networkPassphrase, deployerAddr, saltBytes)
	if err != nil {
		return nil, fmt.Errorf("calculating contract ID: %w", err)
	}
	escrow.ContractID = contractID

	// Parse init_fn (Args[3])
	initFn, err := extractSymbolFromScVal(args[3])
	if err != nil {
		return nil, fmt.Errorf("parsing init_fn: %w", err)
	}
	escrow.InitFunction = initFn

	// Parse init_args (Args[4]) - contains the escrow data
	initArgsVec, err := extractVecFromScVal(args[4])
	if err != nil {
		return nil, fmt.Errorf("parsing init_args: %w", err)
	}

	if len(initArgsVec) == 0 {
		return nil, fmt.Errorf("init_args is empty")
	}

	// The escrow data is the first element of init_args
	if err := parseEscrowData(initArgsVec[0], escrow); err != nil {
		return nil, fmt.Errorf("parsing escrow data: %w", err)
	}

	return escrow, nil
}

// deriveContractID calculates the contract ID from deployer address and salt
func deriveContractID(networkPassphrase string, deployerAddr xdr.ScAddress, salt []byte) (string, error) {
	// Convert salt to Uint256
	var saltUint256 xdr.Uint256
	if len(salt) != 32 {
		return "", fmt.Errorf("salt must be 32 bytes, got %d", len(salt))
	}
	copy(saltUint256[:], salt)

	// Create the preimage
	fromAddress := xdr.ContractIdPreimageFromAddress{
		Address: deployerAddr,
		Salt:    saltUint256,
	}

	// Use the existing calculateContractID function
	return processors.CalculateContractID(networkPassphrase, fromAddress)
}

// extractScAddressFromScVal extracts an xdr.ScAddress from a ScVal
func extractScAddressFromScVal(val xdr.ScVal) (xdr.ScAddress, error) {
	addr, ok := val.GetAddress()
	if !ok {
		return xdr.ScAddress{}, fmt.Errorf("invalid address")
	}
	return addr, nil
}

// extractRawBytesFromScVal extracts raw bytes from a ScVal
func extractRawBytesFromScVal(val xdr.ScVal) ([]byte, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return nil, fmt.Errorf("invalid bytes")
	}
	return bytes, nil
}

// parseEscrowData parses the escrow map and populates the Escrow entity
func parseEscrowData(val xdr.ScVal, escrow *entities.Escrow) error {
	entries, err := extractMapFromScVal(val)
	if err != nil {
		return fmt.Errorf("extracting escrow map: %w", err)
	}

	// Parse amount
	if amountVal, ok := findInMap(entries, "amount"); ok {
		amount, err := extractI128FromScVal(amountVal)
		if err != nil {
			return fmt.Errorf("parsing amount: %w", err)
		}
		escrow.Amount = amount
	}

	// Parse description
	if descVal, ok := findInMap(entries, "description"); ok {
		desc, err := extractStringFromScVal(descVal)
		if err != nil {
			return fmt.Errorf("parsing description: %w", err)
		}
		escrow.Description = desc
	}

	// Parse engagement_id
	if engVal, ok := findInMap(entries, "engagement_id"); ok {
		engID, err := extractStringFromScVal(engVal)
		if err != nil {
			return fmt.Errorf("parsing engagement_id: %w", err)
		}
		escrow.EngagementID = engID
	}

	// Parse title
	if titleVal, ok := findInMap(entries, "title"); ok {
		title, err := extractStringFromScVal(titleVal)
		if err != nil {
			return fmt.Errorf("parsing title: %w", err)
		}
		escrow.Title = title
	}

	// Parse platform_fee
	if feeVal, ok := findInMap(entries, "platform_fee"); ok {
		fee, err := extractU32FromScVal(feeVal)
		if err != nil {
			return fmt.Errorf("parsing platform_fee: %w", err)
		}
		escrow.PlatformFee = fee
	}

	// Parse receiver_memo (can be string or number)
	if memoVal, ok := findInMap(entries, "receiver_memo"); ok {
		memo, err := extractStringOrNumberFromScVal(memoVal)
		if err != nil {
			return fmt.Errorf("parsing receiver_memo: %w", err)
		}
		escrow.ReceiverMemo = memo
	}

	// Parse flags
	if flagsVal, ok := findInMap(entries, "flags"); ok {
		flags, err := parseEscrowFlags(flagsVal)
		if err != nil {
			return fmt.Errorf("parsing flags: %w", err)
		}
		escrow.Flags = flags
	}

	// Parse roles
	if rolesVal, ok := findInMap(entries, "roles"); ok {
		roles, err := parseEscrowRoles(rolesVal)
		if err != nil {
			return fmt.Errorf("parsing roles: %w", err)
		}
		escrow.Roles = roles
	}

	// Parse milestones
	if milestonesVal, ok := findInMap(entries, "milestones"); ok {
		milestones, err := parseMilestones(milestonesVal)
		if err != nil {
			return fmt.Errorf("parsing milestones: %w", err)
		}
		escrow.Milestones = milestones
	}

	// Parse trustline
	if trustlineVal, ok := findInMap(entries, "trustline"); ok {
		trustlineAddr, err := parseTrustlineAddress(trustlineVal)
		if err != nil {
			return fmt.Errorf("parsing trustline: %w", err)
		}
		escrow.TrustlineAddress = trustlineAddr
	}

	return nil
}

// parseEscrowFlags parses the flags map
// Supports both single-release format (disputed, released, resolved)
// and multi-release format (approved, disputed, released, resolved)
func parseEscrowFlags(val xdr.ScVal) (entities.EscrowFlags, error) {
	entries, err := extractMapFromScVal(val)
	if err != nil {
		return entities.EscrowFlags{}, fmt.Errorf("extracting flags map: %w", err)
	}

	flags := entities.EscrowFlags{}

	// Multi-release specific: approved is inside flags
	if approvedVal, ok := findInMap(entries, "approved"); ok {
		approved, err := extractBoolFromScVal(approvedVal)
		if err != nil {
			return entities.EscrowFlags{}, fmt.Errorf("parsing approved: %w", err)
		}
		flags.Approved = approved
	}

	if disputedVal, ok := findInMap(entries, "disputed"); ok {
		disputed, err := extractBoolFromScVal(disputedVal)
		if err != nil {
			return entities.EscrowFlags{}, fmt.Errorf("parsing disputed: %w", err)
		}
		flags.Disputed = disputed
	}

	if releasedVal, ok := findInMap(entries, "released"); ok {
		released, err := extractBoolFromScVal(releasedVal)
		if err != nil {
			return entities.EscrowFlags{}, fmt.Errorf("parsing released: %w", err)
		}
		flags.Released = released
	}

	if resolvedVal, ok := findInMap(entries, "resolved"); ok {
		resolved, err := extractBoolFromScVal(resolvedVal)
		if err != nil {
			return entities.EscrowFlags{}, fmt.Errorf("parsing resolved: %w", err)
		}
		flags.Resolved = resolved
	}

	return flags, nil
}

// parseEscrowRoles parses the roles map
func parseEscrowRoles(val xdr.ScVal) (entities.EscrowRoles, error) {
	entries, err := extractMapFromScVal(val)
	if err != nil {
		return entities.EscrowRoles{}, fmt.Errorf("extracting roles map: %w", err)
	}

	roles := entities.EscrowRoles{}

	if spVal, ok := findInMap(entries, "service_provider"); ok {
		sp, err := extractAddressFromScVal(spVal)
		if err != nil {
			return entities.EscrowRoles{}, fmt.Errorf("parsing service_provider: %w", err)
		}
		roles.ServiceProvider = sp
	}

	if recVal, ok := findInMap(entries, "receiver"); ok {
		rec, err := extractAddressFromScVal(recVal)
		if err != nil {
			return entities.EscrowRoles{}, fmt.Errorf("parsing receiver: %w", err)
		}
		roles.Receiver = rec
	}

	if appVal, ok := findInMap(entries, "approver"); ok {
		app, err := extractAddressFromScVal(appVal)
		if err != nil {
			return entities.EscrowRoles{}, fmt.Errorf("parsing approver: %w", err)
		}
		roles.Approver = app
	}

	if rsVal, ok := findInMap(entries, "release_signer"); ok {
		rs, err := extractAddressFromScVal(rsVal)
		if err != nil {
			return entities.EscrowRoles{}, fmt.Errorf("parsing release_signer: %w", err)
		}
		roles.ReleaseSigner = rs
	}

	if drVal, ok := findInMap(entries, "dispute_resolver"); ok {
		dr, err := extractAddressFromScVal(drVal)
		if err != nil {
			return entities.EscrowRoles{}, fmt.Errorf("parsing dispute_resolver: %w", err)
		}
		roles.DisputeResolver = dr
	}

	if paVal, ok := findInMap(entries, "platform_address"); ok {
		pa, err := extractAddressFromScVal(paVal)
		if err != nil {
			return entities.EscrowRoles{}, fmt.Errorf("parsing platform_address: %w", err)
		}
		roles.PlatformAddress = pa
	}

	return roles, nil
}

// parseMilestones parses the milestones vector
func parseMilestones(val xdr.ScVal) ([]entities.Milestone, error) {
	vec, err := extractVecFromScVal(val)
	if err != nil {
		return nil, fmt.Errorf("extracting milestones vec: %w", err)
	}

	milestones := make([]entities.Milestone, 0, len(vec))

	for i, milestoneVal := range vec {
		milestone, err := parseMilestone(milestoneVal)
		if err != nil {
			return nil, fmt.Errorf("parsing milestone[%d]: %w", i, err)
		}
		milestones = append(milestones, milestone)
	}

	return milestones, nil
}

// parseMilestone parses a single milestone map
// Supports both single-release format (approved, description, evidence, status)
// and multi-release format (amount, description, evidence, flags, receiver, status)
func parseMilestone(val xdr.ScVal) (entities.Milestone, error) {
	entries, err := extractMapFromScVal(val)
	if err != nil {
		return entities.Milestone{}, fmt.Errorf("extracting milestone map: %w", err)
	}

	milestone := entities.Milestone{}

	// Common fields
	if descVal, ok := findInMap(entries, "description"); ok {
		desc, err := extractStringFromScVal(descVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing description: %w", err)
		}
		milestone.Description = desc
	}

	if statusVal, ok := findInMap(entries, "status"); ok {
		status, err := extractSymbolOrStringFromScVal(statusVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing status: %w", err)
		}
		milestone.Status = status
	}

	if evidenceVal, ok := findInMap(entries, "evidence"); ok {
		evidence, err := extractStringFromScVal(evidenceVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing evidence: %w", err)
		}
		milestone.Evidence = evidence
	}

	// Single-release specific field
	if approvedVal, ok := findInMap(entries, "approved"); ok {
		approved, err := extractBoolFromScVal(approvedVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing approved: %w", err)
		}
		milestone.Approved = approved
	}

	// Multi-release specific fields
	if amountVal, ok := findInMap(entries, "amount"); ok {
		amount, err := extractI128FromScVal(amountVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing amount: %w", err)
		}
		milestone.Amount = amount
	}

	if flagsVal, ok := findInMap(entries, "flags"); ok {
		flags, err := parseEscrowFlags(flagsVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing flags: %w", err)
		}
		milestone.Flags = &flags
	}

	if receiverVal, ok := findInMap(entries, "receiver"); ok {
		receiver, err := extractAddressFromScVal(receiverVal)
		if err != nil {
			return entities.Milestone{}, fmt.Errorf("parsing receiver: %w", err)
		}
		milestone.Receiver = receiver
	}

	return milestone, nil
}

// parseGetEscrowByContractIDArgs parses the arguments from get_escrow_by_contract_id.
// Args structure: [0] contract_id (Address)
func parseGetEscrowByContractIDArgs(args []xdr.ScVal) (string, error) {
	if len(args) < 1 {
		return "", fmt.Errorf("insufficient arguments: expected 1 (contract_id), got %d", len(args))
	}
	return extractAddressFromScVal(args[0])
}

// parseGetMultipleEscrowBalancesArgs parses the arguments from get_multiple_escrow_balances.
// Args structure: [0] addresses (Vec<Address>)
func parseGetMultipleEscrowBalancesArgs(args []xdr.ScVal) ([]string, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("insufficient arguments: expected 1 (addresses), got %d", len(args))
	}
	vec, err := extractVecFromScVal(args[0])
	if err != nil {
		return nil, fmt.Errorf("parsing addresses vec: %w", err)
	}
	addresses := make([]string, 0, len(vec))
	for i, val := range vec {
		addr, err := extractAddressFromScVal(val)
		if err != nil {
			return nil, fmt.Errorf("parsing address at index %d: %w", i, err)
		}
		addresses = append(addresses, addr)
	}
	return addresses, nil
}

// parseTrustlineAddress parses the trustline field which can come in different formats:
// 1. Vec of maps: [{address: Address}]
// 2. Map directly: {address: Address}
// 3. Address directly: Address
func parseTrustlineAddress(val xdr.ScVal) (string, error) {
	// Try as vector first (most common case)
	if vec, ok := val.GetVec(); ok && vec != nil && len(*vec) > 0 {
		// Get the first trustline entry
		entries, err := extractMapFromScVal((*vec)[0])
		if err != nil {
			return "", fmt.Errorf("extracting trustline map from vec: %w", err)
		}

		if addrVal, ok := findInMap(entries, "address"); ok {
			addr, err := extractAddressFromScVal(addrVal)
			if err != nil {
				return "", fmt.Errorf("parsing trustline address from vec: %w", err)
			}
			return addr, nil
		}
		return "", nil
	}

	// Try as map directly
	if entries, err := extractMapFromScVal(val); err == nil {
		if addrVal, ok := findInMap(entries, "address"); ok {
			addr, err := extractAddressFromScVal(addrVal)
			if err != nil {
				return "", fmt.Errorf("parsing trustline address from map: %w", err)
			}
			return addr, nil
		}
		return "", nil
	}

	// Try as address directly
	if addr, err := extractAddressFromScVal(val); err == nil {
		return addr, nil
	}

	// Empty or null trustline
	return "", nil
}
