package persistence

import (
	"errors"
	"math"
	"strconv"
)

var ErrJSONStorageNumber = errors.New("unsupported JSON storage number")

// MaxJSONStorageNumberBytes is the existing raw numeric representation contract.
// It is not a forecast of candidate, driver, or in-memory allocation.
const MaxJSONStorageNumberBytes int64 = 343

// ValidateJSONStorageNumber validates a raw finite numeric representation.
func ValidateJSONStorageNumber(token string) error {
	_, err := jsonStorageNumberWidth(token)
	return err
}

func jsonStorageNumberWidth(token string) (int64, error) {
	bad := func() (int64, error) { return 0, ErrJSONStorageNumber }
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
		return width, nil
	}
	if _, err := strconv.ParseUint(token, 10, 64); err == nil {
		return width, nil
	}
	f, err := strconv.ParseFloat(token, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return bad()
	}
	return width, nil
}
