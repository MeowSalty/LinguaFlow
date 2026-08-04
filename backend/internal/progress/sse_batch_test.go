package progress

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateSSEContent_UnderLimit(t *testing.T) {
	s := "hello"
	got, trunc, n := TruncateSSEContent(s)
	if got != s || trunc || n != 5 {
		t.Fatalf("got %q trunc=%v n=%d", got, trunc, n)
	}
}

func TestTruncateSSEContent_OverLimit(t *testing.T) {
	s := make([]byte, MaxSSEBatchContentBytes+10)
	for i := range s {
		s[i] = 'x'
	}
	got, trunc, n := TruncateSSEContent(string(s))
	if !trunc || n != len(s) || len(got) != MaxSSEBatchContentBytes {
		t.Fatalf("trunc=%v n=%d len(got)=%d", trunc, n, len(got))
	}
}

func TestTruncateSSEContent_UTF8Boundary(t *testing.T) {
	rune := '中'
	rb := make([]byte, utf8.RuneLen(rune))
	utf8.EncodeRune(rb, rune)
	prefix := strings.Repeat("a", MaxSSEBatchContentBytes-len(rb)+1)
	s := prefix + string(rb)
	got, trunc, n := TruncateSSEContent(s)
	if !trunc || n != len(s) {
		t.Fatalf("trunc=%v n=%d len(s)=%d", trunc, n, len(s))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid utf8: %q", got)
	}
	if strings.HasSuffix(got, "中") {
		t.Fatal("expected trailing rune dropped when over byte limit")
	}
}

func TestBatchLevelFromStatus(t *testing.T) {
	cases := map[string]string{
		"success": "info",
		"partial": "warn",
		"failed":  "error",
		"other":   "info",
	}
	for status, want := range cases {
		if got := BatchLevelFromStatus(status); got != want {
			t.Errorf("status %q: got %q want %q", status, got, want)
		}
	}
}
