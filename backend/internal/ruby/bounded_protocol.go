package ruby

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func ownedJSONBytes(b *backend.BuildBounds, s string) ([]byte, error) {
	if e := b.ReserveText(int64(len(s)), int64(len(s))); e != nil {
		return nil, e
	}
	out := make([]byte, len(s))
	copy(out, s)
	return out, nil
}
func outputStringLimit(b *backend.BuildBounds) int64 {
	if q := b.OutputLimit(); q > 0 {
		return q
	}
	return b.Snapshot().TextLimit
}
func decodeOutputBounded(b *backend.BuildBounds, v backend.JSONValue, required, strict bool) (OutputEntry, error) {
	entry := OutputEntry{Invalid: v.Kind() != '{' || !utf8.Valid(v.Raw())}
	if v.Kind() != '{' {
		return entry, nil
	}
	if e := b.ReserveNodes(int64(v.Count()/2)+1, 1024); e != nil {
		return entry, e
	}
	seen := make(map[string]bool, v.Count()/2)
	e := v.EachField(func(k, value backend.JSONValue) error {
		name, e := k.String()
		if e != nil {
			return e
		}
		if seen[name] {
			entry.Invalid = true
		}
		seen[name] = true
		switch name {
		case "id":
			e = value.DecodeInto(&entry.ID, outputStringLimit(b))
		case "base":
			e = value.DecodeInto(&entry.Base, outputStringLimit(b))
		case "text":
			e = value.DecodeInto(&entry.Text, outputStringLimit(b))
		case "kind":
			e = value.DecodeInto(&entry.Kind, outputStringLimit(b))
		case "occurrence":
			e = value.DecodeInto(&entry.Occurrence, outputStringLimit(b))
			if value.Kind() == 'n' || entry.Occurrence < 1 {
				entry.Invalid = true
			}
		default:
			if strict {
				entry.Invalid = true
			}
		}
		if errors.Is(e, backend.ErrBuildCapacity) {
			return e
		}
		if e != nil {
			entry.Invalid = true
		}
		return nil
	})
	if e != nil {
		return entry, e
	}
	if strict && (entry.ID == "" || entry.Base == "" || entry.Text == "" || !isValidKind(entry.Kind)) {
		entry.Invalid = true
	}
	if required && entry.Occurrence < 1 {
		entry.Invalid = true
	}
	return entry, nil
}
func outputArrayBounded(b *backend.BuildBounds, v backend.JSONValue, required, strict bool) ([]OutputEntry, error) {
	if v.Kind() != '[' {
		return nil, errors.New("ruby_output must be an array")
	}
	if e := b.ReserveNodes(int64(v.Count()), 128); e != nil {
		return nil, e
	}
	out := make([]OutputEntry, 0, v.Count())
	e := v.EachArray(func(value backend.JSONValue) error {
		entry, e := decodeOutputBounded(b, value, required, strict)
		if e != nil {
			return e
		}
		out = append(out, entry)
		return nil
	})
	return out, e
}
func ParseOutputEntriesBounded(b *backend.BuildBounds, data []byte) ([]OutputEntry, error) {
	v, e := backend.NewJSONView(b, data)
	if e != nil {
		return nil, e
	}
	return outputArrayBounded(b, v, false, false)
}
func ParseOutputMapBounded(b *backend.BuildBounds, data []byte) (map[string][]OutputEntry, error) {
	v, e := backend.NewJSONView(b, data)
	if e != nil {
		return nil, e
	}
	if v.Kind() != '{' {
		return nil, nil
	}
	if e = b.ReserveNodes(int64(v.Count())+2, 1024); e != nil {
		return nil, e
	}
	out := make(map[string][]OutputEntry, v.Count()/2)
	seen := make(map[string]bool, v.Count()/2)
	e = v.EachField(func(k, val backend.JSONValue) error {
		id, e := k.String()
		if e != nil {
			return e
		}
		if seen[id] {
			delete(out, id)
			return nil
		}
		seen[id] = true
		rows, e := outputArrayBounded(b, val, false, false)
		if errors.Is(e, backend.ErrBuildCapacity) {
			return e
		}
		if e == nil {
			out[id] = rows
		}
		return nil
	})
	return out, e
}
func ParseAlignmentJSONV2Bounded(b *backend.BuildBounds, text string) ([]OutputEntry, error) {
	if !utf8.ValidString(text) {
		return nil, errors.New("ruby response is not valid UTF-8")
	}
	data, e := ownedJSONBytes(b, text)
	if e != nil {
		return nil, e
	}
	v, e := backend.NewJSONView(b, data)
	if e != nil {
		return nil, e
	}
	if v.Kind() != '{' || v.Count() != 2 {
		return nil, errors.New("ruby response must contain only ruby_output")
	}
	var out []OutputEntry
	e = v.EachField(func(k, value backend.JSONValue) error {
		name, e := k.String()
		if e != nil {
			return e
		}
		if name != "ruby_output" {
			return errors.New("ruby response missing ruby_output")
		}
		out, e = outputArrayBounded(b, value, true, true)
		return e
	})
	return out, e
}

func splitTextFieldsBounded(line string, limit int) (fields [8]string, n int) {
	start := 0
	inString, escaped := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
		} else if c == '|' {
			fields[n] = line[start:i]
			n++
			start = i + 1
			if n == limit {
				break
			}
		}
	}
	fields[n] = line[start:]
	return fields, n + 1
}
func decodeQuotedBounded(b *backend.BuildBounds, s string, dst *string) error {
	s = strings.TrimSpace(s)
	data, e := ownedJSONBytes(b, s)
	if e != nil {
		return e
	}
	return b.DecodeJSONLimited(data, dst, outputStringLimit(b))
}
func parseTextFieldsBounded(b *backend.BuildBounds, fields []string, required bool) (OutputEntry, error) {
	want := 4
	if required {
		want = 5
	}
	entry := OutputEntry{Invalid: len(fields) != want}
	for i, dst := range []*string{&entry.Base, &entry.Text, &entry.Kind, &entry.ID} {
		if i >= len(fields) {
			break
		}
		e := decodeQuotedBounded(b, fields[i], dst)
		if errors.Is(e, backend.ErrBuildCapacity) {
			return entry, e
		}
		if e != nil {
			entry.Invalid = true
		}
	}
	if required && len(fields) >= 5 {
		number := strings.TrimSpace(fields[4])
		if len(number) > 20 {
			entry.Invalid = true
		} else {
			for _, c := range number {
				if c < '0' || c > '9' {
					entry.Invalid = true
					break
				}
			}
			if e := b.ReserveNodes(1, 128); e != nil {
				return entry, e
			}
			v, e := strconv.Atoi(number)
			entry.Occurrence = v
			if e != nil || v < 1 {
				entry.Invalid = true
			}
		}
	}
	entry.Invalid = entry.Invalid || entry.ID == "" || entry.Base == "" || entry.Text == "" || !isValidKind(entry.Kind)
	return entry, nil
}
func ParseAlignmentTextV2Bounded(b *backend.BuildBounds, text string) ([]OutputEntry, error) {
	var out []OutputEntry
	for text != "" {
		line, tail, ok := strings.Cut(text, "\n")
		text = tail
		if !ok {
			text = ""
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields, n := splitTextFieldsBounded(line, 5)
		entry, e := parseTextFieldsBounded(b, fields[:n], true)
		if e != nil {
			return out, e
		}
		out, e = backend.AppendNode(b, out, entry)
		if e != nil {
			return out, e
		}
	}
	return out, nil
}
func ParseAlignmentTextLegacyBounded(b *backend.BuildBounds, text string) ([]OutputEntry, error) {
	var out []OutputEntry
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		entry := OutputEntry{Invalid: true}
		last := strings.LastIndexByte(line, '|')
		if last >= 0 {
			field := strings.TrimSpace(line[last+1:])
			if isValidKind(field) {
				if e := b.ReserveNodes(4, 128); e != nil {
					return out, e
				}
				entry.Base, entry.Text, entry.Kind, _, _ = ParseSectionLine(line)
			} else {
				entry.ID = trimIDPrefix(field)
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
			}
		}
		var e error
		out, e = backend.AppendNode(b, out, entry)
		if e != nil {
			return out, e
		}
	}
	return out, nil
}

type jsonCursor struct {
	b    *backend.BuildBounds
	data []byte
	pos  int
}

func (c *jsonCursor) space() {
	for c.pos < len(c.data) && (c.data[c.pos] == ' ' || c.data[c.pos] == '\n' || c.data[c.pos] == '\r' || c.data[c.pos] == '\t') {
		c.pos++
	}
}
func (c *jsonCursor) take(ch byte) bool {
	c.space()
	if c.pos < len(c.data) && c.data[c.pos] == ch {
		c.pos++
		return true
	}
	return false
}
func (c *jsonCursor) raw() ([]byte, error) {
	raw, n, e := backend.FirstJSONRaw(c.b, c.data[c.pos:])
	if e != nil {
		return nil, e
	}
	c.pos += n
	return raw, nil
}
func (c *jsonCursor) string() (string, error) {
	raw, e := c.raw()
	if e != nil {
		return "", e
	}
	var s string
	e = c.b.DecodeJSONLimited(raw, &s, outputStringLimit(c.b))
	return s, e
}
func resultErrorBounded(b *backend.BuildBounds, r *AlignmentBatchResult, key *AlignmentBatchKey, message string) error {
	if e := b.ReserveNodes(2, 1024); e != nil {
		return e
	}
	if key == nil {
		r.EnvelopeInvalid = true
	} else {
		r.Invalid[*key] = true
		delete(r.Entries, *key)
	}
	var e error
	r.Diagnostics, e = backend.AppendNode(b, r.Diagnostics, message)
	return e
}

type memberConflict struct{ works, candidates map[string]bool }

func ParseAlignmentBatchJSONBounded(b *backend.BuildBounds, text string) (AlignmentBatchResult, error) {
	if e := backend.CheckJSONDepth(text); e != nil {
		return AlignmentBatchResult{}, e
	}
	if e := b.ReserveNodes(4, 1024); e != nil {
		return AlignmentBatchResult{}, e
	}
	result := newAlignmentBatchResult()
	seen := make(map[AlignmentBatchKey]bool)
	var conflicts []memberConflict
	data, e := ownedJSONBytes(b, text)
	if e != nil {
		return result, e
	}
	cursor := jsonCursor{b: b, data: data}
	if !cursor.take('{') {
		e = resultErrorBounded(b, &result, nil, "batch response must be an object containing alignments")
		return result, e
	}
	seenAlignments := false
	parseMember := func(raw []byte, partial bool) error {
		if e := b.ReserveNodes(4, 1024); e != nil {
			return e
		}
		works, candidates := make(map[string]bool), make(map[string]bool)
		fields := make(map[string]backend.JSONValue)
		invalid := partial
		if partial {
			c := jsonCursor{b: b, data: raw}
			if c.take('{') {
				for {
					name, e := c.string()
					if e != nil || !c.take(':') {
						if errors.Is(e, backend.ErrBuildCapacity) {
							return e
						}
						break
					}
					value, e := c.raw()
					if e != nil {
						if errors.Is(e, backend.ErrBuildCapacity) {
							return e
						}
						break
					}
					if name == "work_id" || name == "candidate_id" {
						var id string
						decodeErr := b.DecodeJSONLimited(value, &id, outputStringLimit(b))
						if errors.Is(decodeErr, backend.ErrBuildCapacity) {
							return decodeErr
						}
						if decodeErr == nil && id != "" {
							if e := b.ReserveNodes(1, 1024); e != nil {
								return e
							}
							if name == "work_id" {
								works[id] = true
							} else {
								candidates[id] = true
							}
						}
					}
					if !c.take(',') {
						break
					}
				}
			}
		}
		if !partial {
			v, e := backend.NewJSONView(b, raw)
			if e != nil {
				return e
			}
			if v.Kind() != '{' {
				return resultErrorBounded(b, &result, nil, "batch member must be an object")
			}
			e = v.EachField(func(k, value backend.JSONValue) error {
				name, e := k.String()
				if e != nil {
					return e
				}
				if _, ok := fields[name]; ok {
					invalid = true
				}
				if e = b.ReserveNodes(2, 1024); e != nil {
					return e
				}
				fields[name] = value
				switch name {
				case "work_id", "candidate_id":
					var id string
					if e = value.DecodeInto(&id, outputStringLimit(b)); errors.Is(e, backend.ErrBuildCapacity) {
						return e
					}
					if e != nil || id == "" || !utf8.ValidString(id) {
						invalid = true
					} else if name == "work_id" {
						works[id] = true
					} else {
						candidates[id] = true
					}
				case "ruby_output":
				default:
					invalid = true
				}
				return nil
			})
			if e != nil {
				return e
			}
		}
		if len(works) != 1 || len(candidates) != 1 || partial {
			var e error
			conflicts, e = backend.AppendNode(b, conflicts, memberConflict{works, candidates})
			if e != nil {
				return e
			}
			return resultErrorBounded(b, &result, nil, "batch member identities are missing or conflicting")
		}
		key := AlignmentBatchKey{}
		for key.WorkID = range works {
		}
		for key.CandidateID = range candidates {
		}
		if seen[key] {
			return resultErrorBounded(b, &result, &key, "batch response contains a duplicate member")
		}
		if e := b.ReserveNodes(2, 1024); e != nil {
			return e
		}
		seen[key] = true
		value, ok := fields["ruby_output"]
		if !ok || invalid {
			return resultErrorBounded(b, &result, &key, "batch member fields or ruby_output array are invalid")
		}
		rows, e := outputArrayBounded(b, value, true, true)
		if e != nil {
			if errors.Is(e, backend.ErrBuildCapacity) {
				return e
			}
			return resultErrorBounded(b, &result, &key, "batch member fields or ruby_output array are invalid")
		}
		result.Entries[key] = rows
		return nil
	}
	for {
		cursor.space()
		if cursor.take('}') {
			break
		}
		name, e := cursor.string()
		if e != nil || !cursor.take(':') {
			if errors.Is(e, backend.ErrBuildCapacity) {
				return result, e
			}
			if e = resultErrorBounded(b, &result, nil, "batch response field is incomplete"); e != nil {
				return result, e
			}
			break
		}
		if name != "alignments" {
			if e = resultErrorBounded(b, &result, nil, "batch response contains an unexpected field"); e != nil {
				return result, e
			}
			if _, e = cursor.raw(); e != nil {
				if errors.Is(e, backend.ErrBuildCapacity) {
					return result, e
				}
				break
			}
		} else {
			if seenAlignments {
				if e = resultErrorBounded(b, &result, nil, "batch response contains duplicate alignments fields"); e != nil {
					return result, e
				}
			}
			seenAlignments = true
			if !cursor.take('[') {
				if e = resultErrorBounded(b, &result, nil, "batch alignments must be an array"); e != nil {
					return result, e
				}
				break
			}
			for {
				cursor.space()
				if cursor.take(']') {
					break
				}
				start := cursor.pos
				raw, e := cursor.raw()
				if e != nil {
					if errors.Is(e, backend.ErrBuildCapacity) {
						return result, e
					}
					if e = parseMember(bytes.TrimSpace(data[start:]), true); e != nil {
						return result, e
					}
					if e = resultErrorBounded(b, &result, nil, "batch member is incomplete or malformed"); e != nil {
						return result, e
					}
					cursor.pos = len(data)
					break
				}
				if e = parseMember(raw, false); e != nil {
					return result, e
				}
				if cursor.take(',') {
					continue
				}
				if cursor.take(']') {
					break
				}
				if e = resultErrorBounded(b, &result, nil, "batch alignments array is incomplete"); e != nil {
					return result, e
				}
				cursor.pos = len(data)
				break
			}
		}
		if cursor.take(',') {
			continue
		}
		if cursor.take('}') {
			break
		}
		if e = resultErrorBounded(b, &result, nil, "batch response object is incomplete"); e != nil {
			return result, e
		}
		break
	}
	if !seenAlignments {
		if e = resultErrorBounded(b, &result, nil, "batch response is missing alignments"); e != nil {
			return result, e
		}
	}
	cursor.space()
	if cursor.pos < len(data) {
		if e = resultErrorBounded(b, &result, nil, "batch response contains trailing data"); e != nil {
			return result, e
		}
	}
	for key := range result.Entries {
		for _, conflict := range conflicts {
			if conflict.works[key.WorkID] && conflict.candidates[key.CandidateID] {
				if e = resultErrorBounded(b, &result, &key, "member identity conflicts with another object"); e != nil {
					return result, e
				}
				break
			}
		}
	}
	return result, nil
}

func ParseAlignmentBatchTextBounded(b *backend.BuildBounds, text string) (AlignmentBatchResult, error) {
	if e := b.ReserveNodes(3, 1024); e != nil {
		return AlignmentBatchResult{}, e
	}
	result := newAlignmentBatchResult()
	empty := make(map[AlignmentBatchKey]bool)
	for text != "" {
		line, tail, _ := strings.Cut(text, "\n")
		text = tail
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields, n := splitTextFieldsBounded(line, 7)
		if n < 2 {
			if e := resultErrorBounded(b, &result, nil, "batch text row is missing member identities"); e != nil {
				return result, e
			}
			continue
		}
		var work, candidate string
		e := decodeQuotedBounded(b, fields[0], &work)
		if e == nil {
			e = decodeQuotedBounded(b, fields[1], &candidate)
		}
		if errors.Is(e, backend.ErrBuildCapacity) {
			return result, e
		}
		if e != nil || work == "" || candidate == "" {
			if e = resultErrorBounded(b, &result, nil, "batch text member identities are invalid"); e != nil {
				return result, e
			}
			continue
		}
		key := AlignmentBatchKey{work, candidate}
		if result.Invalid[key] {
			continue
		}
		if batchTextEmptyMarker(fields[:n]) {
			if _, exists := result.Entries[key]; exists {
				if e = resultErrorBounded(b, &result, &key, "batch text empty marker is repeated or mixed with mappings"); e != nil {
					return result, e
				}
				continue
			}
			if e = b.ReserveNodes(2, 1024); e != nil {
				return result, e
			}
			empty[key] = true
			result.Entries[key] = []OutputEntry{}
			continue
		}
		if empty[key] {
			if e = resultErrorBounded(b, &result, &key, "batch text mappings follow an empty marker"); e != nil {
				return result, e
			}
			continue
		}
		entry, e := parseTextFieldsBounded(b, fields[2:n], true)
		if e != nil {
			return result, e
		}
		if e = b.ReserveNodes(1, 1024); e != nil {
			return result, e
		}
		rows, e := backend.AppendNode(b, result.Entries[key], entry)
		if e != nil {
			return result, e
		}
		result.Entries[key] = rows
	}
	return result, nil
}
