package persistence

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// JSONStorageRepresentation describes the database value's text/byte projection,
// not its physical row, index or WAL allocation. No SQL dependency is required.
type JSONStorageRepresentation uint8

const (
	StorageRawBytes JSONStorageRepresentation = iota
	StorageSQLiteJSON
	StoragePostgresJSONB
)

var (
	ErrJSONStorageBound  = errors.New("JSON storage bound exceeds capacity")
	ErrJSONStorageNumber = errors.New("unsupported JSON storage number")
)

// MaxJSONStorageNumberBytes follows from float64's shortest round-trip format:
// at most 17 significant digits, normalized exponent in [-324,308]. The worst
// conservative negative fraction is '-' + '0.' + 323 zeros + 17 digits = 343.
// Integer64/uint64 representations are at most 20 bytes. Raw number tokens must
// instead pass ValidateJSONStorageNumber or be normalized/reencoded first.
const MaxJSONStorageNumberBytes int64 = 343

// JSONStorageBound computes a maximum reliable save commitment before dispatch.
// C bounds the complete compact canonical UTF-8 encoding and N bounds its nodes
// (including numeric scalars). Canonical string escaping must be at least as
// conservative as the adapter: JSONB UTF-8 strings do not exceed that encoding.
// PostgreSQL's non-indented jsonb::text adds at most two separator spaces per
// node, and replaces a >=1-byte number by at most 343 bytes. ReadLimit is a hard
// representation limit; an excessive bound is rejected, never truncated.
func JSONStorageBound(rep JSONStorageRepresentation, canonicalMaxBytes, maxNodes, readLimit int64) (int64, error) {
	if canonicalMaxBytes <= 0 || readLimit <= 0 || maxNodes < 0 {
		return 0, fmt.Errorf("%w: nonpositive byte limit or negative nodes", ErrJSONStorageBound)
	}
	upper := canonicalMaxBytes
	switch rep {
	case StorageRawBytes:
	case StorageSQLiteJSON:
		if maxNodes == 0 {
			return 0, fmt.Errorf("%w: missing JSON node limit", ErrJSONStorageBound)
		}
	case StoragePostgresJSONB:
		if maxNodes == 0 {
			return 0, fmt.Errorf("%w: missing JSON node limit", ErrJSONStorageBound)
		}
		// Every numeric scalar needs at least one canonical byte, so N and C
		// independently bound its count. Each checked term is nonnegative.
		count := min(maxNodes, canonicalMaxBytes)
		if maxNodes > math.MaxInt64/2 || count > math.MaxInt64/(MaxJSONStorageNumberBytes-1) {
			return 0, fmt.Errorf("%w: arithmetic overflow", ErrJSONStorageBound)
		}
		spaces, growth := 2*maxNodes, (MaxJSONStorageNumberBytes-1)*count
		if spaces > math.MaxInt64-upper {
			return 0, fmt.Errorf("%w: arithmetic overflow", ErrJSONStorageBound)
		}
		upper += spaces
		if growth > math.MaxInt64-upper {
			return 0, fmt.Errorf("%w: arithmetic overflow", ErrJSONStorageBound)
		}
		upper += growth
	default:
		return 0, fmt.Errorf("%w: unknown storage representation", ErrJSONStorageBound)
	}
	if upper > readLimit {
		return 0, fmt.Errorf("%w: maximum %d exceeds read limit %d", ErrJSONStorageBound, upper, readLimit)
	}
	return upper, nil
}

// ValidateJSONStorageNumber narrows a raw numeric token before promising its
// JSONB representation. In particular ParseFloat alone is insufficient: zero
// or underflow with a huge negative exponent still carries PostgreSQL dscale.
// This parser computes a conservative decimal length without constructing it.
// The caller validates every raw scalar before dispatch or normalizes the graph
// into int64/uint64/finite float64 and reencodes; checking only after HTTP cannot
// establish a maximum save commitment for already dispatched work.
func ValidateJSONStorageNumber(token string) error {
	bad := func() error { return ErrJSONStorageNumber }
	if len(token) == 0 || int64(len(token)) > MaxJSONStorageNumberBytes {
		return bad()
	}
	i := 0
	sign := int64(0)
	if token[0] == '-' {
		sign = 1
		i++
		if i == len(token) {
			return bad()
		}
	}
	integerStart := i
	if token[i] == '0' {
		i++
	} else {
		if token[i] < '1' || token[i] > '9' {
			return bad()
		}
		for i < len(token) && token[i] >= '0' && token[i] <= '9' {
			i++
		}
	}
	integerDigits := int64(i - integerStart)
	digits := integerDigits
	if i < len(token) && token[i] == '.' {
		i++
		start := i
		for i < len(token) && token[i] >= '0' && token[i] <= '9' {
			i++
		}
		if start == i {
			return bad()
		}
		digits += int64(i - start)
	}
	exponent := int64(0)
	if i < len(token) && (token[i] == 'e' || token[i] == 'E') {
		i++
		negative := false
		if i < len(token) && (token[i] == '+' || token[i] == '-') {
			negative = token[i] == '-'
			i++
		}
		start := i
		for i < len(token) && token[i] >= '0' && token[i] <= '9' {
			d := int64(token[i] - '0')
			if exponent > (math.MaxInt64-d)/10 {
				return bad()
			}
			exponent = 10*exponent + d
			i++
		}
		if start == i {
			return bad()
		}
		if negative {
			exponent = -exponent
		}
	}
	if i != len(token) || exponent > math.MaxInt64-integerDigits {
		return bad()
	}
	point := integerDigits + exponent
	var width int64
	if point >= digits {
		if point > math.MaxInt64-sign {
			return bad()
		}
		width = point + sign
	} else if point > 0 {
		width = digits + 1 + sign
	} else {
		zeros := -point
		if zeros > math.MaxInt64-digits-2-sign {
			return bad()
		}
		width = 2 + zeros + digits + sign
	}
	if width > MaxJSONStorageNumberBytes {
		return bad()
	}
	// Preserve integer identity rather than routing >2^53 integers through a
	// rounding float conversion. Larger decimal values must remain finite64.
	if _, err := strconv.ParseInt(token, 10, 64); err == nil {
		return nil
	}
	if _, err := strconv.ParseUint(token, 10, 64); err == nil {
		return nil
	}
	f, err := strconv.ParseFloat(token, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return bad()
	}
	return nil
}
