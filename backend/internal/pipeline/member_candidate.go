package pipeline

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func prepareArchivedCandidate(b *backend.BuildBounds, h stagedHandler, header MainResponseHeader, m frozenMainMember, out archivedOutput, logger *slog.Logger) (*Candidate, error) {
	if out.Failure != "" {
		return nil, fmt.Errorf("response member: %s", out.Failure)
	}
	var c *Candidate
	outputs := map[string][]ruby.OutputEntry{m.PromptID: out.RubyOutput}
	switch handler := h.(type) {
	case *TranslateHandler:
		if len(m.RubyItems) > 0 {
			if m.Segment.Meta == nil {
				m.Segment.Meta = make(map[string]any)
			}
			m.Segment.Meta["ruby_items"] = m.RubyItems
		}
		if out.Missing || strings.TrimSpace(out.Target) == "" {
			return nil, fmt.Errorf("missing response member")
		}
		copy := *handler
		copy.buildBounds = b
		copy.Repair.Bounds = b
		result := copy.prepareTranslatedCandidates(&Document{Format: header.Format, Segments: []Segment{m.Segment}}, []int{0}, []string{m.PromptID}, map[string]string{m.PromptID: out.Target}, outputs, nil, logger, m.CandidateID)
		if result.err != nil {
			return nil, result.err
		}
		if len(result.candidates) == 0 {
			return nil, fmt.Errorf("invalid response member")
		}
		c = result.candidates[0]
	case *ReviseHandler:
		if out.Revision == nil {
			return nil, fmt.Errorf("missing or invalid revision member")
		}
		copy := *handler
		copy.buildBounds = b
		copy.Repair.Bounds = b
		var err error
		c, err = copy.prepareRevisionCandidate(&m.Segment, m.Index, *out.Revision, &reviseProtectState{mapping: m.Mapping, rubyItems: m.RubyItems}, outputs, header.Format, m.CandidateID)
		if err != nil {
			return nil, err
		}
	}
	if c == nil {
		return nil, fmt.Errorf("invalid archived candidate")
	}
	c.Index, c.ID = m.Index, m.CandidateID
	if err := b.ReserveAllocation(int64(len(header.ID)), 1); err != nil {
		return nil, err
	}
	c.ParentRequestID = strings.Clone(header.ID)
	c.PoolIndex, c.MainAttempt = header.Pool, header.MainAttempt
	return c, nil
}
