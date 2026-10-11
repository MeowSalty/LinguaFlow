package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

const CandidateDTOVersion = 2

// Candidate owns its entire payload. It is never an alias of a live Document.
// Its baseline is captured before the main request, and cannot be refreshed on retry.
type Candidate struct {
	DTOVersion             int                  `json:"dto_version"`
	ID                     string               `json:"id"`
	WorkID                 string               `json:"work_id,omitempty"`
	Version                int64                `json:"version"`
	Index                  int                  `json:"index"`
	Mode                   string               `json:"mode"`
	Format                 string               `json:"format"`
	Segment                Segment              `json:"segment"`
	BaselineTarget         string               `json:"baseline_target"`
	BaselineStatus         string               `json:"baseline_status"`
	Alignment              *ruby.AlignmentState `json:"alignment,omitempty"`
	PoolIndex              int                  `json:"pool_index"`
	MainAttempt            int                  `json:"main_attempt"`
	LogicalAttempt         int                  `json:"logical_attempt"`
	NetworkAttempt         int                  `json:"network_attempt"`
	RetryEpoch             int64                `json:"retry_epoch"`
	StoredBytes            int64                `json:"-"`
	NextAttemptAt          time.Time            `json:"next_attempt_at,omitempty"`
	Ready                  bool                 `json:"ready"`
	ParentRequestID        string               `json:"parent_request_id"`
	LastAlignmentRequestID string               `json:"last_alignment_request_id,omitempty"`
	ForceSingleAlignment   bool                 `json:"force_single_alignment,omitempty"`
}

func NewWorkID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

func newCandidate(seg Segment, index int, mode, format string) *Candidate {
	return &Candidate{DTOVersion: CandidateDTOVersion, ID: NewWorkID(), WorkID: fmt.Sprintf("%s:%d", mode, index), Version: 1,
		Index: index, Mode: mode, Format: format, Segment: cloneSegment(seg),
		BaselineTarget: seg.Target, BaselineStatus: seg.Status}
}

// cloneValue preserves concrete types in parser metadata (notably []ruby.Item).
// Metadata is an acyclic value tree produced by the document parsers.
func cloneValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneValue(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneValue(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneValue(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if out.Field(i).CanSet() && v.Field(i).CanInterface() {
				out.Field(i).Set(cloneValue(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

func cloneSegment(seg Segment) Segment { return cloneValue(reflect.ValueOf(seg)).Interface().(Segment) }

func (c *Candidate) Encode() ([]byte, error) { return json.Marshal(c) }
func DecodeCandidate(data []byte) (*Candidate, error) {
	var c Candidate
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decode candidate: %w", err)
	}
	if (c.DTOVersion != 1 && c.DTOVersion != CandidateDTOVersion) || c.ID == "" || c.Version < 1 {
		return nil, fmt.Errorf("unsupported or invalid candidate DTO")
	}
	if c.Alignment != nil {
		if err := c.Alignment.Validate(); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// PrepareBatch calls the existing main-response/repair path with all output kept private.
func (h *TranslateHandler) PrepareBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	copy := *h
	copy.prepareOnly = true
	return copy.ProcessBatch(ctx, doc, idxs, attempt, logger)
}
func (h *ReviseHandler) PrepareBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	copy := *h
	copy.prepareOnly = true
	return copy.ProcessBatch(ctx, doc, idxs, attempt, logger)
}

func (h *TranslateHandler) prepareTranslatedCandidates(doc *Document, idxs []int, wantIDs []string, trans map[string]string, outputs map[string][]ruby.OutputEntry, contextSet map[int]struct{}, logger *slog.Logger) batchResult {
	var result batchResult
	n := 0
	for _, idx := range idxs {
		if IsContext(contextSet, idx) {
			continue
		}
		id := wantIDs[n]
		n++
		text, ok := trans[id]
		if !ok || strings.TrimSpace(text) == "" {
			result.unresolved = append(result.unresolved, idx)
			continue
		}
		c := newCandidate(doc.Segments[idx], idx, RoundModeTranslate, doc.Format)
		seg := &c.Segment
		if h.Repair.PlaceholderNormalize {
			text, _ = repair.NormalizePlaceholders(text, seg.Protected)
		}
		seg.Target = text
		seg.Issues = nil
		missing, duplicated, invented := protect.PlaceholderViolations(seg)
		if len(missing)+len(duplicated)+len(invented) > 0 {
			result.unresolved = append(result.unresolved, idx)
			continue
		}
		if h.Postprocess != nil && h.Postprocess.TrimSpaces {
			seg.Target = strings.TrimSpace(seg.Target)
		}
		if h.Protector != nil {
			if err := h.Protector.Unprotect(seg); err != nil {
				result.unresolved = append(result.unresolved, idx)
				continue
			}
		}
		if h.RubyEnabled {
			// Legacy markers can preserve the main text but cannot establish item
			// identity. Only explicit source mappings below can verify annotations.
			if max(1, h.RubyProtocolVersion) == ruby.ProtocolLegacy {
				seg.Target = ruby.StripLegacyInlineMarkers(seg.Target)
			}
			if strings.Contains(seg.Target, "⟦ruby:") {
				result.unresolved = append(result.unresolved, idx)
				continue
			}
			items := ruby.ClassifyInitialItems(extractRubyItemsFromSeg(seg), outputs[id])
			state, err := PrepareAlignment(seg.Target, doc.Format, items, kindSet(h.RubyPreserveKinds), max(1, h.RubyProtocolVersion), outputs[id])
			if err != nil {
				result.err = err
				return result
			}
			c.Alignment = state
		}
		c.Ready = c.Alignment == nil || len(c.Alignment.Missing()) == 0 || h.RubyRetryAttempts <= 0 || len(h.RubyRetryBackends) == 0
		result.candidates = append(result.candidates, c)
	}
	return result
}

func (h *ReviseHandler) prepareRevisionCandidate(seg *Segment, idx int, revision prompt.ReviseRevision, st *reviseProtectState, outputs map[string][]ruby.OutputEntry, format string) (*Candidate, error) {
	c := newCandidate(*seg, idx, RoundModeRevise, format)
	text := revision.Target
	if h.Repair.PlaceholderNormalize {
		text, _ = repair.NormalizePlaceholders(text, st.mapping)
	}
	c.Segment.Target = text
	c.Segment.Protected = st.mapping
	missing, duplicated, invented := protect.PlaceholderViolations(&c.Segment)
	if len(missing)+len(duplicated)+len(invented) > 0 || strings.Contains(text, "⟦ruby:") {
		return nil, nil
	}
	c.Segment.Target = protect.RestoreText(text, st.mapping)
	if h.RubyEnabled && len(st.rubyItems) > 0 {
		state, err := PrepareAlignment(c.Segment.Target, format, st.rubyItems, nil, max(1, h.RubyProtocolVersion), outputs[revision.ID])
		if err != nil {
			return nil, err
		}
		c.Alignment = state
	}
	c.Ready = c.Alignment == nil || len(c.Alignment.Missing()) == 0 || h.RubyRetryAttempts <= 0 || len(h.RubyRetryBackends) == 0
	return c, nil
}

// PrepareAlignment freezes body regions and retention before validating optional
// main-response mappings. It performs no requests and never mutates a Document.
func PrepareAlignment(body, format string, items []ruby.Item, preserve map[string]bool, protocol int, initial []ruby.OutputEntry) (*ruby.AlignmentState, error) {
	state, err := ruby.NewAlignmentState(body, format, items, preserve, protocol)
	if err != nil {
		return nil, err
	}
	state.ApplyOutput(itemIDs(state.Missing()), initial, false)
	return state, nil
}

func itemIDs(items []ruby.Item) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

// FinalizeCandidate is deterministic and does not change the baseline or write a Document.
func FinalizeCandidate(c *Candidate) (TranslatedSegment, bool, error) {
	seg := cloneSegment(c.Segment)
	if c.Alignment != nil {
		text, restored, err := c.Alignment.Render()
		if err != nil {
			return TranslatedSegment{}, false, err
		}
		seg.Target = text
		if restored.Matched < restored.Total {
			if c.Mode == RoundModeRevise {
				return TranslatedSegment{}, false, nil
			}
			seg.Issues = append(seg.Issues, qa.QualityIssue{SegmentIndex: c.Index, Severity: qa.SeverityWarning, Code: qa.CodeRubyRestoreIncomplete,
				Message: fmt.Sprintf("注音还原不完整：应还原 %d 条，实际 %d 条", restored.Total, restored.Matched)})
		}
	}
	baseline := rawSource(&seg)
	if c.Mode == RoundModeRevise {
		baseline = c.BaselineTarget
	}
	if markup.RequiresWellFormedTargets(c.Format) {
		if err := markup.TargetRegression(baseline, seg.Target); err != nil {
			return TranslatedSegment{}, false, nil
		}
	}
	return TranslatedSegment{Index: c.Index, ID: seg.ID, SourceText: rawSource(&seg), TargetText: seg.Target, Meta: seg.Meta, Issues: seg.Issues, Protected: seg.Protected}, true, nil
}
