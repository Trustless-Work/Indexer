package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Trustless-Work/Indexer/internal/ingest"
	"github.com/Trustless-Work/Indexer/internal/services"
	"github.com/sirupsen/logrus"
	"github.com/stellar/go-stellar-sdk/support/log"
)

func main() {
	fmt.Println("Starting ingest...")
	preConfigureLogger()

	const (
		NetworkPassphrase = "Test SDF Network ; September 2015"
		RPCURL            = "https://soroban-testnet.stellar.org"
	)
	httpClient := &http.Client{Timeout: 30 * time.Second}

	rpcClient, _ := services.NewRPCService(RPCURL, NetworkPassphrase, httpClient)
	rpcHealthInfo, _ := rpcClient.GetHealth()
	latesLedgerSequence := rpcHealthInfo.LatestLedger

	cfg := ingest.Config{
		StartLedger:       latesLedgerSequence,
		EndLedger:         0,
		RPCURL:            RPCURL,
		NetworkPassphrase: NetworkPassphrase,
		GetLedgersLimit:   100,
		LedgerBackendType: ingest.LedgerBackendTypeRPC,
	}
	err := ingest.Ingest(cfg)
	if err != nil {
		log.Fatalf("running ingest: %v", err)
	}
}

func preConfigureLogger() {
	log.DefaultLogger = log.New()
	log.DefaultLogger.SetLevel(logrus.TraceLevel)
}
