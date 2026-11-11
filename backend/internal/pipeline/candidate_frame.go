package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// CandidateFrame contains one owned Q wire payload plus fixed reconciliation
// metadata. Persistence loading ends before the short decode/build permit.
type CandidateFrame struct {
	Payload                                                             []byte
	ID, WorkID                                                          string
	Version, Epoch                                                      int64
	Index, SegmentID, Pool, MainAttempt, LogicalAttempt, NetworkAttempt int
	Next                                                                time.Time
	Reconcile                                                           bool
}
type CandidateFrameStore interface {
	CandidateFrame(context.Context, string, int64) (CandidateFrame, error)
}

func DecodeCandidateFrame(b *backend.BuildBounds, f CandidateFrame) (*Candidate, error) {
	var c Candidate
	if err := b.DecodeJSON(f.Payload, &c); err != nil {
		return nil, err
	}
	for i := range c.Segment.Issues {
		if c.Segment.Issues[i].Disposition == "" {
			c.Segment.Issues[i].Disposition = "pending"
		}
	}
	if c.DTOVersion != CandidateDTOVersion || c.ID != f.ID || c.Version != f.Version || f.SegmentID > 0 && c.Segment.DBID != f.SegmentID {
		return nil, ErrCandidateStale
	}
	if c.Alignment != nil {
		if err := c.Alignment.ValidateBounded(b); err != nil {
			return nil, fmt.Errorf("decode candidate frame %s/version%d state%d: %w", c.ID, c.Version, c.Alignment.Version, err)
		}
	}
	if f.WorkID != "" && c.WorkID != "" && c.WorkID != f.WorkID {
		return nil, ErrCandidateStale
	}
	if f.WorkID != "" {
		c.WorkID = f.WorkID
	}
	c.Index = f.Index
	c.StoredBytes = int64(len(f.Payload))
	if f.Reconcile {
		if c.RetryEpoch != f.Epoch {
			c.RetryEpoch = f.Epoch
			c.PoolIndex = f.Pool
			c.MainAttempt = f.MainAttempt
			c.LogicalAttempt = 0
			c.NetworkAttempt = 0
			c.NextAttemptAt = time.Time{}
			c.LastAlignmentRequestID = ""
		}
		if !c.Ready && f.LogicalAttempt > c.LogicalAttempt {
			c.LogicalAttempt = max(c.LogicalAttempt, f.LogicalAttempt-1)
			c.NetworkAttempt = max(c.NetworkAttempt, f.NetworkAttempt)
		}
		if !f.Next.IsZero() {
			c.NextAttemptAt = f.Next
		}
	}
	return &c, nil
}
func (c *Candidate) SealAndCompact(b *backend.BuildBounds, limit int64) error {
	c.PreparedPayload = nil
	c.PreparedHeader = nil
	payload, err := b.EncodeJSON(c, limit)
	if err != nil {
		return err
	}
	c.StoredBytes = int64(len(payload))
	digest := sha256.New()
	_, _ = io.WriteString(digest, c.BaselineTarget)
	c.BaselineDigest = hex.EncodeToString(digest.Sum(nil))
	h := HeaderForCandidate(c)
	header, err := c.EncodeHeader()
	if err != nil {
		return err
	}
	summary := h.Candidate()
	summary.PreparedPayload = payload
	summary.PreparedHeader = header
	summary.BaselineDigest = hex.EncodeToString(digest.Sum(nil))
	summary.BaselineStatus = c.BaselineStatus
	summary.Segment = Segment{ID: c.Segment.ID, DBID: c.Segment.DBID, ContentVersion: c.Segment.ContentVersion, TargetIsNull: c.Segment.TargetIsNull, Status: c.Segment.Status}
	summary.LastAlignmentRequestID = c.LastAlignmentRequestID
	summary.PendingSave = c.PendingSave
	*c = *summary
	return nil
}
func loadCandidateFrame(ctx context.Context, store RoundStore, id string, limit int64) (CandidateFrame, error) {
	if frames, ok := store.(CandidateFrameStore); ok {
		return frames.CandidateFrame(ctx, id, limit)
	}
	c, err := store.(CandidatePayloadStore).Candidate(ctx, id, limit)
	if err != nil {
		return CandidateFrame{}, err
	}
	payload, err := c.EncodeBounded(limit, 0)
	if err != nil {
		return CandidateFrame{}, err
	}
	return CandidateFrame{Payload: payload, ID: c.ID, WorkID: c.WorkID, Version: c.Version, Index: c.Index, SegmentID: c.Segment.DBID}, nil
}

type CandidateCommitPreparer interface {
	PrepareCandidateCommit(context.Context, *backend.BuildBounds, *Candidate, TranslatedSegment) (TranslatedSegment, error)
}

func compactCommitResult(b *backend.BuildBounds, r TranslatedSegment, limit int64, claim ...func(int64) error) (TranslatedSegment, error) {
	for i := range r.Issues {
		if r.Issues[i].Disposition == "" {
			r.Issues[i].Disposition = qa.DispositionPending
		}
	}
	if len(claim) > 0 {
		size, err := b.MeasureJSON(r)
		if err != nil {
			return TranslatedSegment{}, err
		}
		if err = claim[0](size); err != nil {
			return TranslatedSegment{}, err
		}
	}
	payload, err := b.EncodeJSON(r, limit)
	if err != nil {
		return TranslatedSegment{}, err
	}
	root, err := backend.NewJSONView(b, payload)
	if err != nil {
		return TranslatedSegment{}, err
	}
	var issues []byte
	err = root.EachField(func(k, v backend.JSONValue) error {
		if k.StringEquals("Issues") {
			issues = v.Raw()
		}
		return nil
	})
	if err != nil {
		return TranslatedSegment{}, err
	}
	r.PreparedPayload = payload
	r.IssuesPayload = issues
	r.Meta = nil
	r.Protected = nil
	r.Issues = nil
	return r, nil
}
func restoreCommitResult(b *backend.BuildBounds, r TranslatedSegment) (TranslatedSegment, error) {
	if r.PreparedPayload == nil {
		return r, nil
	}
	var full TranslatedSegment
	if err := b.DecodeJSON(r.PreparedPayload, &full); err != nil {
		return TranslatedSegment{}, err
	}
	for i := range full.Issues {
		if full.Issues[i].Disposition == "" {
			full.Issues[i].Disposition = qa.DispositionPending
		}
	}
	return full, nil
}
