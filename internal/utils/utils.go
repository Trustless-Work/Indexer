package utils

import (
	"context"
	"io"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/support/log"
)

// IsContractAddress reports whether address is a Stellar contract address
// (C-strkey). Kept for validating operator-supplied contract ids at the
// admin edge — an id that fails this check can never match a ledger entry
// and would otherwise be tracked and silently never queried.
func IsContractAddress(address string) bool {
	_, err := strkey.Decode(strkey.VersionByteContract, address)
	return err == nil
}

// DeferredClose is a function that closes an `io.Closer` resource and logs an error if it fails.
func DeferredClose(ctx context.Context, closer io.Closer, errMsg string) {
	if err := closer.Close(); err != nil {
		if errMsg == "" {
			errMsg = "closing resource"
		}
		log.Ctx(ctx).Errorf("%s: %v", errMsg, err)
	}
}
