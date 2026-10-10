package ruby

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// BatchProtocolVersion versions the envelope independently of the v2 rows.
const BatchProtocolVersion = 1

type AlignmentBatchKey struct {
	WorkID      string
	CandidateID string
}

type AlignmentBatchMember struct {
	WorkID      string
	CandidateID string
	Source      string
	Alignment   *AlignmentState
}

// AlignmentBatchResult never associates members by order. Entries contains
// complete, explicitly identified members, including valid empty results.
// Invalid marks identifiable member failures; malformed individual mappings
// remain OutputEntry.Invalid so the existing validator can reject duplicate IDs.
// EnvelopeInvalid does not invalidate safely decoded Entries (e.g. on a later
// truncated member). Callers compare both maps with the actual request set.
type AlignmentBatchResult struct {
	Entries         map[AlignmentBatchKey][]OutputEntry
	Invalid         map[AlignmentBatchKey]bool
	EnvelopeInvalid bool
	Diagnostics     []string
}

// AlignmentBatchRequest reuses the exact public single-member view. Coordinates
// and already verified entries never appear in the payload.
func AlignmentBatchRequest(members []AlignmentBatchMember) (string, error) {
	if len(members) == 0 {
		return "", errors.New("ruby alignment batch must contain members")
	}
	type memberPayload struct {
		WorkID      string `json:"work_id"`
		CandidateID string `json:"candidate_id"`
		alignmentRequestPayload
	}
	payload := struct {
		Alignments []memberPayload `json:"alignments"`
	}{Alignments: make([]memberPayload, 0, len(members))}
	seen := make(map[AlignmentBatchKey]bool, len(members))
	for _, member := range members {
		key := AlignmentBatchKey{member.WorkID, member.CandidateID}
		if key.WorkID == "" || key.CandidateID == "" || !utf8.ValidString(key.WorkID) || !utf8.ValidString(key.CandidateID) || seen[key] {
			return "", errors.New("ruby alignment batch identities must be nonempty, valid UTF-8 and unique")
		}
		seen[key] = true
		if err := member.Alignment.Validate(); err != nil {
			return "", fmt.Errorf("ruby alignment batch member %q: %w", key.WorkID, err)
		}
		if member.Alignment.ProtocolVersion != ProtocolV2 {
			return "", errors.New("ruby alignment batch requires v2 member protocol")
		}
		payload.Alignments = append(payload.Alignments, memberPayload{
			WorkID: key.WorkID, CandidateID: key.CandidateID,
			alignmentRequestPayload: member.Alignment.alignmentRequestPayload(member.Source, member.Alignment.Missing()),
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode ruby alignment batch: %w", err)
	}
	return string(data), nil
}

func AlignmentBatchJSONSchema() map[string]any {
	output := AlignmentJSONSchema()["properties"].(map[string]any)["ruby_output"]
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"alignments"},
		"properties": map[string]any{"alignments": map[string]any{
			"type": "array", "items": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"work_id", "candidate_id", "ruby_output"},
				"properties": map[string]any{
					"work_id":      map[string]any{"type": "string", "minLength": 1},
					"candidate_id": map[string]any{"type": "string", "minLength": 1},
					"ruby_output":  output,
				},
			},
		}},
	}
}

func newAlignmentBatchResult() AlignmentBatchResult {
	return AlignmentBatchResult{
		Entries: make(map[AlignmentBatchKey][]OutputEntry),
		Invalid: make(map[AlignmentBatchKey]bool),
	}
}

func (r *AlignmentBatchResult) envelopeError(message string) {
	r.EnvelopeInvalid = true
	r.Diagnostics = append(r.Diagnostics, message)
}

func (r *AlignmentBatchResult) invalidate(key AlignmentBatchKey, message string) {
	r.Invalid[key] = true
	delete(r.Entries, key)
	r.Diagnostics = append(r.Diagnostics, message)
}

// ParseAlignmentBatchJSON streams complete member objects out of the envelope.
// It never repairs JSON, scans for embedded objects, or guesses a truncated row.
func ParseAlignmentBatchJSON(text string) AlignmentBatchResult {
	parser := alignmentBatchJSONParser{
		result:    newAlignmentBatchResult(),
		seen:      make(map[AlignmentBatchKey]bool),
		conflicts: make(map[string][]map[string]bool),
	}
	parser.parse(text)
	// Duplicate identity fields can name more than one key. Index conflicts
	// by work ID, without allocating a cross-product of arbitrary identities.
	for key := range parser.result.Entries {
		for _, candidates := range parser.conflicts[key.WorkID] {
			if candidates[key.CandidateID] {
				parser.result.invalidate(key, "member identity conflicts with another object")
				break
			}
		}
	}
	return parser.result
}

type alignmentBatchJSONParser struct {
	result    AlignmentBatchResult
	seen      map[AlignmentBatchKey]bool
	conflicts map[string][]map[string]bool
}

func (parser *alignmentBatchJSONParser) parse(text string) {
	dec := json.NewDecoder(strings.NewReader(text))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		parser.result.envelopeError("batch response must be an object containing alignments")
		return
	}
	seenAlignments := false
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			parser.result.envelopeError("batch response field is incomplete")
			return
		}
		if token != "alignments" {
			parser.result.envelopeError("batch response contains an unexpected field")
			var ignored json.RawMessage
			if dec.Decode(&ignored) != nil {
				return
			}
			continue
		}
		if seenAlignments {
			parser.result.envelopeError("batch response contains duplicate alignments fields")
		}
		seenAlignments = true
		if token, err := dec.Token(); err != nil || token != json.Delim('[') {
			parser.result.envelopeError("batch alignments must be an array")
			return
		}
		for dec.More() {
			memberStart := dec.InputOffset()
			var member json.RawMessage
			if err := dec.Decode(&member); err != nil {
				parser.noteIncompleteMember(text[memberStart:])
				parser.result.envelopeError("batch member is incomplete or malformed")
				return
			}
			parser.parseMember(member)
		}
		if token, err := dec.Token(); err != nil || token != json.Delim(']') {
			parser.result.envelopeError("batch alignments array is incomplete")
			return
		}
	}
	if !seenAlignments {
		parser.result.envelopeError("batch response is missing alignments")
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		parser.result.envelopeError("batch response object is incomplete")
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		parser.result.envelopeError("batch response contains trailing data")
	}
}

// noteIncompleteMember only records complete identity fields. It never returns
// mappings from a partial object, but prevents a later truncated duplicate from
// making the first object appear uniquely associated.
func (p *alignmentBatchJSONParser) noteIncompleteMember(text string) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, ",") {
		text = strings.TrimSpace(text[1:])
	}
	dec := json.NewDecoder(strings.NewReader(text))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return
	}
	works, candidates := make(map[string]bool), make(map[string]bool)
	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			break
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			break
		}
		if name == "work_id" || name == "candidate_id" {
			id, ok := batchIdentity(value)
			if ok && name == "work_id" {
				works[id] = true
			} else if ok {
				candidates[id] = true
			}
		}
	}
	for work := range works {
		p.conflicts[work] = append(p.conflicts[work], candidates)
	}
}

func (p *alignmentBatchJSONParser) parseMember(raw json.RawMessage) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		p.result.envelopeError("batch member must be an object")
		return
	}
	fields := make(map[string]json.RawMessage)
	works, candidates := make(map[string]bool), make(map[string]bool)
	invalid := false
	for dec.More() {
		token, err := dec.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			p.result.envelopeError("batch member field is malformed")
			return
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			p.result.envelopeError("batch member value is malformed")
			return
		}
		if _, exists := fields[name]; exists {
			invalid = true
		}
		fields[name] = value
		switch name {
		case "work_id", "candidate_id":
			id, ok := batchIdentity(value)
			if !ok {
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
	}
	if len(works) != 1 || len(candidates) != 1 {
		p.result.envelopeError("batch member identities are missing or conflicting")
		for work := range works {
			p.conflicts[work] = append(p.conflicts[work], candidates)
		}
		return
	}
	key := AlignmentBatchKey{}
	for key.WorkID = range works {
	}
	for key.CandidateID = range candidates {
	}
	if p.seen[key] {
		p.result.invalidate(key, "batch response contains a duplicate member")
		return
	}
	p.seen[key] = true
	entries, err := parseOutputArray(fields["ruby_output"], true, true)
	if invalid || err != nil {
		p.result.invalidate(key, "batch member fields or ruby_output array are invalid")
		return
	}
	p.result.Entries[key] = entries
}

func batchIdentity(raw []byte) (string, bool) {
	raw = bytes.TrimSpace(raw)
	var identity string
	if len(raw) == 0 || raw[0] != '"' || !utf8.Valid(raw) || json.Unmarshal(raw, &identity) != nil || identity == "" {
		return "", false
	}
	return identity, true
}

// ParseAlignmentBatchText groups explicitly identified v2 rows. The sole empty
// member marker has four empty body strings and occurrence 0; it cannot be
// repeated or mixed with mappings. Multiple mapping rows are ordinary output.
func ParseAlignmentBatchText(text string) AlignmentBatchResult {
	result := newAlignmentBatchResult()
	empty := make(map[AlignmentBatchKey]bool)
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := splitQuotedTextFieldsLimit(line, 7)
		if len(fields) < 2 {
			result.envelopeError("batch text row is missing member identities")
			continue
		}
		work, workOK := batchIdentity([]byte(fields[0]))
		candidate, candidateOK := batchIdentity([]byte(fields[1]))
		if !workOK || !candidateOK {
			result.envelopeError("batch text member identities are invalid")
			continue
		}
		key := AlignmentBatchKey{work, candidate}
		if result.Invalid[key] {
			continue
		}
		if batchTextEmptyMarker(fields) {
			if _, exists := result.Entries[key]; exists {
				result.invalidate(key, "batch text empty marker is repeated or mixed with mappings")
				continue
			}
			empty[key] = true
			result.Entries[key] = []OutputEntry{}
			continue
		}
		if empty[key] {
			result.invalidate(key, "batch text mappings follow an empty marker")
			continue
		}
		entry := parseQuotedTextEntry(strings.Join(fields[2:], "|"), true)
		result.Entries[key] = append(result.Entries[key], entry)
	}
	return result
}

func batchTextEmptyMarker(fields []string) bool {
	if len(fields) != 7 || strings.TrimSpace(fields[6]) != "0" {
		return false
	}
	for _, field := range fields[2:6] {
		if strings.TrimSpace(field) != `""` {
			return false
		}
	}
	return true
}
