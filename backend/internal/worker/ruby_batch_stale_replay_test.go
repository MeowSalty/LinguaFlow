package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestRubyBatchHTTPUnknownSaveCommitThenEditRetiresDurableSuccessor(t *testing.T) {
	for _, batchSize := range []int{1, 4} {
		t.Run(fmt.Sprintf("batch=%d", batchSize), func(t *testing.T) {
			f := newRubyBatchFixture(t, batchSize)
			const editedText = "manual edit after unacknowledged candidate save"
			unknownCommit := errors.New("candidate transaction committed but acknowledgement was lost")
			editedID, attemptedSaves := 0, 0
			var editedCandidate, requestID string
			_, result, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
				return &rubyPipelineFaultStore{roundStore: store,
					beforeSave: func(_ context.Context, c *pipeline.Candidate) error {
						if c.Index == 1 && c.LogicalAttempt == 1 {
							attemptedSaves++
						}
						return nil
					},
					afterSave: func(ctx context.Context, c *pipeline.Candidate) error {
						if c.Index != 1 || c.LogicalAttempt != 1 || editedID != 0 {
							return nil
						}
						// The real SQLite handoff has committed version 2. Inject
						// an acknowledgement failure after a concurrent edit, so
						// retrying the same save encounters the changed baseline.
						candidate, err := f.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(c.ID)).Only(ctx)
						if err != nil {
							return err
						}
						if candidate.Version != 2 || candidate.State != "ready_to_commit" {
							return permanentStoreError{fmt.Errorf("fault must follow durable successor save: version=%d state=%s", candidate.Version, candidate.State)}
						}
						request, err := f.client.WorkRequest.Query().Where(workrequest.IdentityEQ(c.LastAlignmentRequestID)).Only(ctx)
						if err != nil {
							return err
						}
						if request.State != "received" {
							return permanentStoreError{fmt.Errorf("request completed before member save acknowledgement: %s", request.State)}
						}
						editedID, editedCandidate, requestID = c.Segment.DBID, c.ID, c.LastAlignmentRequestID
						if err := f.client.Segment.UpdateOneID(editedID).SetTargetText(editedText).SetStatus(segment.StatusEdited).Exec(ctx); err != nil {
							return err
						}
						return unknownCommit
					},
				}
			})
			if err != nil || len(result.Resolved) != 3 || len(result.Unresolved) != 1 || editedID == 0 || attemptedSaves != 2 {
				t.Fatalf("unknown-commit stale retirement failed: result=%+v err=%v edited=%d saves=%d", result, err, editedID, attemptedSaves)
			}
			ctx := context.Background()
			for _, row := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
				want := rubyPipelineTarget
				if row.ID == editedID {
					want = editedText
				}
				if row.TargetText == nil || *row.TargetText != want || row.ContentVersion != 2 {
					t.Fatalf("edit or other received member was lost: %+v", row)
				}
			}
			retired := f.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(editedCandidate)).OnlyX(ctx)
			if retired.Version != 2 || retired.State != "rejected" || len(retired.Payload) != 0 {
				t.Fatalf("retirement did not use the actually saved successor: %+v", retired)
			}
			work := f.client.WorkItem.GetX(ctx, retired.WorkItemID)
			if work.CandidateID != "" || work.State != "unresolved" {
				t.Fatalf("stale member remained active: %+v", work)
			}
			if count := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0])), workcandidate.StateEQ("completed")).CountX(ctx); count != 3 {
				t.Fatalf("other members were discarded after unknown commit: completed=%d", count)
			}
			if request := f.client.WorkRequest.Query().Where(workrequest.IdentityEQ(requestID)).OnlyX(ctx); request.State != "completed" {
				t.Fatalf("successful stale retirement left request unfinished: %+v", request)
			}
			wantAlignmentCalls := 1
			if batchSize == 1 {
				wantAlignmentCalls = 4
			}
			f.assertUsage(t, 1+wantAlignmentCalls)
			if requests := alignmentHTTPRequests(f); len(requests) != wantAlignmentCalls {
				t.Fatalf("unknown storage commit resent model requests: %+v", requests)
			}
		})
	}
}
