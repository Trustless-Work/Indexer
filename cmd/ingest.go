package main

import (
	"fmt"

	"github.com/Trustless-Work/Indexer/internal/ingest"
	"github.com/stellar/go-stellar-sdk/support/log"
)

func main() {
	fmt.Println("Starting ingest...")

	cfg := ingest.Config{
		StartLedger:       299385,
		EndLedger:         0,
		RPCURL:            "https://soroban-testnet.stellar.org",
		NetworkPassphrase: "Test SDF Network ; September 2015",
		GetLedgersLimit:   100,
		LedgerBackendType: ingest.LedgerBackendTypeRPC,
	}
	err := ingest.Ingest(cfg)
	if err != nil {
		log.Fatalf("running ingest: %v", err)
	}
}
