package persistence

import (
	"errors"
	"strings"
	"testing"
)

func TestJSONStorageNumberRejectsUnboundedRawExponents(t *testing.T) {
	for _, token := range []string{"1e100000", "0e-100000", "-0.000e-100000", "1e9223372036854775807", "1e-9223372036854775807", "1e-9223372036854775808", "1e999999999999999999999999", "NaN", "Infinity", "-Inf", "+1", "01", "1.", ".1", "1e", "1e+", "1 2", "null", "true", "", strings.Repeat("1", 344), "-1.234567890123456789012345678901234567890123456789e-324"} {
		if err := ValidateJSONStorageNumber(token); !errors.Is(err, ErrJSONStorageNumber) {
			t.Errorf("raw token %q accepted: %v", token, err)
		}
	}
	for _, token := range []string{"0", "-0", "9007199254740993", "-9223372036854775808", "18446744073709551615", "1E+0308", "1.230e+0", "-5e-324", "1e-325"} {
		if err := ValidateJSONStorageNumber(token); err != nil {
			t.Errorf("bounded token %s: %v", token, err)
		}
	}
}
