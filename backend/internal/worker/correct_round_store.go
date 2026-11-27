package worker

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func (s *roundStore) CommitCorrect(ctx context.Context, identity string, baseline pipeline.Segment, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, int64, error) {
	segmentID, ok := s.segmentID(result.Index)
	if !ok || segmentID != baseline.DBID {
		return pipeline.CommitFatal, 0, s.classify(workstate.ErrManifest)
	}
	noop := baseline.Target == result.TargetText
	version := baseline.ContentVersion
	if !noop {
		version++
	}
	var target *string
	if !baseline.TargetIsNull {
		target = &baseline.Target
	}
	commit, err := s.store.Commit(ctx, workstate.CommitInput{
		Scope: s.scope, SegmentID: segmentID, CommitID: identity,
		BaselineVersion: baseline.ContentVersion, BaselineTarget: target,
		BaselineStatus: baseline.Status, BaselineTargetIsNull: baseline.TargetIsNull,
		Target: result.TargetText, Issues: result.Issues, Noop: noop,
		RequireProjectionProof: true, ConfirmedVersion: version, DeferIfPaused: true,
	})
	return pipeline.CommitOutcome(commit.Outcome), commit.ContentVersion, s.classifyCorrectPersistence(err)
}

func (s *roundStore) SealCorrectCanonical(ctx context.Context, doc *pipeline.Document) error {
	return s.classifyCorrectPersistence(s.sealCanonical(ctx, doc, true))
}

func (s *roundStore) classifyCorrectPersistence(err error) error {
	var uncertain interface{ Uncertain() bool }
	if errors.Is(err, workstate.ErrPaused) && !(errors.As(err, &uncertain) && uncertain.Uncertain()) {
		return permanentStoreError{backend.ErrDispatchPaused}
	}
	return s.classify(err)
}
