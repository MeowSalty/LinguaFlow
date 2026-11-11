package ruby

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"reflect"
	"sort"
	"strings"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type digestWriter struct {
	h       hash.Hash
	scratch [4096]byte
}

func (w *digestWriter) Write(p []byte) (int, error) { return w.h.Write(p) }
func (w *digestWriter) WriteString(s string) (int, error) {
	n := len(s)
	for len(s) > 0 {
		c := min(len(s), len(w.scratch))
		copy(w.scratch[:], s[:c])
		if _, e := w.h.Write(w.scratch[:c]); e != nil {
			return 0, e
		}
		s = s[c:]
	}
	return n, nil
}
func (s *AlignmentState) immutableDigestBounded(b *backend.BuildBounds) (string, error) {
	if e := b.ReserveNodes(8, 1024); e != nil {
		return "", e
	}
	immutable := *s
	immutable.Digest = ""
	immutable.Verified = nil
	w := &digestWriter{h: sha256.New()}
	if e := b.WriteJSON(w, immutable); e != nil {
		return "", e
	}
	if e := b.ReserveText(64, 64); e != nil {
		return "", e
	}
	return hex.EncodeToString(w.h.Sum(nil)), nil
}

func NewAlignmentStateBounded(b *backend.BuildBounds, translation, format string, items []Item, keep map[string]bool, protocolVersion int) (*AlignmentState, error) {
	if protocolVersion != ProtocolV2 {
		return nil, errors.New("unsupported ruby protocol version")
	}
	regions, e := BuildTranslationRegionsBounded(b, translation, format)
	if e != nil {
		return nil, e
	}
	if e = b.ReserveNodes(int64(len(items))+1, 1024); e != nil {
		return nil, e
	}
	seen := make(map[string]bool, len(items))
	if e = b.ReserveNodes(int64(len(items)), int64(unsafe.Sizeof(AlignmentItem{}))); e != nil {
		return nil, e
	}
	if e = b.ReserveNodes(1, int64(unsafe.Sizeof(AlignmentState{}))); e != nil {
		return nil, e
	}
	s := &AlignmentState{Version: AlignmentStateVersion, ValidatorVersion: AlignmentValidatorVersion, ProtocolVersion: protocolVersion, Translation: translation, Format: format, Regions: regions, Items: make([]AlignmentItem, len(items))}
	for i, it := range items {
		if it.ID == "" || seen[it.ID] {
			return nil, errors.New("ruby source item IDs must be nonempty and unique")
		}
		seen[it.ID] = true
		it.Aligned = false
		s.Items[i] = AlignmentItem{it, keep == nil || it.Kind == "" || keep[it.Kind]}
	}
	s.Digest, e = s.immutableDigestBounded(b)
	return s, e
}

// lookupOccurrences retains ordinals including unsafe entity boundaries, but
// does not allocate an O(number-of-occurrences) slice for repeated text.
func (s *AlignmentState) lookupOccurrences(base string, ordinal int) (selected alignmentInterval, sole alignmentInterval, total, legal, soleOrdinal int) {
	if base == "" {
		return
	}
	for _, r := range s.Regions {
		for from := 0; from <= len(r.Text)-len(base); {
			i := strings.Index(r.Text[from:], base)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(base)
			rs, okS := r.rawBoundary(start)
			re, okE := r.rawBoundary(end)
			v := alignmentInterval{rs, re, okS && okE}
			total++
			if total == ordinal {
				selected = v
			}
			if v.valid {
				legal++
				sole = v
				soleOrdinal = total
			}
			from = end
		}
	}
	return
}

func (s *AlignmentState) ValidateBounded(b *backend.BuildBounds) error {
	if s == nil || s.Version != AlignmentStateVersion || s.ValidatorVersion != AlignmentValidatorVersion || s.ProtocolVersion != ProtocolV2 {
		return errors.New("unsupported ruby alignment state version")
	}
	digest, e := s.immutableDigestBounded(b)
	if e != nil {
		return e
	}
	if s.Digest == "" || digest != s.Digest {
		return errors.New("ruby alignment state digest mismatch")
	}
	regions, e := BuildTranslationRegionsBounded(b, s.Translation, s.Format)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(regions, s.Regions) {
		return errors.New("ruby alignment regions cannot be reproduced")
	}
	if e = b.ReserveNodes(int64(len(s.Items)+len(s.Verified))+2, 1024); e != nil {
		return e
	}
	items := make(map[string]AlignmentItem, len(s.Items))
	seen := make(map[string]bool, len(s.Verified))
	for _, it := range s.Items {
		if it.Item.ID == "" {
			return errors.New("ruby alignment item missing ID")
		}
		if _, ok := items[it.Item.ID]; ok {
			return errors.New("ruby alignment duplicate source ID")
		}
		items[it.Item.ID] = it
	}
	for i, v := range s.Verified {
		it, ok := items[v.ID]
		if !ok || !it.Preserve || seen[v.ID] || v.Text == "" || !isValidKind(v.Kind) {
			return errors.New("invalid verified ruby entry")
		}
		seen[v.ID] = true
		p, _, total, legal, _ := s.lookupOccurrences(v.Base, v.Occurrence)
		if v.Occurrence < 1 || v.Occurrence > total {
			return errors.New("verified ruby occurrence missing")
		}
		if !p.valid || p.start != v.Start || p.end != v.End || (v.SourceFallback && (v.Base != it.Item.SourceBase || legal != 1)) {
			return errors.New("verified ruby position mismatch")
		}
		for _, old := range s.Verified[:i] {
			if overlaps(v.Start, v.End, old.Start, old.End) {
				return errors.New("verified ruby intervals overlap")
			}
		}
	}
	return nil
}
func (s *AlignmentState) MissingBounded(b *backend.BuildBounds) ([]Item, error) {
	if e := b.ReserveNodes(int64(len(s.Verified))+1, 1024); e != nil {
		return nil, e
	}
	done := make(map[string]bool, len(s.Verified))
	for _, v := range s.Verified {
		done[v.ID] = true
	}
	var missing []Item
	for _, it := range s.Items {
		if it.Preserve && !done[it.Item.ID] {
			var e error
			missing, e = backend.AppendNode(b, missing, it.Item)
			if e != nil {
				return nil, e
			}
		}
	}
	return missing, nil
}

func (s *AlignmentState) ApplyOutputBounded(b *backend.BuildBounds, requestedIDs []string, entries []OutputEntry, requireOccurrence bool) (AlignmentProgress, error) {
	if e := b.ReserveNodes(int64(len(requestedIDs)+len(s.Items)+3*len(entries))+5, 1024); e != nil {
		return AlignmentProgress{}, e
	}
	progress := AlignmentProgress{Rejected: make(map[string]string, len(entries))}
	requested := make(map[string]bool, len(requestedIDs))
	for _, id := range requestedIDs {
		requested[id] = true
	}
	miss, e := s.MissingBounded(b)
	if e != nil {
		return progress, e
	}
	missing := make(map[string]Item, len(miss))
	for _, it := range miss {
		missing[it.ID] = it
	}
	counts := make(map[string]int, len(entries))
	for _, entry := range entries {
		counts[entry.ID]++
	}
	var proposed []VerifiedAlignment
	for _, entry := range entries {
		it, ok := missing[entry.ID]
		if entry.ID == "" || !requested[entry.ID] || !ok {
			progress.Rejected[entry.ID] = "unrequested_id"
			continue
		}
		if counts[entry.ID] != 1 {
			progress.Rejected[entry.ID] = "duplicate_id"
			continue
		}
		if entry.Invalid || entry.Base == "" || entry.Text == "" || !isValidKind(entry.Kind) || entry.Occurrence < 0 || (requireOccurrence && entry.Occurrence == 0) {
			progress.Rejected[entry.ID] = "invalid_entry"
			continue
		}
		base, occurrence, fallback := entry.Base, entry.Occurrence, false
		p, sole, total, legal, position := s.lookupOccurrences(base, occurrence)
		if legal == 0 && it.SourceBase != "" && it.SourceBase != base {
			p, sole, total, legal, position = s.lookupOccurrences(it.SourceBase, 0)
			if legal != 1 {
				progress.Rejected[entry.ID] = "source_fallback_ambiguous"
				continue
			}
			base, occurrence, fallback = it.SourceBase, position, true
			p = sole
		}
		if occurrence == 0 && !requireOccurrence && legal == 1 {
			occurrence = position
			p = sole
		}
		if occurrence < 1 || occurrence > total {
			progress.Rejected[entry.ID] = "invalid_occurrence"
			continue
		}
		if !p.valid {
			progress.Rejected[entry.ID] = "unsafe_text_boundary"
			continue
		}
		v := VerifiedAlignment{ID: entry.ID, Base: base, Text: entry.Text, Kind: entry.Kind, Occurrence: occurrence, Start: p.start, End: p.end, SourceFallback: fallback}
		conflict := false
		for _, old := range s.Verified {
			if overlaps(v.Start, v.End, old.Start, old.End) {
				conflict = true
				break
			}
		}
		if conflict {
			progress.Rejected[entry.ID] = "verified_interval_conflict"
			continue
		}
		proposed, e = backend.AppendNode(b, proposed, v)
		if e != nil {
			return progress, e
		}
	}
	conflicts := make(map[string]bool, len(proposed))
	for i, a := range proposed {
		for _, v := range proposed[i+1:] {
			if overlaps(a.Start, a.End, v.Start, v.End) {
				conflicts[a.ID] = true
				conflicts[v.ID] = true
			}
		}
	}
	for _, v := range proposed {
		if conflicts[v.ID] {
			progress.Rejected[v.ID] = "interval_conflict"
			continue
		}
		s.Verified, e = backend.AppendNode(b, s.Verified, v)
		if e != nil {
			return progress, e
		}
		progress.Added++
	}
	return progress, nil
}

func htmlEscapedBytes(s string) int64 {
	var n int64
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&', '\'', '"':
			n += 5
		case '<', '>':
			n += 4
		default:
			n++
		}
	}
	return n
}
func writeHTMLText(w *backend.BoundedWriter, s string) error {
	from := 0
	for i := 0; i < len(s); i++ {
		replacement := ""
		switch s[i] {
		case '&':
			replacement = "&amp;"
		case '\'':
			replacement = "&#39;"
		case '"':
			replacement = "&#34;"
		case '<':
			replacement = "&lt;"
		case '>':
			replacement = "&gt;"
		}
		if replacement == "" {
			continue
		}
		if _, e := w.WriteString(s[from:i]); e != nil {
			return e
		}
		if _, e := w.WriteString(replacement); e != nil {
			return e
		}
		from = i + 1
	}
	_, e := w.WriteString(s[from:])
	return e
}
func (s *AlignmentState) RenderBounded(b *backend.BuildBounds, maxBytes int64) (string, RestoreResult, error) {
	if e := s.ValidateBounded(b); e != nil {
		return "", RestoreResult{}, e
	}
	result := RestoreResult{Matched: len(s.Verified)}
	for _, it := range s.Items {
		if it.Preserve {
			result.Total++
		}
	}
	n := int64(len(s.Translation))
	for _, v := range s.Verified {
		add := int64(len("<ruby><rt></rt></ruby>")) + htmlEscapedBytes(v.Text)
		if add > maxBytes-n {
			return "", result, &backend.BuildCapacityError{Resource: "ruby rendered target bytes"}
		}
		n += add
	}
	if n > maxBytes {
		return "", result, &backend.BuildCapacityError{Resource: "ruby rendered target bytes"}
	}
	if e := b.ReserveNodes(int64(len(s.Verified))+1, int64(unsafe.Sizeof(VerifiedAlignment{}))); e != nil {
		return "", result, e
	}
	if e := b.ReserveNodes(2, 1024); e != nil {
		return "", RestoreResult{}, e
	}
	ordered := make([]VerifiedAlignment, len(s.Verified))
	copy(ordered, s.Verified)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	w, e := backend.NewFixedBoundedWriter(b, n)
	if e != nil {
		return "", result, e
	}
	start := 0
	for _, v := range ordered {
		_, _ = w.WriteString(s.Translation[start:v.Start])
		_, _ = w.WriteString("<ruby>")
		_, _ = w.WriteString(s.Translation[v.Start:v.End])
		_, _ = w.WriteString("<rt>")
		if e = writeHTMLText(w, v.Text); e != nil {
			return "", result, e
		}
		_, _ = w.WriteString("</rt></ruby>")
		start = v.End
	}
	_, _ = w.WriteString(s.Translation[start:])
	if e = b.ReserveText(n, n); e != nil {
		return "", result, e
	}
	return string(w.Bytes()), result, nil
}
