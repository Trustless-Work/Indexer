package contracts

import (
	"encoding/hex"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// extractAddressFromScVal helps extract a string representation of an address from a ScVal
func extractAddressFromScVal(val xdr.ScVal) (string, error) {
	addr, ok := val.GetAddress()
	if !ok {
		return "", fmt.Errorf("invalid address")
	}
	addrStr, err := addr.String()
	if err != nil {
		return "", fmt.Errorf("failed to convert address to string: %w", err)
	}
	return addrStr, nil
}

// extractAssetFromScVal helps extract an asset from a ScVal
func extractAssetFromScVal(val xdr.ScVal) (xdr.Asset, error) {
	asset, ok := val.GetStr()
	if !ok {
		return xdr.Asset{}, fmt.Errorf("invalid asset")
	}
	assets, err := xdr.BuildAssets(string(asset))
	if err != nil {
		return xdr.Asset{}, fmt.Errorf("failed to build assets: %w", err)
	}
	if len(assets) == 0 {
		return xdr.Asset{}, fmt.Errorf("no assets found")
	}
	return assets[0], nil
}

// extractBytesFromScVal extracts bytes from a ScVal and returns it as a hex string
func extractBytesFromScVal(val xdr.ScVal) (string, error) {
	bytes, ok := val.GetBytes()
	if !ok {
		return "", fmt.Errorf("invalid bytes")
	}
	return hex.EncodeToString(bytes), nil
}

// extractSymbolFromScVal extracts a symbol from a ScVal
func extractSymbolFromScVal(val xdr.ScVal) (string, error) {
	sym, ok := val.GetSym()
	if !ok {
		return "", fmt.Errorf("invalid symbol")
	}
	return string(sym), nil
}

// extractStringFromScVal extracts a string from a ScVal
func extractStringFromScVal(val xdr.ScVal) (string, error) {
	str, ok := val.GetStr()
	if !ok {
		return "", fmt.Errorf("invalid string")
	}
	return string(str), nil
}

// extractI128FromScVal extracts an i128 from a ScVal and converts it to uint64
func extractI128FromScVal(val xdr.ScVal) (uint64, error) {
	i128, ok := val.GetI128()
	if !ok {
		return 0, fmt.Errorf("invalid i128")
	}
	if i128.Hi < 0 {
		return 0, fmt.Errorf("negative i128 value")
	}
	if i128.Hi > 0 {
		return 0, fmt.Errorf("i128 overflow: value exceeds uint64")
	}
	return uint64(i128.Lo), nil
}

// extractU32FromScVal extracts a u32 from a ScVal
func extractU32FromScVal(val xdr.ScVal) (uint32, error) {
	u32, ok := val.GetU32()
	if !ok {
		return 0, fmt.Errorf("invalid u32")
	}
	return uint32(u32), nil
}

// extractBoolFromScVal extracts a bool from a ScVal
func extractBoolFromScVal(val xdr.ScVal) (bool, error) {
	b, ok := val.GetB()
	if !ok {
		return false, fmt.Errorf("invalid bool")
	}
	return b, nil
}

// extractVecFromScVal extracts a vector from a ScVal
func extractVecFromScVal(val xdr.ScVal) ([]xdr.ScVal, error) {
	vec, ok := val.GetVec()
	if !ok {
		return nil, fmt.Errorf("invalid vec")
	}
	if vec == nil {
		return []xdr.ScVal{}, nil
	}
	return *vec, nil
}

// extractMapFromScVal extracts a map from a ScVal
func extractMapFromScVal(val xdr.ScVal) ([]xdr.ScMapEntry, error) {
	m, ok := val.GetMap()
	if !ok {
		return nil, fmt.Errorf("invalid map")
	}
	if m == nil {
		return []xdr.ScMapEntry{}, nil
	}
	return *m, nil
}

// findInMap searches for a key in a map and returns the corresponding value
func findInMap(entries []xdr.ScMapEntry, key string) (xdr.ScVal, bool) {
	for _, entry := range entries {
		sym, ok := entry.Key.GetSym()
		if !ok {
			continue
		}
		if string(sym) == key {
			return entry.Val, true
		}
	}
	return xdr.ScVal{}, false
}

// extractSymbolOrStringFromScVal extracts a value that can be either a symbol or a string
func extractSymbolOrStringFromScVal(val xdr.ScVal) (string, error) {
	// Try symbol first
	if sym, ok := val.GetSym(); ok {
		return string(sym), nil
	}

	// Try string
	if str, ok := val.GetStr(); ok {
		return string(str), nil
	}

	return "", fmt.Errorf("value is neither symbol nor string")
}

// extractStringOrNumberFromScVal extracts a value that can be either a string or a number
func extractStringOrNumberFromScVal(val xdr.ScVal) (string, error) {
	// Try string first
	if str, ok := val.GetStr(); ok {
		return string(str), nil
	}

	// Try u32
	if u32, ok := val.GetU32(); ok {
		return fmt.Sprintf("%d", u32), nil
	}

	// Try i32
	if i32, ok := val.GetI32(); ok {
		return fmt.Sprintf("%d", i32), nil
	}

	// Try u64
	if u64, ok := val.GetU64(); ok {
		return fmt.Sprintf("%d", u64), nil
	}

	// Try i64
	if i64, ok := val.GetI64(); ok {
		return fmt.Sprintf("%d", i64), nil
	}

	// Try i128
	if i128, ok := val.GetI128(); ok {
		if i128.Hi == 0 {
			return fmt.Sprintf("%d", i128.Lo), nil
		}
		return fmt.Sprintf("%d%d", i128.Hi, i128.Lo), nil
	}

	return "", fmt.Errorf("value is neither string nor number")
}
