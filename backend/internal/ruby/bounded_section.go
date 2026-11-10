package ruby

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func ParseInlineMarkersBounded(b *backend.BuildBounds, text string) ([]OutputEntry, error) {
	var out []OutputEntry
	for from := 0; from < len(text); {
		if e := b.ReserveNodes(1, 256); e != nil {
			return nil, e
		}
		loc, findErr := backend.FindRegexpSubmatch(b, inlineMarkerRe, text[from:])
		if findErr != nil {
			return nil, findErr
		}
		if loc == nil {
			break
		}
		entry := OutputEntry{Base: text[from+loc[2] : from+loc[3]], Text: text[from+loc[4] : from+loc[5]]}
		if len(loc) > 7 && loc[6] >= 0 {
			entry.Kind = text[from+loc[6] : from+loc[7]]
		}
		var e error
		out, e = backend.AppendNode(b, out, entry)
		if e != nil {
			return nil, e
		}
		from += loc[1]
	}
	return out, nil
}
func ParseSectionRubyOutputBounded(b *backend.BuildBounds, lines []string) (map[string][]OutputEntry, error) {
	if e := b.ReserveNodes(1, 1024); e != nil {
		return nil, e
	}
	out := make(map[string][]OutputEntry)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if e := b.ReserveNodes(1, 256); e != nil {
			return nil, e
		}
		loc, findErr := backend.FindRegexpSubmatch(b, sectionRubyLineRe, line)
		if findErr != nil {
			return nil, findErr
		}
		if loc == nil {
			continue
		}
		id, content := line[loc[2]:loc[3]], line[loc[4]:loc[5]]
		var entry OutputEntry
		if strings.HasPrefix(strings.TrimSpace(content), "\"") {
			fields, n := splitTextFieldsBounded(content, 5)
			var e error
			entry, e = parseTextFieldsBounded(b, fields[:n], false)
			if e != nil {
				return nil, e
			}
		} else {
			base, text, kind, itemID, ok := ParseSectionLine(content)
			if !ok {
				continue
			}
			entry = OutputEntry{Base: base, Text: text, Kind: kind, ID: itemID}
		}
		if e := b.ReserveNodes(1, 1024); e != nil {
			return nil, e
		}
		rows, e := backend.AppendNode(b, out[id], entry)
		if e != nil {
			return nil, e
		}
		out[id] = rows
	}
	return out, nil
}
