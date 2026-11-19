package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// memberFixturePayload always supplies the actual encoded backing receipt.
// The store consumes that receipt; cleanup only retires unconsumed temporaries.
func memberFixturePayload(t *testing.T, ctx context.Context, value any) ([]byte, *backend.OwnedAllocation) {
	t.Helper()
	b, err := backend.NewBuildBoundsFromContext(ctx, 1<<20, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	payload, err := b.EncodeJSON(value, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := b.RetainedAllocation(int64(cap(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if err = allocation.DetachBuild(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(allocation.Release)
	return payload, allocation
}

func memberFixtureReserve(t *testing.T, ctx context.Context, s *MemoryRoundStore, id string, businessLimit int64, frozen []frozenMainMember) MainResponseHeader {
	t.Helper()
	in := RequestIntent{ID: id, Stage: backend.RequestStageMain, InputDigest: "input:" + id, FrozenContext: FrozenContext{Mode: RoundModeTranslate, Format: "txt", TargetLang: "zh"}, BusinessBound: businessLimit}
	for ordinal, m := range frozen {
		payload, allocation := memberFixturePayload(t, ctx, m)
		in.InputMembers = append(in.InputMembers, InputMemberFrame{Key: MemberKey{RequestID: id, Ordinal: ordinal}, ResponseMember: ResponseMember{Index: m.Index, CandidateID: m.CandidateID}, PromptID: m.PromptID, Payload: payload, Bytes: int64(len(payload)), Digest: responseDigest(payload), Allocation: allocation})
		in.Indices = append(in.Indices, m.Index)
	}
	if err := s.Reserve(ctx, in); err != nil {
		t.Fatal(err)
	}
	return s.requests[id].response.MainResponseHeader
}

func memberFixtureOutputs(t *testing.T, ctx context.Context, s *MemoryRoundStore, header MainResponseHeader, selected []archivedOutput, entries []glossary.Entry) MainResponseHeader {
	t.Helper()
	if len(selected) != header.MemberCount || len(selected) > 16 {
		t.Fatal("fixture output count")
	}
	// Capture is an independent reliable fact, even when the fixture directly
	// supplies the subsequent normalized member publication.
	raw, rawAllocation := memberFixturePayload(t, ctx, map[string]string{"capture": "fixture"})
	if _, err := s.CaptureResponse(ctx, CapturedResponse{MainResponseHeader: header, Payload: raw, Allocation: rawAllocation}); err != nil {
		t.Fatal(err)
	}
	publication := OutputPublication{Key: archiveMemberKey(header, 0), Next: len(selected), Complete: true}
	for ordinal, out := range selected {
		out.Outcome = MemberOutcome{Key: archiveMemberKey(header, ordinal), Accepted: out.Failure == "" && !out.Missing}
		if !out.Outcome.Accepted {
			out.Outcome.Reason = responsecontract.MemberInvalid
			if out.Missing {
				out.Outcome.Reason = responsecontract.MemberMissing
			}
		}
		payload, allocation := memberFixturePayload(t, ctx, out)
		digest := responseDigest(payload)
		publication.Digest = archiveDigest(publication.Digest, digest)
		publication.Members = append(publication.Members, OutputMemberFrame{Key: archiveMemberKey(header, ordinal), Payload: payload, Bytes: int64(len(payload)), Digest: digest, Allocation: allocation})
	}
	if entries != nil {
		payload, allocation := memberFixturePayload(t, ctx, entries)
		publication.Glossary = payload
		publication.PublicAllocation = allocation
	}
	if _, err := s.PublishOutputs(ctx, publication); err != nil {
		t.Fatal(err)
	}
	return s.requests[header.ID].response.MainResponseHeader
}

func memberFixtureCorrection(t *testing.T, ctx context.Context, s *MemoryRoundStore, header MainResponseHeader, ordinal int, target string) OutputMemberFrame {
	t.Helper()
	payload, allocation := memberFixturePayload(t, ctx, archivedOutput{Target: target, Outcome: MemberOutcome{Key: archiveMemberKey(header, ordinal), Accepted: true}})
	frame := OutputMemberFrame{Key: archiveMemberKey(header, ordinal), Payload: payload, Bytes: int64(len(payload)), Digest: responseDigest(payload), Allocation: allocation}
	if err := s.StageCorrectedMember(ctx, frame); err != nil {
		t.Fatal(err)
	}
	return frame
}

func memberFixtureDecode(t *testing.T, frame OutputMemberFrame) archivedOutput {
	t.Helper()
	b, err := backend.NewBuildBounds(1<<20, frame.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	var selected archivedOutput
	if err = b.DecodeJSON(frame.Payload, &selected); err != nil {
		t.Fatal(err)
	}
	return selected
}

func memberFixtureInputs(n int) []frozenMainMember {
	members := make([]frozenMainMember, n)
	for ordinal := range members {
		members[ordinal] = frozenMainMember{Index: ordinal, PromptID: fmt.Sprint(ordinal + 1), CandidateID: fmt.Sprintf("member-%d", ordinal), Segment: Segment{Source: "source", Status: "pending", TargetIsNull: true}}
	}
	return members
}
