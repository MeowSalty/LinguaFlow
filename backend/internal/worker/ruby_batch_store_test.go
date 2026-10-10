package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func seedRubyBatchCandidates(t *testing.T, f *rubyPipelineFixture) (*roundStore, []*pipeline.Candidate) {
	t.Helper()
	ctx := context.Background()
	doc, store := f.round(t, 0)
	if _, err := store.Seal(ctx, []int{0, 1}); err != nil {
		t.Fatal(err)
	}
	var candidates []*pipeline.Candidate
	for index, seg := range doc.Segments {
		c := &pipeline.Candidate{DTOVersion: pipeline.CandidateDTOVersion, ID: fmt.Sprintf("member-%d", index), Version: 1, Index: index, Mode: pipeline.RoundModeTranslate, Format: doc.Format, Segment: seg, BaselineTarget: seg.Target, BaselineStatus: seg.Status}
		if err := store.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, c)
	}
	return store, candidates
}

func rubyBatchIntent(candidates []*pipeline.Candidate, id string, backendID int) pipeline.RequestIntent {
	in := pipeline.RequestIntent{ID: id, Stage: backend.RequestStageAlignment, BackendID: backendID, Phase: "alignment", InputDigest: id}
	for _, c := range candidates {
		in.Indices = append(in.Indices, c.Index)
		in.Members = append(in.Members, pipeline.RequestMember{Index: c.Index, WorkID: c.WorkID, CandidateID: c.ID, CandidateVersion: c.Version, Pool: c.PoolIndex, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt})
	}
	return in
}

func TestRubyBatchStoreRecoversPartialResponseAndPreservesIsolationOnRetry(t *testing.T) {
	ctx := context.Background()
	f := newRubyPipelineFixture(t, 2)
	store, candidates := seedRubyBatchCandidates(t, f)
	request := rubyBatchIntent(candidates, "first-batch", f.snapshot.RubyRetry.Backend.ID)
	if err := store.Reserve(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(ctx, request.ID, pipeline.RequestRecord{State: "received"}); err != nil {
		t.Fatal(err)
	}
	first := candidates[0]
	first.Version++
	first.LogicalAttempt++
	first.LastAlignmentRequestID = request.ID
	first.ForceSingleAlignment = true
	if err := store.Save(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := workstate.MarkUnknown(ctx, f.client, f.jobID); err != nil {
		t.Fatal(err)
	}
	_, recoveredStore := f.round(t, 0)
	recovered, _, err := recoveredStore.Candidates(ctx, 0, 10)
	if err != nil || len(recovered) != 2 {
		t.Fatalf("recover members=%d: %v", len(recovered), err)
	}
	if recovered[0].Version != 2 || !recovered[0].ForceSingleAlignment || recovered[0].LastAlignmentRequestID != request.ID || recovered[0].LogicalAttempt != 1 || recovered[0].NetworkAttempt != 0 {
		t.Fatalf("saved member lost progress or isolation: %+v", recovered[0])
	}
	if recovered[1].Version != 1 || recovered[1].ForceSingleAlignment || recovered[1].LogicalAttempt != 0 || recovered[1].NetworkAttempt != 1 {
		t.Fatalf("unknown member lost its debit: %+v", recovered[1])
	}
	if _, err := f.jobs.CancelJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	_, retryStore := f.round(t, 0)
	retried, _, err := retryStore.Candidates(ctx, 0, 10)
	if err != nil || len(retried) != 2 {
		t.Fatalf("retry load: %v", err)
	}
	for i, c := range retried {
		if c.RetryEpoch != 1 || c.LogicalAttempt != 0 || c.NetworkAttempt != 0 || c.WorkID != candidates[i].WorkID || c.LastAlignmentRequestID != "" {
			t.Fatalf("retry scope did not rebase stable member: %+v", c)
		}
	}
	if !retried[0].ForceSingleAlignment {
		t.Fatal("explicit retry removed permanent per-candidate isolation")
	}
	// The second member may remain in a configured batch; an isolated member
	// uses this same member-aware contract for a one-member request.
	request = rubyBatchIntent(retried[:1], "retry-single", f.snapshot.RubyRetry.Backend.ID)
	if err := retryStore.Reserve(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := retryStore.Record(ctx, request.ID, pipeline.RequestRecord{State: "received"}); err != nil {
		t.Fatal(err)
	}
	f.client.Job.UpdateOneID(f.jobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	c := retried[0]
	c.Version++
	c.LogicalAttempt++
	c.LastAlignmentRequestID = request.ID
	if err := retryStore.Save(ctx, c); err != nil {
		t.Fatalf("member-aware save during retry pause: %v", err)
	}
	if err := retryStore.Save(ctx, c); err != nil {
		t.Fatalf("member-aware pause save replay: %v", err)
	}
	if got := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).CountX(ctx); got != 2 {
		t.Fatalf("store handoff created %d requests", got)
	}
}

func TestRubyBatchStoreLoadsAndUpgradesLegacyCandidateDTO(t *testing.T) {
	ctx := context.Background()
	f := newRubyPipelineFixture(t, 2)
	store, candidates := seedRubyBatchCandidates(t, f)
	c := candidates[0]
	// Model a pre-P2 persisted DTO without new identity or fallback fields.
	c.DTOVersion = 1
	c.WorkID = ""
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	f.client.WorkCandidate.Update().Where(workcandidate.IdentityEQ(c.ID)).SetDtoVersion(1).SetPayload(data).SetPayloadBytes(int64(len(data))).ExecX(ctx)
	loaded, _, err := store.Candidates(ctx, 0, 1)
	if err != nil || len(loaded) != 1 {
		t.Fatalf("legacy load: %v", err)
	}
	old := loaded[0]
	if old.WorkID != store.WorkIdentity(0) || old.ForceSingleAlignment || old.DTOVersion != 1 {
		t.Fatalf("legacy defaults: %+v", old)
	}
	old.Version++
	old.DTOVersion = pipeline.CandidateDTOVersion
	if err := store.Save(ctx, old); err != nil {
		t.Fatalf("upgrade old candidate on next save: %v", err)
	}
	loaded, _, err = store.Candidates(ctx, 0, 1)
	if err != nil || loaded[0].DTOVersion != 2 || loaded[0].Version != 2 {
		t.Fatalf("upgraded reload: %+v, %v", loaded, err)
	}
}
