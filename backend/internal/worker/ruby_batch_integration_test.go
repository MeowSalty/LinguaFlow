package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func newRubyBatchFixture(t *testing.T, batchSize int) *rubyPipelineFixture {
	t.Helper()
	f := newRubyPipelineFixture(t, 4)
	f.snapshot.Rounds[0].Translate.BatchSize = 4
	f.snapshot.RubyRetry.BatchSize = &batchSize
	words, waitMS := 0, 100
	f.snapshot.RubyRetry.MaxWordsPerBatch = &words
	f.snapshot.RubyRetry.BatchWaitMS = &waitMS
	f.persistSnapshot(t)
	return f
}

func alignmentHTTPRequests(f *rubyPipelineFixture) []rubyPipelineRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []rubyPipelineRequest
	for _, request := range f.requests {
		if request.Segments == nil {
			result = append(result, request)
		}
	}
	return result
}

func TestRubyBatchHTTPFourMembersUseOneInvocationAndUsage(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 4 || len(result.Unresolved) != 0 {
		t.Fatalf("batch result=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 2)
	requests := alignmentHTTPRequests(f)
	if len(requests) != 1 || len(requests[0].Alignments) != 4 {
		t.Fatalf("alignment requests=%+v", requests)
	}
	ctx := context.Background()
	row := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.StageIn("ruby_alignment", "alignment")).OnlyX(ctx)
	var envelope struct {
		Version int               `json:"version"`
		Members []json.RawMessage `json:"members"`
	}
	if err := json.Unmarshal(row.Members, &envelope); err != nil || envelope.Version != 1 || len(envelope.Members) != 4 || row.CandidateID != "" {
		t.Fatalf("batch ledger=%s, %v", row.Members, err)
	}
	usage := f.client.UsageRecord.GetX(ctx, *row.UsageRecordID)
	if usage.APICalls != 1 || usage.SegmentCount != 4 {
		t.Fatalf("batch usage=%+v", usage)
	}
	for _, w := range f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).AllX(ctx) {
		if w.MainAttempts != 1 || w.AlignmentAttempts != 1 || w.AlignmentNetworkAttempts != 0 {
			t.Fatalf("batch member cursor=%+v", w)
		}
	}
}

func TestRubyBatchHTTPPartialProgressKeepsConfiguredBatch(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	var mu sync.Mutex
	batchCalls := 0
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Alignments) > 0 {
			mu.Lock()
			batchCalls++
			first := batchCalls == 1
			mu.Unlock()
			if first {
				request.Alignments = append([]rubyPipelineAlignment(nil), request.Alignments...)
				for i := range request.Alignments {
					request.Alignments[i].Missing = request.Alignments[i].Missing[:1]
				}
			}
		}
		return rubyPipelineResponse(request), nil
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 4 {
		t.Fatalf("partial batch=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 3)
	requests := alignmentHTTPRequests(f)
	if len(requests) != 2 || len(requests[0].Alignments) != 4 || len(requests[1].Alignments) != 4 {
		t.Fatalf("valid partial response reduced configured batch: %+v", requests)
	}
	for _, member := range requests[1].Alignments {
		if len(member.Missing) != 1 || member.Missing[0].SourceBase != "source-b" {
			t.Fatalf("validated entries were resent: %+v", member)
		}
	}
}

func TestRubyBatchHTTPPauseDrainsAllResponseMembersAndResumeOnlyCommits(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	gate := pipeline.NewPauseGate()
	paused := false
	_, _, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
			if c.LogicalAttempt != 1 || paused {
				return nil
			}
			paused = true
			if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
				return err
			}
			gate.Pause()
			return nil
		}}
	})
	if err != nil || !paused {
		t.Fatalf("pause during batch handoff: %v", err)
	}
	ctx := context.Background()
	rows := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).AllX(ctx)
	if len(rows) != 4 {
		t.Fatalf("batch pause saved %d members", len(rows))
	}
	for _, row := range rows {
		if row.Version != 2 || (row.State != "ready_to_commit" && row.State != "completed") {
			t.Fatalf("pause lost a received member: %+v", row)
		}
	}
	if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("resume=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 2)
	if got := alignmentHTTPRequests(f); len(got) != 1 {
		t.Fatal("resume repeated already saved batch response")
	}
}

func TestRubyBatchHTTPPartialSaveRecoveryResendsOnlyUnsavedMembers(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	injected := errors.New("process stopped between batch member saves")
	savedAlignment := 0
	_, _, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, beforeSave: func(_ context.Context, c *pipeline.Candidate) error {
			if c.LogicalAttempt != 1 {
				return nil
			}
			savedAlignment++
			if savedAlignment == 2 {
				return permanentStoreError{injected}
			}
			return nil
		}}
	})
	if !errors.Is(err, injected) {
		t.Fatalf("partial member failure: %v", err)
	}
	ctx := context.Background()
	rows := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).Order(ent.Asc(workcandidate.FieldID)).AllX(ctx)
	ready := 0
	var savedID string
	for _, row := range rows {
		if row.Version == 2 {
			ready++
			savedID = row.Identity
		}
	}
	if ready != 1 {
		t.Fatalf("received batch saved %d members before interruption", ready)
	}
	if err := f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("partial-save recovery=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 3)
	requests := alignmentHTTPRequests(f)
	if len(requests) != 2 || len(requests[1].Alignments) != 3 {
		t.Fatalf("recovery batch=%+v", requests)
	}
	for _, member := range requests[1].Alignments {
		if member.CandidateID == savedID {
			t.Fatal("recovery resent saved member")
		}
	}
}

func TestRubyBatchHTTPProtocolIsolationSurvivesResumeAndRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "resume"
		if retry {
			name = "retry"
		}
		t.Run(name, func(t *testing.T) {
			f := newRubyBatchFixture(t, 4)
			f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
				if len(request.Alignments) > 0 {
					return map[string]any{"alignments": []any{}}, nil
				}
				return rubyPipelineResponse(request), nil
			}
			gate := pipeline.NewPauseGate()
			paused := false
			_, _, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
				return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
					if !c.ForceSingleAlignment || paused {
						return nil
					}
					paused = true
					if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
						return err
					}
					gate.Pause()
					return nil
				}}
			})
			if err != nil || !paused {
				t.Fatalf("protocol fault save: %v", err)
			}
			ctx := context.Background()
			for _, row := range f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).AllX(ctx) {
				c, err := pipeline.DecodeCandidate(row.Payload)
				if err != nil || !c.ForceSingleAlignment || c.LogicalAttempt != 1 || c.Ready {
					t.Fatalf("isolation not durable: %+v, %v", c, err)
				}
			}
			if retry {
				if _, err := f.jobs.CancelJob(ctx, f.ownerID, f.jobID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.jobs.MarkJobPaused(ctx, f.jobID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
					t.Fatal(err)
				}
			}
			_, result, err := f.execute(t, 0, nil, nil)
			if err != nil || len(result.Unresolved) != 0 {
				t.Fatalf("isolated recovery=%+v, %v", result, err)
			}
			f.assertAccepted(t, 0, 4)
			f.assertUsage(t, 6)
			requests := alignmentHTTPRequests(f)
			if len(requests) != 5 {
				t.Fatalf("protocol fallback sent %d requests", len(requests))
			}
			for _, request := range requests[1:] {
				if len(request.Alignments) != 0 || len(request.Missing) != 2 {
					t.Fatalf("isolated candidate rejoined batch: %+v", request)
				}
			}
		})
	}
}

func TestRubyBatchHTTPEditedMemberDoesNotDiscardOtherReceivedMembers(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	editedID := 0
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Alignments) > 0 {
			candidate, err := f.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(request.Alignments[0].CandidateID)).WithWorkItem().Only(ctx)
			if err != nil {
				return nil, err
			}
			editedID = candidate.Edges.WorkItem.SegmentID
			if err := f.client.Segment.UpdateOneID(editedID).SetTargetText("manual edit").SetStatus(segment.StatusEdited).Exec(ctx); err != nil {
				return nil, err
			}
		}
		return rubyPipelineResponse(request), nil
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 3 || len(result.Unresolved) != 1 {
		t.Fatalf("member edit discarded batch response: %+v, %v", result, err)
	}
	ctx := context.Background()
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		want := rubyPipelineTarget
		if row.ID == editedID {
			want = "manual edit"
		}
		if row.TargetText == nil || *row.TargetText != want || row.ContentVersion != 2 {
			t.Fatalf("per-member result lost: %+v", row)
		}
	}
	f.assertUsage(t, 2)
	if got := alignmentHTTPRequests(f); len(got) != 1 {
		t.Fatal("edited member caused unnecessary batch retry")
	}
}

func TestRubyBatchHTTPCASStaleMemberDoesNotDiscardOtherReceivedMembers(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	editedID := 0
	_, result, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, beforeSave: func(ctx context.Context, c *pipeline.Candidate) error {
			if c.LogicalAttempt != 1 || editedID != 0 {
				return nil
			}
			// RunBatch has already validated this member, but its candidate
			// transaction has not started. The normal ent hook changes the CAS
			// version as a concurrent user edit would.
			editedID = c.Segment.DBID
			return f.client.Segment.UpdateOneID(editedID).SetTargetText("edit between validation and save").SetStatus(segment.StatusEdited).Exec(ctx)
		}}
	})
	if err != nil || len(result.Resolved) != 3 || len(result.Unresolved) != 1 || editedID == 0 {
		t.Fatalf("CAS stale member discarded batch response: %+v, %v", result, err)
	}
	ctx := context.Background()
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		want := rubyPipelineTarget
		if row.ID == editedID {
			want = "edit between validation and save"
		}
		if row.TargetText == nil || *row.TargetText != want || row.ContentVersion != 2 {
			t.Fatalf("CAS race lost per-member result: %+v", row)
		}
	}
	retired := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.SegmentIDEQ(editedID))).OnlyX(ctx)
	if retired.State != "rejected" || retired.Version != 1 || len(retired.Payload) != 0 {
		t.Fatalf("failed save did not retire exactly the prior durable candidate: %+v", retired)
	}
	f.assertUsage(t, 2)
	if got := alignmentHTTPRequests(f); len(got) != 1 {
		t.Fatal("CAS race caused unnecessary batch retry")
	}
}

func TestRubyBatchHTTPNetworkFailurePreservesConfiguredBatchAndLogicalBudget(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	var mu sync.Mutex
	failed := false
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Alignments) > 0 {
			mu.Lock()
			first := !failed
			failed = true
			mu.Unlock()
			if first {
				return nil, errors.New("transient provider error")
			}
		}
		return rubyPipelineResponse(request), nil
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 4 {
		t.Fatalf("network retry=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	requests := alignmentHTTPRequests(f)
	if len(requests) != 2 || len(requests[0].Alignments) != 4 || len(requests[1].Alignments) != 4 {
		t.Fatalf("network error changed configured batch: %+v", requests)
	}
	ctx := context.Background()
	for _, w := range f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).AllX(ctx) {
		if w.AlignmentAttempts != 1 {
			t.Fatalf("network retry consumed another logical attempt: %+v", w)
		}
	}
	if f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).CountX(ctx) != 3 || f.client.UsageRecord.Query().CountX(ctx) != 3 {
		t.Fatal("network retry was charged per member instead of invocation")
	}
}

func TestRubyBatchHTTPDuplicateIdentityIsolatesOnlyAffectedMember(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		response := rubyPipelineResponse(request)
		if len(request.Alignments) > 0 {
			batch := response.(map[string]any)
			members := batch["alignments"].([]map[string]any)
			// Duplicate only one known member and add an unrelated identity.
			// The other three valid members must retain their successful result.
			members = append(members, members[0], map[string]any{"work_id": "unknown", "candidate_id": "unknown", "ruby_output": []any{}})
			batch["alignments"] = members
		}
		return response, nil
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 4 {
		t.Fatalf("duplicate identity=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 3)
	requests := alignmentHTTPRequests(f)
	if len(requests) != 2 || len(requests[0].Alignments) != 4 || len(requests[1].Alignments) != 0 || len(requests[1].Missing) != 2 {
		t.Fatalf("unaffected members were retried: %+v", requests)
	}
}

func TestRubyBatchHTTPProtocolFallbackDoesNotGrantAdditionalLogicalBudget(t *testing.T) {
	f := newRubyBatchFixture(t, 4)
	f.snapshot.RubyRetry.MaxAttempts = 1
	f.persistSnapshot(t)
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Alignments) > 0 {
			return map[string]any{"alignments": []any{}}, nil
		}
		return rubyPipelineResponse(request), nil
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 4 {
		t.Fatalf("exhausted protocol fallback=%+v, %v", result, err)
	}
	f.assertUsage(t, 2)
	if requests := alignmentHTTPRequests(f); len(requests) != 1 {
		t.Fatalf("fallback granted extra requests: %+v", requests)
	}
	ctx := context.Background()
	for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
		if row.TargetText == nil || *row.TargetText != "alpha beta" || row.ContentVersion != 2 || len(row.QualityIssues) == 0 {
			t.Fatalf("budget exhaustion lost accepted text or warning: %+v", row)
		}
	}
}
