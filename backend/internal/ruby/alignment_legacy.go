package ruby

import "strings"

// StripLegacyInlineMarkers recovers the body of well-formed legacy markers.
// Marker count and annotation text are not accepted as source-item identities.
func StripLegacyInlineMarkers(text string) string {
	return inlineMarkerRe.ReplaceAllStringFunc(text, func(marker string) string {
		return inlineMarkerRe.FindStringSubmatch(marker)[1]
	})
}

// ParseAlignmentTextLegacy preserves explicit legacy IDs, including malformed
// duplicate rows. The old wire grammar trims fields and accepts a display '#'
// prefix; three-field rows stay unassociated and cannot establish a fact.
func ParseAlignmentTextLegacy(text string) []OutputEntry {
	var entries []OutputEntry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := OutputEntry{Invalid: true}
		last := strings.LastIndexByte(line, '|')
		if last < 0 {
			entries = append(entries, entry)
			continue
		}
		lastField := strings.TrimSpace(line[last+1:])
		if isValidKind(lastField) {
			entry.Base, entry.Text, entry.Kind, _, _ = ParseSectionLine(line)
			entries = append(entries, entry)
			continue
		}
		entry.ID = trimIDPrefix(lastField)
		line = line[:last]
		last = strings.LastIndexByte(line, '|')
		if last >= 0 {
			entry.Kind = strings.TrimSpace(line[last+1:])
			line = line[:last]
			last = strings.LastIndexByte(line, '|')
			if last >= 0 {
				entry.Text = strings.TrimSpace(line[last+1:])
				entry.Base = strings.TrimSpace(line[:last])
				entry.Invalid = entry.ID == "" || entry.Base == "" || entry.Text == "" || !isValidKind(entry.Kind)
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// AlignmentJSONSchemaForProtocol leaves legacy fields optional exactly as the
// frozen format expects. Safety is enforced by AlignmentState after parsing.
func AlignmentJSONSchemaForProtocol(protocol int) map[string]any {
	schema := AlignmentJSONSchema()
	if protocol != ProtocolLegacy {
		return schema
	}
	properties := schema["properties"].(map[string]any)
	items := properties["ruby_output"].(map[string]any)["items"].(map[string]any)
	delete(items["properties"].(map[string]any), "occurrence")
	items["required"] = []string{"base", "text", "kind"}
	return schema
}
