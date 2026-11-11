package ruby

import (
	"errors"
	"io"
	"unicode/utf8"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func StripRubyTextBounded(b *backend.BuildBounds, source string) (string, error) {
	els, e := scanElementsBounded(b, source)
	if e != nil {
		return "", e
	}
	if len(els) == 0 {
		return source, nil
	}
	w := backend.NewBoundedWriter(b, int64(len(source)))
	from := 0
	for _, el := range els {
		if _, e = w.WriteString(source[from:el.start]); e != nil {
			return "", e
		}
		pos := el.start
		for _, rt := range el.rts {
			plain, e := stripPatternBounded(b, source[pos:rt.start], htmlTagRe)
			if e != nil {
				return "", e
			}
			if _, e = w.WriteString(plain); e != nil {
				return "", e
			}
			pos = rt.end
		}
		plain, e := stripPatternBounded(b, source[pos:el.end], htmlTagRe)
		if e != nil {
			return "", e
		}
		if _, e = w.WriteString(plain); e != nil {
			return "", e
		}
		from = el.end
	}
	if _, e = w.WriteString(source[from:]); e != nil {
		return "", e
	}
	if e = b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
		return "", e
	}
	return string(w.Bytes()), nil
}

func (s *AlignmentState) requestPayloadBounded(b *backend.BuildBounds, source string, missing []Item) (alignmentRequestPayload, error) {
	if e := b.ReserveNodes(int64(len(s.Regions)), int64(unsafe.Sizeof(alignmentRequestRegion{}))); e != nil {
		return alignmentRequestPayload{}, e
	}
	regions := make([]alignmentRequestRegion, len(s.Regions))
	for i, r := range s.Regions {
		regions[i] = alignmentRequestRegion{r.Text}
	}
	if e := b.ReserveNodes(int64(len(missing)), int64(unsafe.Sizeof(alignmentRequestItem{}))); e != nil {
		return alignmentRequestPayload{}, e
	}
	items := make([]alignmentRequestItem, len(missing))
	for i, it := range missing {
		items[i] = alignmentRequestItem{it.ID, it.SourceBase, it.SourceText}
	}
	stripped, e := StripRubyTextBounded(b, source)
	return alignmentRequestPayload{stripped, s.Translation, regions, s.Digest, items}, e
}
func (s *AlignmentState) AlignmentRequestBounded(b *backend.BuildBounds, source string, missing []Item, maxBytes int64) (string, error) {
	return s.AlignmentRequestWithCreditsBounded(b, source, missing, "", maxBytes, nil)
}
func (s *AlignmentState) AlignmentRequestWithCreditsBounded(b *backend.BuildBounds, source string, missing []Item, system string, maxBytes int64, claim func(backend.PromptMeasure) error) (string, error) {
	p, e := s.requestPayloadBounded(b, source, missing)
	if e != nil {
		return "", e
	}
	return backend.BuildUserWithCredits(b, system, maxBytes, claim, func(w io.Writer) error { return b.WriteJSON(w, p) })
}

// This iterator avoids building a JSON request or copied text list merely to
// count alignment words. Regions/item fields remain borrowed frozen strings.
func (s *AlignmentState) VisitAlignmentRequestTextsBounded(b *backend.BuildBounds, source string, missing []Item, visit func(string)) error {
	stripped, e := StripRubyTextBounded(b, source)
	if e != nil {
		return e
	}
	visit(stripped)
	visit(s.Translation)
	for _, r := range s.Regions {
		visit(r.Text)
	}
	for _, it := range missing {
		visit(it.SourceBase)
		visit(it.SourceText)
	}
	return nil
}
func (s *AlignmentState) AlignmentRequestTextsBounded(b *backend.BuildBounds, source string, missing []Item) ([]string, error) {
	n := 2 + len(s.Regions) + 2*len(missing)
	if e := b.ReserveNodes(int64(n), 16); e != nil {
		return nil, e
	}
	out := make([]string, 0, n)
	e := s.VisitAlignmentRequestTextsBounded(b, source, missing, func(s string) { out = append(out, s) })
	return out, e
}

func AlignmentBatchRequestBounded(b *backend.BuildBounds, members []AlignmentBatchMember, maxBytes int64) (string, error) {
	return AlignmentBatchRequestWithCreditsBounded(b, members, "", maxBytes, nil)
}
func AlignmentBatchRequestWithCreditsBounded(b *backend.BuildBounds, members []AlignmentBatchMember, system string, maxBytes int64, claim func(backend.PromptMeasure) error) (string, error) {
	if len(members) == 0 {
		return "", errors.New("ruby alignment batch must contain members")
	}
	type memberPayload struct {
		WorkID      string                   `json:"work_id"`
		CandidateID string                   `json:"candidate_id"`
		Source      string                   `json:"source"`
		Translation string                   `json:"translation"`
		Regions     []alignmentRequestRegion `json:"translation_regions"`
		Digest      string                   `json:"region_digest"`
		Missing     []alignmentRequestItem   `json:"missing"`
	}
	if e := b.ReserveNodes(int64(len(members))+1, 1024); e != nil {
		return "", e
	}
	seen := make(map[AlignmentBatchKey]bool, len(members))
	if e := b.ReserveNodes(int64(len(members)), int64(unsafe.Sizeof(memberPayload{}))); e != nil {
		return "", e
	}
	payload := struct {
		Alignments []memberPayload `json:"alignments"`
	}{Alignments: make([]memberPayload, len(members))}
	for i, m := range members {
		key := AlignmentBatchKey{m.WorkID, m.CandidateID}
		if key.WorkID == "" || key.CandidateID == "" || !utf8.ValidString(key.WorkID) || !utf8.ValidString(key.CandidateID) || seen[key] {
			return "", errors.New("ruby alignment batch identities must be nonempty, valid UTF-8 and unique")
		}
		seen[key] = true
		if e := m.Alignment.ValidateBounded(b); e != nil {
			return "", e
		}
		missing, e := m.Alignment.MissingBounded(b)
		if e != nil {
			return "", e
		}
		p, e := m.Alignment.requestPayloadBounded(b, m.Source, missing)
		if e != nil {
			return "", e
		}
		payload.Alignments[i] = memberPayload{key.WorkID, key.CandidateID, p.Source, p.Translation, p.Regions, p.RegionDigest, p.Missing}
	}
	return backend.BuildUserWithCredits(b, system, maxBytes, claim, func(w io.Writer) error { return b.WriteJSON(w, payload) })
}
