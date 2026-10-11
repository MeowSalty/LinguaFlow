package worker

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestRubyRetryRepeatedRecoveryPreservesUnknownAttemptBudgets(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	f.respond = func(_ context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Missing) == 2 {
			request.Missing = request.Missing[:1]
		}
		return rubyPipelineResponse(request), nil
	}
	gate := pipeline.NewPauseGate()
	_, _, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
			if c.LogicalAttempt != 1 || c.Ready {
				return nil
			}
			if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
				return err
			}
			gate.Pause()
			return nil
		}}
	})
	if err != nil || !gate.Paused() {
		t.Fatalf("save partial alignment: %v", err)
	}
	ctx := context.Background()
	saved := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).OnlyX(ctx)
	originalPayload := bytes.Clone(saved.Payload)
	original, err := pipeline.DecodeCandidate(originalPayload)
	if err != nil || len(original.Alignment.Verified) != 1 || original.LogicalAttempt != 1 || original.RetryEpoch != 0 {
		t.Fatalf("original partial candidate: %+v, %v", original, err)
	}
	verified := original.Alignment.Verified[0]
	if _, err := f.jobs.CancelJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	retried, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID)
	if err != nil || retried.RetryEpoch != 1 {
		t.Fatalf("explicit retry: %+v, %v", retried, err)
	}
	load := func(network int) (*roundStore, *pipeline.Candidate) {
		t.Helper()
		_, store := f.round(t, 0)
		candidates, _, err := store.Candidates(ctx, 0, 1)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("recover retained candidate: count=%d, %v", len(candidates), err)
		}
		candidate := candidates[0]
		if candidate.RetryEpoch != 1 || candidate.LogicalAttempt != 0 || candidate.NetworkAttempt != network || candidate.MainAttempt != 0 || candidate.PoolIndex != 0 {
			t.Fatalf("recovery refreshed retry budgets: %+v, want network=%d", candidate, network)
		}
		if len(candidate.Alignment.Verified) != 1 || candidate.Alignment.Verified[0] != verified || len(candidate.Alignment.Missing()) != 1 {
			t.Fatal("recovery discarded verified alignment")
		}
		return store, candidate
	}
	networkBudget := f.snapshot.Rounds[0].Translate.Retry.MaxAttempts
	for network := 1; network <= networkBudget; network++ {
		store, candidate := load(network - 1)
		requestID := fmt.Sprintf("retry-unknown-%d", network)
		if err := store.Reserve(ctx, pipeline.RequestIntent{
			ID: requestID, CandidateID: candidate.ID, Indices: []int{candidate.Index},
			Stage: backend.RequestStageAlignment, BackendID: f.snapshot.RubyRetry.Backend.ID,
			Pool: candidate.PoolIndex, LogicalAttempt: candidate.LogicalAttempt, NetworkAttempt: candidate.NetworkAttempt,
			Phase: "alignment", InputDigest: requestID,
		}); err != nil {
			t.Fatalf("reserve alignment network attempt %d: %v", network, err)
		}
		// The process can disappear after the debit without saving a response or
		// the rebased DTO. Startup recovery owns these uncertain reservations.
		for restart := range 3 {
			if err := f.jobs.PrepareRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			load(network)
			cursor := f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).OnlyX(ctx)
			if cursor.RetryEpoch != 1 || cursor.AlignmentAttempts != 1 || cursor.AlignmentNetworkAttempts != network || cursor.MainAttempts != 0 {
				t.Fatalf("restart %d changed durable attempt debits: %+v", restart, cursor)
			}
			request := f.client.WorkRequest.Query().Where(workrequest.IdentityEQ(requestID)).OnlyX(ctx)
			if request.State != "unknown" {
				t.Fatalf("restart %d left request state %q", restart, request.State)
			}
			current := f.client.WorkCandidate.GetX(ctx, saved.ID)
			if !bytes.Equal(current.Payload, originalPayload) || current.Version != saved.Version {
				t.Fatal("test must recover from the original epoch DTO without a saved response")
			}
		}
	}
	beforeMain, beforeAlignment, _, _ := f.calls()
	for range 2 {
		_, result, err := f.execute(t, 0, nil, nil)
		if err != nil || len(result.Resolved) != 1 || len(result.Unresolved) != 0 {
			t.Fatalf("exhausted alignment finalization: %+v, %v", result, err)
		}
	}
	if main, alignment, _, _ := f.calls(); main != beforeMain || alignment != beforeAlignment {
		t.Fatalf("exhausted recovery made new requests: main=%d alignment=%d, before=%d/%d", main, alignment, beforeMain, beforeAlignment)
	}
	row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
	wantTarget := "<ruby>alpha<rt>reading-a</rt></ruby> beta"
	if row.TargetText == nil || *row.TargetText != wantTarget || row.ContentVersion != 2 || row.Status != segment.StatusTranslated {
		t.Fatalf("exhausted recovery lost verified translation: %+v", row)
	}
	warning := false
	for _, issue := range row.QualityIssues {
		warning = warning || issue.Code == qa.CodeRubyRestoreIncomplete && issue.Severity == qa.SeverityWarning
	}
	if !warning {
		t.Fatal("exhausted alignment did not preserve the incomplete-ruby warning")
	}
	if f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).CountX(ctx) != 1 || f.client.Job.GetX(ctx, f.jobID).ProgressCompleted != 1 {
		t.Fatal("repeated exhausted recovery lost or duplicated confirmation")
	}
	if got := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.RetryEpochEQ(1)).CountX(ctx); got != networkBudget {
		t.Fatalf("retry epoch requests=%d, want %d", got, networkBudget)
	}
}
