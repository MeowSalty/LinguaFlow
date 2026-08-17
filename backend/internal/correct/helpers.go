package correct

// quotePairs is the authoritative open→close mapping used by the wrap rule.
// It mirrors a subset of qa.quoteRunes restricted to directionally paired quotes
// (symmetric " is excluded — it has no open/close direction). Kept local & explicit
// so correct never drifts if the checker's superset changes.
var quotePairs = map[rune]rune{
	'「': '」',
	'『': '』',
	'“': '”',
	'‘': '’',
	'«': '»',
}

func countRune(s string, r rune) int {
	n := 0
	for _, c := range s {
		if c == r {
			n++
		}
	}
	return n
}

// firstRune returns the first rune of s and whether s is non-empty.
func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

// lastRune returns the last rune of s and whether s is non-empty.
func lastRune(s string) (rune, bool) {
	var last rune
	seen := false
	for _, r := range s {
		last = r
		seen = true
	}
	return last, seen
}

// closingRuneFor reports whether r is a known opening quote and returns its closing pair.
func closingRuneFor(r rune) (rune, bool) {
	c, ok := quotePairs[r]
	return c, ok
}
