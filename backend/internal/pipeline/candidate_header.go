package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

const CandidateHeaderLimit int64 = 2048

type CandidateHeader struct {
	SegmentID, BaselineStatus, BaselineDigest                                            string
	SegmentDBID                                                                          int
	BaselineVersion                                                                      int64
	BaselineNull                                                                         bool
	LastAlignmentRequestID                                                               string
	ID, WorkID, ParentRequestID, Mode, Format                                            string
	Version, RetryEpoch, Bytes                                                           int64
	Index, Pool, MainAttempt, LogicalAttempt, NetworkAttempt, Words, Protocol, Validator int
	Next                                                                                 time.Time
	Ready, ForceSingle, Missing                                                          bool
}

func HeaderForCandidate(c *Candidate) CandidateHeader {
	h := CandidateHeader{ID: c.ID, WorkID: c.WorkID, ParentRequestID: c.ParentRequestID, Mode: c.Mode, Format: c.Format, Version: c.Version, RetryEpoch: c.RetryEpoch, Bytes: c.StoredBytes, Index: c.Index, Pool: c.PoolIndex, MainAttempt: c.MainAttempt, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt, Words: alignmentWords(c), Next: c.NextAttemptAt, Ready: c.Ready, ForceSingle: c.ForceSingleAlignment}
	h.SegmentID = c.Segment.ID
	h.SegmentDBID = c.Segment.DBID
	h.BaselineVersion = c.Segment.ContentVersion
	h.BaselineNull = c.Segment.TargetIsNull
	h.BaselineStatus = c.BaselineStatus
	h.BaselineDigest = c.BaselineDigest
	h.LastAlignmentRequestID = c.LastAlignmentRequestID
	if c.Alignment != nil {
		h.Protocol = c.Alignment.ProtocolVersion
		h.Validator = c.Alignment.ValidatorVersion
		h.Missing = len(c.Alignment.Missing()) > 0
	}
	if c.HeaderOnly {
		h.Missing = c.HeaderMissing
	}
	return h
}
func (h CandidateHeader) Candidate() *Candidate {
	c := &Candidate{DTOVersion: 2, ID: h.ID, WorkID: h.WorkID, ParentRequestID: h.ParentRequestID, Mode: h.Mode, Format: h.Format, Version: h.Version, RetryEpoch: h.RetryEpoch, StoredBytes: h.Bytes, Index: h.Index, PoolIndex: h.Pool, MainAttempt: h.MainAttempt, LogicalAttempt: h.LogicalAttempt, NetworkAttempt: h.NetworkAttempt, NextAttemptAt: h.Next, Ready: h.Ready, ForceSingleAlignment: h.ForceSingle, HeaderOnly: true, HeaderWords: h.Words, HeaderMissing: h.Missing}
	c.Segment = Segment{ID: h.SegmentID, DBID: h.SegmentDBID, ContentVersion: h.BaselineVersion, TargetIsNull: h.BaselineNull}
	c.BaselineStatus = h.BaselineStatus
	c.BaselineDigest = h.BaselineDigest
	c.LastAlignmentRequestID = h.LastAlignmentRequestID
	if h.Protocol > 0 {
		c.Alignment = &ruby.AlignmentState{ProtocolVersion: h.Protocol, ValidatorVersion: h.Validator}
	}
	return c
}
func (c *Candidate) EncodeHeader() ([]byte, error) {
	if c.PreparedHeader != nil {
		return c.PreparedHeader, nil
	}
	b, err := backend.NewBuildBounds(CandidateHeaderLimit, 0)
	if err != nil {
		return nil, err
	}
	return b.EncodeJSON(HeaderForCandidate(c), CandidateHeaderLimit)
}
func DecodeCandidateHeader(data []byte) (*Candidate, error) {
	if len(data) == 0 || int64(len(data)) > CandidateHeaderLimit {
		return nil, fmt.Errorf("invalid candidate scheduling header")
	}
	b, err := backend.NewBuildBounds(CandidateHeaderLimit, 0)
	if err != nil {
		return nil, err
	}
	var h CandidateHeader
	if err := b.DecodeJSON(data, &h); err != nil {
		return nil, fmt.Errorf("decode candidate scheduling header: %w", err)
	}
	if h.ID == "" || h.Version < 1 || h.Bytes < 0 {
		return nil, fmt.Errorf("invalid candidate scheduling identity")
	}
	return h.Candidate(), nil
}

type CandidatePayloadStore interface {
	CandidateHeaders(context.Context, int, int) ([]*Candidate, int, error)
	Candidate(context.Context, string, int64) (*Candidate, error)
}
