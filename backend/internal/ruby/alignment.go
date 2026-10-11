package ruby

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"reflect"
	"sort"
	"strings"
)

const (
	AlignmentStateVersion     = 1
	AlignmentValidatorVersion = 1
	ProtocolLegacy            = 1
	ProtocolV2                = 2
)

type AlignmentItem struct {
	Item     Item `json:"item"`
	Preserve bool `json:"preserve"`
}

type VerifiedAlignment struct {
	ID             string `json:"id"`
	Base           string `json:"base"`
	Text           string `json:"text"`
	Kind           string `json:"kind"`
	Occurrence     int    `json:"occurrence"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
	SourceFallback bool   `json:"source_fallback"`
}

// AlignmentState is a task-private serializable draft. Translation, Items,
// Regions and versions are immutable after construction; only Verified grows.
// Digest covers every immutable field. No Aligned flag is a persisted fact.
type AlignmentState struct {
	Version          int                 `json:"version"`
	ValidatorVersion int                 `json:"validator_version"`
	ProtocolVersion  int                 `json:"protocol_version"`
	Translation      string              `json:"translation"`
	Format           string              `json:"format"`
	Items            []AlignmentItem     `json:"items"`
	Regions          []TranslationRegion `json:"regions"`
	Digest           string              `json:"digest"`
	Verified         []VerifiedAlignment `json:"verified"`
}

type AlignmentProgress struct {
	Added    int
	Rejected map[string]string
}

// ClassifyInitialItems freezes only trustworthy classification supplied with
// the main response. A repeated/missing ID or malformed row cannot strip a
// source item. Existing classifications are not overwritten by the model.
func ClassifyInitialItems(items []Item, output []OutputEntry) []Item {
	classified := append([]Item(nil), items...)
	counts := make(map[string]int, len(output))
	for _, entry := range output {
		counts[entry.ID]++
	}
	kinds := make(map[string]string, len(output))
	for _, entry := range output {
		if entry.ID != "" && counts[entry.ID] == 1 && !entry.Invalid && entry.Base != "" && entry.Text != "" && isValidKind(entry.Kind) {
			kinds[entry.ID] = entry.Kind
		}
	}
	for i := range classified {
		if classified[i].Kind == "" {
			classified[i].Kind = kinds[classified[i].ID]
		}
	}
	return classified
}

func NewAlignmentState(translation, format string, items []Item, keep map[string]bool, protocolVersion int) (*AlignmentState, error) {
	if protocolVersion != ProtocolLegacy && protocolVersion != ProtocolV2 {
		return nil, errors.New("unsupported ruby protocol version")
	}
	regions, err := BuildTranslationRegions(translation, format)
	if err != nil {
		return nil, err
	}
	s := &AlignmentState{Version: AlignmentStateVersion, ValidatorVersion: AlignmentValidatorVersion, ProtocolVersion: protocolVersion, Translation: translation, Format: format, Regions: regions}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.ID == "" || seen[it.ID] {
			return nil, errors.New("ruby source item IDs must be nonempty and unique")
		}
		seen[it.ID] = true
		it.Aligned = false
		s.Items = append(s.Items, AlignmentItem{Item: it, Preserve: keep == nil || it.Kind == "" || keep[it.Kind]})
	}
	s.Digest = s.immutableDigest()
	return s, nil
}

func (s *AlignmentState) immutableDigest() string {
	immutable := *s
	immutable.Digest = ""
	immutable.Verified = nil
	b, _ := json.Marshal(immutable)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Validate must run after loading a draft. It refuses unsupported/corrupt data
// without discarding it or silently translating again.
func (s *AlignmentState) Validate() error {
	if s == nil || s.Version != AlignmentStateVersion || s.ValidatorVersion != AlignmentValidatorVersion || (s.ProtocolVersion != ProtocolLegacy && s.ProtocolVersion != ProtocolV2) {
		return errors.New("unsupported ruby alignment state version")
	}
	if s.Digest == "" || s.Digest != s.immutableDigest() {
		return errors.New("ruby alignment state digest mismatch")
	}
	regions, err := BuildTranslationRegions(s.Translation, s.Format)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(regions, s.Regions) {
		return errors.New("ruby alignment regions cannot be reproduced")
	}
	items := make(map[string]AlignmentItem, len(s.Items))
	for _, it := range s.Items {
		if it.Item.ID == "" {
			return errors.New("ruby alignment item missing ID")
		}
		if _, ok := items[it.Item.ID]; ok {
			return errors.New("ruby alignment duplicate source ID")
		}
		items[it.Item.ID] = it
	}
	seen := make(map[string]bool, len(s.Verified))
	for i, v := range s.Verified {
		it, ok := items[v.ID]
		if !ok || !it.Preserve || seen[v.ID] || v.Text == "" || !isValidKind(v.Kind) {
			return errors.New("invalid verified ruby entry")
		}
		seen[v.ID] = true
		locations := s.occurrences(v.Base)
		if v.Occurrence < 1 || v.Occurrence > len(locations) {
			return errors.New("verified ruby occurrence missing")
		}
		p := locations[v.Occurrence-1]
		_, legalCount := legalOccurrences(locations)
		if !p.valid || p.start != v.Start || p.end != v.End || (v.SourceFallback && (v.Base != it.Item.SourceBase || legalCount != 1)) {
			return errors.New("verified ruby position mismatch")
		}
		for _, earlier := range s.Verified[:i] {
			if overlaps(v.Start, v.End, earlier.Start, earlier.End) {
				return errors.New("verified ruby intervals overlap")
			}
		}
	}
	return nil
}

func (s *AlignmentState) Missing() []Item {
	done := make(map[string]bool, len(s.Verified))
	for _, v := range s.Verified {
		done[v.ID] = true
	}
	var missing []Item
	for _, it := range s.Items {
		if it.Preserve && !done[it.Item.ID] {
			missing = append(missing, it.Item)
		}
	}
	return missing
}

// ApplyOutput validates the complete response before accepting any intervals.
// requestedIDs is the exact frozen subset sent for this request. Set
// requireOccurrence for a v2 alignment request, but not an initial main response.
func (s *AlignmentState) ApplyOutput(requestedIDs []string, entries []OutputEntry, requireOccurrence bool) AlignmentProgress {
	progress := AlignmentProgress{Rejected: make(map[string]string)}
	requested := make(map[string]bool, len(requestedIDs))
	for _, id := range requestedIDs {
		requested[id] = true
	}
	missing := make(map[string]Item)
	for _, it := range s.Missing() {
		missing[it.ID] = it
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.ID]++
	}
	var proposed []VerifiedAlignment
	for _, e := range entries {
		it, ok := missing[e.ID]
		if e.ID == "" || !requested[e.ID] || !ok {
			progress.Rejected[e.ID] = "unrequested_id"
			continue
		}
		if counts[e.ID] != 1 {
			progress.Rejected[e.ID] = "duplicate_id"
			continue
		}
		if e.Invalid || e.Base == "" || e.Text == "" || !isValidKind(e.Kind) || e.Occurrence < 0 || (requireOccurrence && e.Occurrence == 0) {
			progress.Rejected[e.ID] = "invalid_entry"
			continue
		}
		locations := s.occurrences(e.Base)
		base, occurrence, fallback := e.Base, e.Occurrence, false
		_, legalCount := legalOccurrences(locations)
		if legalCount == 0 && it.SourceBase != "" && it.SourceBase != e.Base {
			locations = s.occurrences(it.SourceBase)
			position, count := legalOccurrences(locations)
			if count == 1 {
				base, occurrence, fallback = it.SourceBase, position, true
			} else {
				progress.Rejected[e.ID] = "source_fallback_ambiguous"
				continue
			}
		}
		if occurrence == 0 && !requireOccurrence {
			if position, count := legalOccurrences(locations); count == 1 {
				occurrence = position
			}
		}
		if occurrence < 1 || occurrence > len(locations) {
			progress.Rejected[e.ID] = "invalid_occurrence"
			continue
		}
		p := locations[occurrence-1]
		if !p.valid {
			progress.Rejected[e.ID] = "unsafe_text_boundary"
			continue
		}
		v := VerifiedAlignment{ID: e.ID, Base: base, Text: e.Text, Kind: e.Kind, Occurrence: occurrence, Start: p.start, End: p.end, SourceFallback: fallback}
		conflict := false
		for _, old := range s.Verified {
			if overlaps(v.Start, v.End, old.Start, old.End) {
				conflict = true
				break
			}
		}
		if conflict {
			progress.Rejected[e.ID] = "verified_interval_conflict"
			continue
		}
		proposed = append(proposed, v)
	}
	conflicts := make(map[string]bool)
	for i, a := range proposed {
		for _, b := range proposed[i+1:] {
			if overlaps(a.Start, a.End, b.Start, b.End) {
				conflicts[a.ID] = true
				conflicts[b.ID] = true
			}
		}
	}
	for _, v := range proposed {
		if conflicts[v.ID] {
			progress.Rejected[v.ID] = "interval_conflict"
			continue
		}
		s.Verified = append(s.Verified, v)
		progress.Added++
	}
	return progress
}

type alignmentInterval struct {
	start, end int
	valid      bool
}

// legalOccurrences retains the full view ordinal, including occurrences that
// cannot be mapped without splitting an entity. Filtering must not renumber a
// later match seen by the model.
func legalOccurrences(locations []alignmentInterval) (position, count int) {
	for i, location := range locations {
		if location.valid {
			position = i + 1
			count++
		}
	}
	return position, count
}

func (s *AlignmentState) occurrences(base string) []alignmentInterval {
	if base == "" {
		return nil
	}
	var found []alignmentInterval
	for _, region := range s.Regions {
		for from := 0; from <= len(region.Text)-len(base); {
			i := strings.Index(region.Text[from:], base)
			if i < 0 {
				break
			}
			start := from + i
			end := start + len(base)
			rawStart, okStart := region.rawBoundary(start)
			rawEnd, okEnd := region.rawBoundary(end)
			found = append(found, alignmentInterval{start: rawStart, end: rawEnd, valid: okStart && okEnd})
			from = end
		}
	}
	return found
}

func overlaps(start, end, otherStart, otherEnd int) bool { return start < otherEnd && otherStart < end }

func (s *AlignmentState) Render() (string, RestoreResult, error) {
	if err := s.Validate(); err != nil {
		return "", RestoreResult{}, err
	}
	result := RestoreResult{Matched: len(s.Verified)}
	for _, it := range s.Items {
		if it.Preserve {
			result.Total++
		}
	}
	ordered := append([]VerifiedAlignment(nil), s.Verified...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	var b strings.Builder
	start := 0
	for _, v := range ordered {
		b.WriteString(s.Translation[start:v.Start])
		fmt.Fprintf(&b, "<ruby>%s<rt>%s</rt></ruby>", s.Translation[v.Start:v.End], html.EscapeString(v.Text))
		start = v.End
	}
	b.WriteString(s.Translation[start:])
	return b.String(), result, nil
}
