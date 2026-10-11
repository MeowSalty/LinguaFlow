package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestRubyBatchHTTPStorageStaleWithoutBaselineChangeRemainsAnError(t *testing.T) {
	for _, fault := range []string{"request_proof", "cursor_monotonicity"} {
		t.Run(fault, func(t *testing.T) {
			f := newRubyBatchFixture(t, 4)
			armed, injected := false, false
			if fault == "cursor_monotonicity" {
				f.client.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if _, candidateSave := m.Field(workitem.FieldCandidateID); candidateSave && armed && !injected {
							// After roundStore loaded the cursor, the candidate
							// transaction changes it before saveCursor checks it.
							// The failing handoff must roll back this mutation too.
							injected = true
							m.(*ent.WorkItemMutation).AddMainAttempts(1)
						}
						return next.Mutate(ctx, m)
					})
				})
			}
			_, result, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
				return &rubyPipelineFaultStore{roundStore: store, beforeSave: func(_ context.Context, c *pipeline.Candidate) error {
					if c.LogicalAttempt != 1 {
						return nil
					}
					armed = true
					if fault == "request_proof" {
						injected = true
						c.LastAlignmentRequestID = "unrelated-request"
					}
					return nil
				}}
			})
			if !injected || !errors.Is(err, workstate.ErrStale) || errors.Is(err, pipeline.ErrCandidateStale) || len(result.Resolved) != 0 {
				t.Fatalf("bookkeeping failure was swallowed as a stale candidate: result=%+v err=%v injected=%v", result, err, injected)
			}
			ctx := context.Background()
			for _, c := range f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).AllX(ctx) {
				if c.Version != 1 || c.State != "pending_alignment" || len(c.Payload) == 0 {
					t.Fatalf("bookkeeping failure retired or changed a valid candidate: %+v", c)
				}
			}
			for _, w := range f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).AllX(ctx) {
				if w.MainAttempts != 1 || w.AlignmentAttempts != 1 || w.AlignmentNetworkAttempts != 1 {
					t.Fatalf("failed candidate/cursor transaction leaked counters: %+v", w)
				}
			}
			for _, s := range f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).AllX(ctx) {
				if s.TargetText != nil || s.ContentVersion != 1 {
					t.Fatalf("test changed the accepted baseline: %+v", s)
				}
			}
			f.assertUsage(t, 2)
			// Recovery retains each request debit and all main candidates; fixing
			// bookkeeping never requires retranslating the main text.
			if err := f.jobs.PrepareRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			_, result, err = f.execute(t, 0, nil, nil)
			if err != nil || len(result.Unresolved) != 0 {
				t.Fatalf("bookkeeping recovery=%+v, %v", result, err)
			}
			f.assertAccepted(t, 0, 4)
			f.assertUsage(t, 3)
		})
	}
}
