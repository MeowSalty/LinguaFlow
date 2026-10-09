package ruby

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ParseAlignmentJSONV2 decodes successful rows independently. Malformed rows
// retain their identifiable ID as Invalid, allowing complete duplicate checks.
// Unlike the legacy repair adapter it never trims strings or guesses fields.
func ParseAlignmentJSONV2(text string) ([]OutputEntry, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("ruby response is not valid UTF-8")
	}
	dec := json.NewDecoder(strings.NewReader(text))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') || !dec.More() {
		return nil, errors.New("ruby response must contain only ruby_output")
	}
	token, err = dec.Token()
	if err != nil || token != "ruby_output" {
		return nil, errors.New("ruby response missing ruby_output")
	}
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("ruby response contains duplicate or unexpected fields")
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("ruby response contains trailing data")
	}
	return parseOutputArray(raw, true, true)
}

// ParseOutputEntries decodes optional main-response/legacy mappings, preserving
// individual invalid rows. Occurrence is optional; final association and safety
// validation are performed against the candidate and the actual request set.
func ParseOutputEntries(raw json.RawMessage) ([]OutputEntry, error) {
	return parseOutputArray(raw, false, false)
}

// ParseOutputMap isolates malformed optional ruby output from valid translations
// and revisions. A bad segment or row cannot discard other segments' mappings.
func ParseOutputMap(raw json.RawMessage) map[string][]OutputEntry {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil
	}
	out := make(map[string][]OutputEntry)
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		id, ok := token.(string)
		if err != nil || !ok {
			return nil
		}
		var entries json.RawMessage
		if err := decoder.Decode(&entries); err != nil {
			return nil
		}
		if seen[id] {
			delete(out, id)
			continue
		}
		seen[id] = true
		parsed, err := ParseOutputEntries(entries)
		if err == nil {
			out[id] = parsed
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil
	}
	return out
}

func parseOutputArray(raw json.RawMessage, requireOccurrence, strict bool) ([]OutputEntry, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("ruby_output must be an array")
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	out := make([]OutputEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, decodeOutputEntry(entry, requireOccurrence, strict))
	}
	return out, nil
}

func decodeOutputEntry(raw json.RawMessage, requireOccurrence, strict bool) OutputEntry {
	entry := OutputEntry{}
	fields := make(map[string]json.RawMessage)
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		entry.Invalid = true
		return entry
	}
	for dec.More() {
		token, err = dec.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			entry.Invalid = true
			return entry
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			entry.Invalid = true
			return entry
		}
		if _, exists := fields[name]; exists {
			entry.Invalid = true
		}
		fields[name] = value
	}
	if _, err = dec.Token(); err != nil {
		entry.Invalid = true
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		entry.Invalid = true
	}
	for name, value := range fields {
		switch name {
		case "id":
			err = json.Unmarshal(value, &entry.ID)
		case "base":
			err = json.Unmarshal(value, &entry.Base)
		case "text":
			err = json.Unmarshal(value, &entry.Text)
		case "kind":
			err = json.Unmarshal(value, &entry.Kind)
		case "occurrence":
			err = json.Unmarshal(value, &entry.Occurrence)
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || entry.Occurrence < 1 {
				entry.Invalid = true
			}
		default:
			err = nil
			if strict {
				entry.Invalid = true
			}
		}
		if err != nil {
			entry.Invalid = true
		}
	}
	if strict && (entry.ID == "" || entry.Base == "" || entry.Text == "" || !isValidKind(entry.Kind)) {
		entry.Invalid = true
	}
	if requireOccurrence && entry.Occurrence < 1 {
		entry.Invalid = true
	}
	return entry
}

// ParseAlignmentTextV2 accepts five fields. Delimiters inside JSON strings are
// ordinary text; decoded whitespace, quotes, newlines and backslashes survive.
// Invalid rows never fall back to v1's optional-ID/rightmost-pipe heuristics.
func ParseAlignmentTextV2(text string) []OutputEntry {
	var entries []OutputEntry
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entries = append(entries, parseQuotedTextEntry(line, true))
	}
	return entries
}

// splitQuotedTextFields finds boundaries before decoding individual literals.
// A malformed but delimited field must not hide a later valid ID from duplicate
// rejection. An unterminated string remains ambiguous and is never guessed.
func splitQuotedTextFields(text string) []string {
	fields := make([]string, 0, 5)
	start := 0
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		if inString {
			if escaped {
				escaped = false
			} else if text[i] == '\\' {
				escaped = true
			} else if text[i] == '"' {
				inString = false
			}
			continue
		}
		if text[i] == '"' {
			inString = true
		} else if text[i] == '|' {
			fields = append(fields, text[start:i])
			start = i + 1
			// Six fields already prove the row invalid; bound temporary metadata
			// even when a malformed response contains millions of delimiters.
			if len(fields) == 5 {
				break
			}
		}
	}
	return append(fields, text[start:])
}

// Main requests do not yet have a frozen candidate view, so their quoted text
// form has four fields; unique positions can be verified locally afterwards.
func parsePrimaryTextEntry(text string) OutputEntry {
	return parseQuotedTextEntry(text, false)
}

func parseQuotedTextEntry(text string, requireOccurrence bool) OutputEntry {
	fields := splitQuotedTextFields(text)
	want := 4
	if requireOccurrence {
		want = 5
	}
	entry := OutputEntry{Invalid: len(fields) != want || !utf8.ValidString(text)}
	for i, value := range []*string{&entry.Base, &entry.Text, &entry.Kind, &entry.ID} {
		if i >= len(fields) {
			break
		}
		literal := strings.TrimSpace(fields[i])
		if len(literal) == 0 || literal[0] != '"' || json.Unmarshal([]byte(literal), value) != nil {
			entry.Invalid = true
		}
	}
	if requireOccurrence && len(fields) >= 5 {
		number := strings.TrimSpace(fields[4])
		for _, c := range number {
			if c < '0' || c > '9' {
				entry.Invalid = true
				break
			}
		}
		var err error
		entry.Occurrence, err = strconv.Atoi(number)
		if err != nil || entry.Occurrence < 1 {
			entry.Invalid = true
		}
	}
	entry.Invalid = entry.Invalid || entry.ID == "" || entry.Base == "" || entry.Text == "" || !isValidKind(entry.Kind)
	return entry
}

func AlignmentJSONSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"ruby_output"},
		"properties": map[string]any{"ruby_output": map[string]any{
			"type": "array", "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"id", "base", "text", "kind", "occurrence"},
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "minLength": 1}, "base": map[string]any{"type": "string", "minLength": 1}, "text": map[string]any{"type": "string", "minLength": 1},
					"kind": map[string]any{"type": "string", "enum": ValidKinds}, "occurrence": map[string]any{"type": "integer", "minimum": 1},
				},
			},
		}},
	}
}

// AlignmentRequest is the identical user payload for JSON and text protocols.
// Only public region text is sent; raw coordinates remain internal to Go.
func (s *AlignmentState) AlignmentRequest(source string, missing []Item) string {
	type region struct {
		Text string `json:"text"`
	}
	type item struct {
		ID         string `json:"id"`
		SourceBase string `json:"source_base"`
		SourceText string `json:"source_text"`
	}
	view := make([]region, len(s.Regions))
	for i, r := range s.Regions {
		view[i] = region{Text: r.Text}
	}
	items := make([]item, len(missing))
	for i, it := range missing {
		items[i] = item{ID: it.ID, SourceBase: it.SourceBase, SourceText: it.SourceText}
	}
	data, _ := json.Marshal(struct {
		Source       string   `json:"source"`
		Translation  string   `json:"translation"`
		Regions      []region `json:"translation_regions"`
		RegionDigest string   `json:"region_digest"`
		Missing      []item   `json:"missing"`
	}{StripRubyTags(source), s.Translation, view, s.Digest, items})
	return string(data)
}
