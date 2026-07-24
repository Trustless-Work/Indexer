package config

import (
	"strings"
	"testing"
)

func TestLoad_FallbackURLs_ParsesOrderedList(t *testing.T) {
	withEnv(t, minimalEnv())
	t.Setenv("RPC_FALLBACK_URLS", "https://a.example.org,https://b.example.org")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.RPC.FallbackURLs) != 2 || cfg.RPC.FallbackURLs[0] != "https://a.example.org" || cfg.RPC.FallbackURLs[1] != "https://b.example.org" {
		t.Errorf("expected ordered fallback list; got %v", cfg.RPC.FallbackURLs)
	}
}

func TestLoad_FallbackURLs_UnsetMeansNoFailover(t *testing.T) {
	withEnv(t, minimalEnv())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.RPC.FallbackURLs) != 0 {
		t.Errorf("expected empty fallback list; got %v", cfg.RPC.FallbackURLs)
	}
}

func TestLoad_FallbackURLs_BlankEntry_Fails(t *testing.T) {
	withEnv(t, minimalEnv())
	// A trailing comma silently yields an empty entry; that must fail
	// loudly instead of an operator believing failover is configured.
	t.Setenv("RPC_FALLBACK_URLS", "https://a.example.org,")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RPC_FALLBACK_URLS") {
		t.Fatalf("expected RPC_FALLBACK_URLS validation error; got %v", err)
	}
}

func TestLoad_FallbackURLs_NetworkMismatchHeuristic_Fails(t *testing.T) {
	// A testnet-named fallback behind a mainnet indexer is a landmine
	// that only detonates during an outage — reject it at boot.
	withEnv(t, map[string]string{
		"NETWORK_NAME":       "mainnet",
		"NETWORK_PASSPHRASE": "Public Global Stellar Network ; September 2015",
		"RPC_URL":            "https://soroban.mainnet.example.org",
		"RPC_FALLBACK_URLS":  "https://soroban-testnet.stellar.org",
	})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "RPC_FALLBACK_URLS entry 1") {
		t.Fatalf("expected fallback network-mismatch error; got %v", err)
	}
}
