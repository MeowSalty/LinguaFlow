package script

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"unicode/utf8"
)

// ScanBounded keeps one fixed record per configured sibling. Samples borrow
// their first run directly from immutable input; no rune array or prefix copy
// grows with the number of runs.
func (p Profile) ScanBounded(b *backend.BuildBounds, text string) (Evidence, bool, error) {
	type record struct {
		count, start, end, runes int
		seen                     bool
	}
	if err := b.ReserveNodes(int64(len(p.Siblings)), 40); err != nil {
		return Evidence{}, false, err
	}
	stats := make([]record, len(p.Siblings))
	current := -1
	capture := false
	for at, r := range text {
		sc, ok := runeScript(r)
		idx := -1
		if ok {
			for i, s := range p.Siblings {
				if s == sc {
					idx = i
					break
				}
			}
		}
		if idx < 0 {
			current = -1
			capture = false
			continue
		}
		stats[idx].count++
		if current != idx {
			current = idx
			capture = !stats[idx].seen
			if capture {
				stats[idx].seen = true
				stats[idx].start = at
			}
		}
		if capture && stats[idx].runes < maxSampleRunes {
			_, width := utf8.DecodeRuneInString(text[at:])
			stats[idx].runes++
			stats[idx].end = at + width
		}
	}
	best := -1
	for i := range stats {
		if best < 0 || stats[i].count > stats[best].count {
			best = i
		}
	}
	if best < 0 || stats[best].count == 0 {
		return Evidence{}, false, nil
	}
	stat := stats[best]
	return Evidence{Script: p.Siblings[best], Count: stat.count, Sample: text[stat.start:stat.end]}, true, nil
}
