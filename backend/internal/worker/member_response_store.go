package worker

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func (s *roundStore) CaptureEpoch() int64 { return s.scope.RetryEpoch }
func (s *roundStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) error {
	if install != nil {
		return permanentStoreError{workstate.ErrManifest}
	}
	return s.classify(s.store.PublishMemberBusiness(ctx, id, out.Receipt))
}
func (s *roundStore) AbortMemberBusiness(ctx context.Context, id string) error {
	return s.store.AbortMemberBusiness(ctx, id)
}
func (s *roundStore) LoadResponsePublic(ctx context.Context, id string, limit int64) (pipeline.OutputMemberFrame, error) {
	out, err := s.store.LoadResponsePublic(ctx, id, limit)
	if err != nil {
		return pipeline.OutputMemberFrame{}, s.classify(err)
	}
	return pipeline.OutputMemberFrame{Payload: out.Payload, Bytes: out.Bytes, Release: out.Release}, nil
}
func (s *roundStore) StageCorrectedMember(ctx context.Context, in pipeline.OutputMemberFrame) error {
	return s.classify(s.store.StageCorrectedMember(ctx, workstate.OutputMember{Key: storageMemberKey(in.Key), Payload: in.Payload, Bytes: in.Bytes, Digest: in.Digest}))
}

func storageMemberKey(k pipeline.MemberKey) workstate.MemberKey {
	return workstate.MemberKey{RequestID: k.RequestID, Ordinal: k.Ordinal, CaptureEpoch: k.CaptureEpoch}
}
func (s *roundStore) LoadInputMember(ctx context.Context, k pipeline.MemberKey, limit int64) (pipeline.InputMemberFrame, error) {
	m, err := s.store.LoadInputMember(ctx, storageMemberKey(k), limit)
	if err != nil {
		return pipeline.InputMemberFrame{}, s.classify(err)
	}
	idx, ok := s.segmentIndex(m.SegmentID)
	if !ok {
		m.Release()
		return pipeline.InputMemberFrame{}, permanentStoreError{workstate.ErrManifest}
	}
	return pipeline.InputMemberFrame{Key: k, ResponseMember: pipeline.ResponseMember{Index: idx, SegmentID: m.SegmentID, WorkID: m.WorkID, CandidateID: m.CandidateID, BaselineVersion: m.BaselineVersion, BaselineTarget: m.BaselineTarget, BaselineStatus: m.BaselineStatus, SourceGeneration: m.SourceGeneration, SourceRevisionID: m.SourceRevisionID}, PromptID: m.PromptID, Payload: m.Payload, Bytes: m.Bytes, Digest: m.Digest, Release: m.Release}, nil
}
func (s *roundStore) LoadOutputMember(ctx context.Context, k pipeline.MemberKey, final bool, limit int64) (pipeline.OutputMemberFrame, error) {
	m, err := s.store.LoadOutputMember(ctx, storageMemberKey(k), final, limit)
	if err != nil {
		return pipeline.OutputMemberFrame{}, s.classify(err)
	}
	return pipeline.OutputMemberFrame{StoredBytes: m.StoredBytes, Key: k, Payload: m.Payload, Bytes: m.Bytes, Digest: m.Digest, Release: m.Release}, nil
}
func (s *roundStore) CaptureResponse(ctx context.Context, in pipeline.CapturedResponse) error {
	return s.classify(s.store.CaptureResponse(ctx, workstate.CapturedResponse{ResponseHeader: workstate.ResponseHeader{RequestID: in.ID, Scope: s.scope, Truncated: in.Truncated}, Payload: in.Payload, Result: workstate.RequestResult{UsageKnown: in.Record.UsageKnown, InputTokens: in.Record.Usage.PromptTokens, OutputTokens: in.Record.Usage.CompletionTokens, DurationMS: in.Record.Duration.Milliseconds(), LastError: in.Record.Error}}))
}
func (s *roundStore) LoadCapture(ctx context.Context, id string, limit int64) (pipeline.CapturedResponse, error) {
	m, err := s.store.LoadCapture(ctx, id, limit)
	if err != nil {
		return pipeline.CapturedResponse{}, s.classify(err)
	}
	return pipeline.CapturedResponse{MainResponseHeader: pipelineResponseHeader(m.ResponseHeader), Payload: m.Payload, Release: m.Release}, nil
}
func (s *roundStore) PublishOutputs(ctx context.Context, in pipeline.OutputPublication) error {
	out := workstate.OutputPublication{Key: storageMemberKey(in.Key), Next: in.Next, Digest: in.Digest, Glossary: in.Glossary, Complete: in.Complete}
	for _, m := range in.Members {
		out.Members = append(out.Members, workstate.OutputMember{Key: storageMemberKey(m.Key), Payload: m.Payload, Bytes: m.Bytes, Digest: m.Digest})
	}
	return s.classify(s.store.PublishOutputs(ctx, out))
}
