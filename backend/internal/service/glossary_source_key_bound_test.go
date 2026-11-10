package service

import (
	"testing"
	"unicode"
	"unicode/utf8"
)

// This is the mechanical proof for the additional normalized SourceKey copy,
// separate from opaque response copies. JSON-decoded pipeline strings are UTF8.
func TestGlossarySourceKeyUnicodeAllocationBound(t *testing.T) {
	maxFrom, maxTo := 1, 1
	var witness rune
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if !utf8.ValidRune(r) {
			continue
		}
		from, to := utf8.RuneLen(r), utf8.RuneLen(unicode.ToLower(r))
		if 2*to > 3*from {
			t.Fatalf("lowercase expansion %U: %d -> %d exceeds 3/2", r, from, to)
		}
		if to*maxFrom > maxTo*from {
			maxFrom, maxTo, witness = from, to, r
		}
	}
	if maxFrom != 2 || maxTo != 3 {
		t.Fatalf("unexpected Unicode expansion: %d/%d (%U)", maxTo, maxFrom, witness)
	}
	t.Logf("all Unicode scalars checked: maximum UTF8 SourceKey expansion %d/%d at %U", maxTo, maxFrom, witness)
}
