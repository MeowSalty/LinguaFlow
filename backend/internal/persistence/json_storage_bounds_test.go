package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestJSONStorageBoundExtremeFiniteNumbers(t *testing.T) {
	for _, value := range []float64{math.MaxFloat64, -math.MaxFloat64, math.SmallestNonzeroFloat64, -math.SmallestNonzeroFloat64, 1e308, -1e308, 1e18, -1e18, 1.2345678901234567e-300, math.Copysign(0, -1)} {
		token := strconv.FormatFloat(value, 'g', -1, 64)
		if err := ValidateJSONStorageNumber(token); err != nil {
			t.Fatalf("finite canonical %s: %v", token, err)
		}
		// f/-1 expands the shortest round-trip value without an exponent. Its
		// length exercises the documented jsonb numeric_out decimal envelope.
		decimal := strconv.FormatFloat(value, 'f', -1, 64)
		if len(decimal) > int(MaxJSONStorageNumberBytes) {
			t.Fatalf("decimal exceeds proof: %s length=%d", token, len(decimal))
		}
		bound, err := JSONStorageBound(StoragePostgresJSONB, int64(len(token)), 1, 64<<20)
		if err != nil || bound < int64(len(decimal)) {
			t.Fatalf("under-reserved %s: %d vs %d (%v)", token, bound, len(decimal), err)
		}
	}
	if got := len(strconv.FormatFloat(-math.SmallestNonzeroFloat64, 'f', -1, 64)); got != 327 {
		t.Fatalf("negative smallest expansion=%d, expected 327", got)
	}
	if got := len(strconv.FormatFloat(-math.MaxFloat64, 'f', -1, 64)); got != 310 {
		t.Fatalf("negative maximum expansion=%d, expected 310", got)
	}
}

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

func TestJSONStorageBoundPreservesIntegerAndEscapedStrings(t *testing.T) {
	const exact = "9007199254740993"
	if err := ValidateJSONStorageNumber(exact); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"integer": json.Number(exact), "text": "汉字😀 <ruby> & \" \\ \b \n \u2028"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(exact)) {
		t.Fatal("integer identity rounded")
	}
	// A jsonb-style non-HTML string encoder can shorten canonical escapes;
	// separator spaces are the only string/container growth in this fixture.
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(map[string]any{"integer": json.Number(exact), "text": "汉字😀 <ruby> & \" \\ \b \n \u2028"}); err != nil {
		t.Fatal(err)
	}
	projected := int64(out.Len()-1) + 4
	bound, err := JSONStorageBound(StoragePostgresJSONB, int64(len(data)), 8, 64<<20)
	if err != nil || bound < projected {
		t.Fatalf("unicode/escape projection not bounded: %d %d %v", bound, projected, err)
	}
	for _, rep := range []JSONStorageRepresentation{StorageRawBytes, StorageSQLiteJSON} {
		n, err := JSONStorageBound(rep, int64(len(data)), 8, 64<<20)
		if err != nil || n != int64(len(data)) {
			t.Fatalf("representation %d changed exact canonical bytes: %d %v", rep, n, err)
		}
	}
}

func TestJSONStorageBoundCheckedArithmeticAndHardCap(t *testing.T) {
	for _, args := range [][3]int64{{math.MaxInt64, 1, math.MaxInt64}, {1, math.MaxInt64, math.MaxInt64}, {math.MaxInt64 - 100, 65536, math.MaxInt64}, {-1, 1, 64 << 20}, {1, -1, 64 << 20}, {0, 1, 64 << 20}, {1, 0, 64 << 20}, {1, 1, 0}} {
		if n, err := JSONStorageBound(StoragePostgresJSONB, args[0], args[1], args[2]); n != 0 || !errors.Is(err, ErrJSONStorageBound) {
			t.Fatalf("invalid/overflow args %v returned %d %v", args, n, err)
		}
	}
	if n, err := JSONStorageBound(StoragePostgresJSONB, 64<<20, 65536, 64<<20); n != 0 || !errors.Is(err, ErrJSONStorageBound) {
		t.Fatalf("hard cap silently clipped commitment: %d %v", n, err)
	}
	n, err := JSONStorageBound(StoragePostgresJSONB, 4<<20, 65536, 64<<20)
	if err != nil || n != 26738688 {
		t.Fatalf("default Q proof changed: %d %v", n, err)
	}
	// Overflow cannot occur before it is checked; a bytea R has no JSONB
	// numeric/separator multiplier and may use its independent read limit.
	if n, err := JSONStorageBound(StorageRawBytes, math.MaxInt64, 0, math.MaxInt64); err != nil || n != math.MaxInt64 {
		t.Fatalf("raw R changed: %d %v", n, err)
	}
	if _, err := JSONStorageBound(JSONStorageRepresentation(99), 1, 1, 10); !errors.Is(err, ErrJSONStorageBound) {
		t.Fatal(err)
	}
}
