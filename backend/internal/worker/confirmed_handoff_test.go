package worker

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type confirmedSQLFailureStore struct {
	*roundStore
	cause error
}

func (s *confirmedSQLFailureStore) Commit(context.Context, *pipeline.Candidate, pipeline.TranslatedSegment) (pipeline.CommitOutcome, error) {
	return "", permanentStoreError{s.cause}
}

func TestConfirmedSQLiteHandoffSurvivesOtherResourceStorageFailure(t *testing.T) {
	f := newRubyPipelineFixture(t, 1, 1)
	f.snapshot.Strategy.Ruby.Enabled, f.snapshot.RubyRetry = false, nil
	f.snapshot.Rounds[0].Translate.BatchSize = 1
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	committed, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	firstCtx := backend.WithMemberWorkRecorder(ctx, func(event backend.MemberWorkEvent) {
		if event.Phase == "confirmation" && event.Operation == "commit_ack" {
			close(committed)
			<-release
		}
	})
	firstCtx = pipeline.WithExecutionRuntime(firstCtx, r)
	eng, err := f.factory.BuildEngine(firstCtx, f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, durable := f.round(t, 0)
	defer r.CloseDocument(doc)
	eng.PrepareDocument(doc, nil)
	type completion struct {
		result pipeline.RunRoundResult
		err    error
	}
	done, joined := make(chan completion, 1), false
	defer func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		if !joined {
			<-done
		}
	}()
	go func() {
		result, err := pipeline.RunStagedRound(firstCtx, eng.Rounds()[0], doc, durable, r, f.logger, progress.Nop{})
		done <- completion{result, err}
	}()
	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal("SQLite confirmation barrier not reached")
	}
	before := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
	if before.TargetText == nil || *before.TargetText == "" || before.ContentVersion != 2 || doc.Segments[0].Target != "" {
		t.Fatalf("not between durable save and projection: SQL=%+v doc=%+v", before, doc.Segments[0])
	}
	cause := errors.New("other SQLite resource storage failed")
	other, otherStore := f.round(t, 1)
	defer r.CloseDocument(other)
	eng.PrepareDocument(other, nil)
	_, err = pipeline.RunStagedRound(pipeline.WithExecutionRuntime(ctx, r), eng.Rounds()[0], other, &confirmedSQLFailureStore{roundStore: otherStore, cause: cause}, r, f.logger, progress.Nop{})
	var storage *pipeline.StorageError
	if !errors.Is(err, cause) || !errors.As(err, &storage) {
		t.Fatalf("storage cause lost: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case out := <-done:
		joined = true
		if out.err != nil && !errors.Is(out.err, backend.ErrAdmissionClosed) {
			t.Fatalf("first SQLite result=%+v err=%v", out.result, out.err)
		}
	case <-ctx.Done():
		t.Fatal("SQLite projection did not join")
	}
	got := doc.Segments[0]
	if got.Target != *before.TargetText || got.Status != string(before.Status) || got.ContentVersion != before.ContentVersion || got.TargetIsNull || !got.RoundWork.Completed || !reflect.DeepEqual(got.Issues, before.QualityIssues) {
		t.Fatalf("SQLite and Document differ: SQL=%+v doc=%+v", before, got)
	}
	if other.Segments[0].Target != "" || f.client.JobRoundSegment.Query().CountX(ctx) != 1 {
		t.Fatal("unconfirmed sibling became complete")
	}
	main, alignment, _, _ := f.calls()
	if main != 2 || alignment != 0 {
		t.Fatalf("paid requests repeated: main=%d alignment=%d", main, alignment)
	}
	id := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(f.roundIDs[0])).OnlyX(ctx).CommitID
	if id == nil {
		t.Fatal("confirmed SQLite checkpoint has no identity")
	}
	if r.Admission.BuildBytes("candidate:"+*id) != 0 {
		t.Fatal("SQLite confirmed candidate lost its final refund")
	}
}

type stagedExistingSQLStore struct {
	*roundStore
	candidate *pipeline.Candidate
	payload   []byte
	edit      bool
}

func (s *stagedExistingSQLStore) LoadCanonical(ctx context.Context, doc *pipeline.Document) (bool, int, error) {
	sealed, total, err := s.roundStore.LoadCanonical(ctx, doc)
	if err == nil {
		doc.Segments[0].RoundWork.Completed = false
	}
	return sealed, total, err
}
func (s *stagedExistingSQLStore) CandidateHeaders(_ context.Context, after, _ int) ([]*pipeline.Candidate, int, error) {
	if after > 0 {
		return nil, after, nil
	}
	return []*pipeline.Candidate{pipeline.HeaderForCandidate(s.candidate).Candidate()}, 1, nil
}
func (s *stagedExistingSQLStore) CandidateFrame(context.Context, string, int64) (pipeline.CandidateFrame, error) {
	c := s.candidate
	return pipeline.CandidateFrame{Payload: s.payload, ID: c.ID, WorkID: c.WorkID, Version: c.Version, Index: c.Index, SegmentID: c.Segment.DBID}, nil
}
func (*stagedExistingSQLStore) ValidateCandidate(context.Context, *pipeline.Candidate) (bool, error) {
	return true, nil
}
func (s *stagedExistingSQLStore) PrepareCandidateCommit(ctx context.Context, b *backend.BuildBounds, c *pipeline.Candidate, result pipeline.TranslatedSegment) (pipeline.TranslatedSegment, error) {
	prepared, err := s.roundStore.PrepareCandidateCommit(ctx, b, c, result)
	if err == nil && s.edit {
		s.client.Segment.UpdateOneID(c.Segment.DBID).SetTargetText("edited after authoritative preparation").SetStatus(segment.StatusApproved).ExecX(ctx)
	}
	return prepared, err
}

// This deliberately supplies a stale replay frame and bypasses the advisory
// preflight validator. The real staged finalize/Prepare/SQL Commit proof must
// still reconcile Existing, or reject an edit between preparation and Commit.
func TestStagedSQLiteExistingUsesAuthoritativeBodyAndVersionProof(t *testing.T) {
	for _, mode := range []string{"existing", "edited_after_prepare", "confirmed_null_target"} {
		t.Run(mode, func(t *testing.T) {
			edit := mode == "edited_after_prepare"
			f := newRubyPipelineFixture(t, 1)
			f.snapshot.Strategy.Ruby.Enabled, f.snapshot.RubyRetry = false, nil
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			var saved *pipeline.Candidate
			doc, result, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
				return &rubyPipelineFaultStore{roundStore: store, afterSave: func(_ context.Context, c *pipeline.Candidate) error {
					if c.Ready {
						bytes, err := c.Encode()
						if err != nil {
							return err
						}
						saved, err = pipeline.DecodeCandidate(bytes)
						return err
					}
					return nil
				}}
			})
			if err != nil || len(result.Resolved) != 1 || saved == nil {
				t.Fatalf("initial save=%+v err=%v candidate=%v", result, err, saved != nil)
			}
			authority := doc.Segments[0]
			if mode == "confirmed_null_target" {
				f.client.Segment.UpdateOneID(saved.Segment.DBID).ClearTargetText().SetStatus(segment.StatusPending).ExecX(t.Context())
				authority.Target, authority.TargetIsNull, authority.Status, authority.ContentVersion = "", true, string(segment.StatusPending), authority.ContentVersion+1
			}
			saved.Segment.Target = "unconfirmed replay body"
			saved.Segment.Issues = []qa.QualityIssue{{Code: "fake", Message: "unconfirmed"}}
			saved.Segment.Meta = map[string]any{"injected": "unconfirmed"}
			saved.Segment.Protected = map[string]string{"injected": "unconfirmed"}
			payload, err := saved.Encode()
			if err != nil {
				t.Fatal(err)
			}
			saved.StoredBytes = int64(len(payload))
			doc, result, err = f.executePrepared(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
				return &stagedExistingSQLStore{roundStore: store, candidate: saved, payload: payload, edit: edit}
			}, func(_ context.Context, r *pipeline.ExecutionRuntime, store *roundStore) {
				if err := r.Window.RestoreScoped(saved.ID, int64(len(payload)), store.scope.ResourceID, store.scope.RoundID); err != nil {
					t.Fatal(err)
				}
			})
			if edit {
				if !errors.Is(err, workstate.ErrStale) || doc.Segments[0].Target == saved.Segment.Target || f.client.Segment.GetX(t.Context(), saved.Segment.DBID).ContentVersion != 3 {
					t.Fatalf("edit proof accepted stale body: err=%v doc=%+v", err, doc.Segments[0])
				}
			} else if err != nil || len(result.Resolved) != 1 || doc.Segments[0].Target != authority.Target || doc.Segments[0].TargetIsNull != authority.TargetIsNull || doc.Segments[0].Status != authority.Status || doc.Segments[0].ContentVersion != authority.ContentVersion || !reflect.DeepEqual(doc.Segments[0].Issues, authority.Issues) || doc.Segments[0].Meta["injected"] != nil || doc.Segments[0].Protected["injected"] != "" {
				t.Fatalf("existing installed candidate instead of authority: err=%v doc=%+v authority=%+v", err, doc.Segments[0], authority)
			}
			main, align, _, _ := f.calls()
			if main != 1 || align != 0 || f.client.JobRoundSegment.Query().CountX(t.Context()) != 1 {
				t.Fatal("Existing replay requested a model or duplicated checkpoint")
			}
		})
	}
}

func TestStagedSQLiteNoopPreservesAuthoritativeIssues(t *testing.T) {
	f, barrier := newHandoffHTTPFixture(t, "revise", 1, 1, 1)
	barrier.open()
	row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(t.Context())
	f.client.Segment.UpdateOneID(row.ID).SetTargetText("accepted-" + row.SourceText).SetQualityIssues([]qa.QualityIssue{
		{Code: "calque", Severity: qa.SeverityWarning, Message: "keep unresolved original", Disposition: qa.DispositionPending},
		{Code: "other", Severity: qa.SeverityWarning, Message: "already reviewed", Disposition: qa.DispositionDismissed},
	}).ExecX(t.Context())
	before := f.client.Segment.GetX(t.Context(), row.ID)
	doc, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Resolved) != 1 {
		t.Fatalf("noop result=%+v err=%v", result, err)
	}
	after := f.client.Segment.GetX(t.Context(), row.ID)
	if after.ContentVersion != before.ContentVersion || doc.Segments[0].ContentVersion != before.ContentVersion || doc.Segments[0].Target != *before.TargetText || doc.Segments[0].Status != string(before.Status) || !reflect.DeepEqual(after.QualityIssues, before.QualityIssues) || !reflect.DeepEqual(doc.Segments[0].Issues, before.QualityIssues) {
		t.Fatalf("noop installed filtered candidate: before=%+v SQL=%+v doc=%+v", before, after, doc.Segments[0])
	}
	checkpoint := f.client.JobRoundSegment.Query().OnlyX(t.Context())
	if checkpoint.Outcome != string(workstate.ConfirmedNoop) {
		t.Fatalf("not a durable noop: %+v", checkpoint)
	}
	barrier.mu.Lock()
	calls := barrier.calls
	barrier.mu.Unlock()
	if calls != 1 {
		t.Fatalf("noop repeated model: calls=%d", calls)
	}
}
