package workstate

import (
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"strings"
	"testing"
)

func TestResponseReceiptOpaqueIdentityAndFiniteShape(t *testing.T) {
	id := strings.Repeat("\x01", 128)
	r := glossary.ResponseReceipt{Version: 2, RequestID: id, InputDigest: strings.Repeat("\x02", 128), OutcomeDigest: strings.Repeat("a", 64), Added: 65536, Skipped: 65536}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// Typed integer fields need at most one PG separator space per delimiter;
	// even worst sixfold escaping remains below the fixed 2 KiB O sub-bound.
	if len(data)+32 > responseReceiptReadBytes {
		t.Fatalf("bound %d", len(data))
	}
	t.Logf("128-byte opaque RequestID/InputDigest: maximum escaped six-field receipt=%d; PostgreSQL separator allowance=32; total=%d < %d", len(data), len(data)+32, responseReceiptReadBytes)
	if err = validateResponseReceipt(data, id); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{65, 128} {
		v := r
		v.InputDigest = strings.Repeat("x", n)
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = validateResponseReceipt(data, id); err != nil {
			t.Fatalf("accepted Reserve digest length %d rejected: %v", n, err)
		}
	}
	for _, kind := range []string{"empty_digest", "long_digest", "bad_hash", "negative", "trailing", "unknown", "invalid_utf8"} {
		t.Run(kind, func(t *testing.T) {
			bad := r
			switch kind {
			case "empty_digest":
				bad.InputDigest = ""
			case "long_digest":
				bad.InputDigest = strings.Repeat("a", 129)
			case "bad_hash":
				bad.OutcomeDigest = strings.Repeat("z", 64)
			case "negative":
				bad.Added = -1
			}
			data, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "trailing":
				data = append(data, []byte(`{}`)...)
			case "unknown":
				data = append(data[:len(data)-1], []byte(`,"extra":true}`)...)
			case "invalid_utf8":
				data = append(data[:len(data)-1], []byte(",\"bad\":\"\xff\"}")...)
			}
			if err := validateResponseReceipt(data, id); err == nil {
				t.Fatal("malformed receipt accepted")
			}
		})
	}
}
