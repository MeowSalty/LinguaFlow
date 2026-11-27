package worker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type correctCommitBarrier struct {
	*roundStore
	beforeSeal func()
	before     func()
	after      error
}

func (s *correctCommitBarrier) SealCorrectCanonical(ctx context.Context, doc *pipeline.Document) error {
	if s.beforeSeal != nil {
		s.beforeSeal()
	}
	return s.roundStore.SealCorrectCanonical(ctx, doc)
}

func (s *correctCommitBarrier) CommitCorrect(ctx context.Context, id string, baseline pipeline.Segment, result pipeline.TranslatedSegment) (pipeline.CommitOutcome, int64, error) {
	if s.before != nil {
		s.before()
	}
	outcome, version, err := s.roundStore.CommitCorrect(ctx, id, baseline, result)
	if err == nil && s.after != nil {
		return outcome, version, permanentStoreError{s.after}
	}
	return outcome, version, err
}

func correctFixture(t *testing.T, count int) (*rubyPipelineFixture, []int) {
	t.Helper()
	f := newRubyPipelineFixture(t, count)
	ctx := t.Context()
	f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetMode(pipeline.RoundModeCorrect).ExecX(ctx)
	ids := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).Order(segment.BySegmentIndex()).IDsX(ctx)
	for _, id := range ids {
		f.client.Segment.UpdateOneID(id).SetSourceText("「（对话）」").SetTargetText("对话").SetStatus(segment.StatusEdited).SetReviewComment("keep review").SetQualityIssues([]qa.QualityIssue{
			{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning},
			{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning, Span: &qa.Span{MatchedText: "（）"}, Disposition: qa.DispositionDismissed, Note: "keep dismissal"},
		}).ExecX(ctx)
	}
	return f, ids
}

func executeCorrectFixture(t *testing.T, f *rubyPipelineFixture, wrap func(*roundStore) pipeline.RoundStore) (*pipeline.Document, pipeline.RunRoundResult, error) {
	t.Helper()
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, pipeline.NewPauseGate(), pipeline.DefaultCandidateLimits())
	if err != nil {
		t.Fatal(err)
	}
	doc, store := f.round(t, 0)
	store.bindCanonical(doc)
	var durable pipeline.RoundStore = store
	if wrap != nil {
		durable = wrap(store)
	}
	t.Cleanup(func() {
		if err := errors.Join(runtime.CloseDocument(doc), runtime.Close()); err != nil {
			t.Error(err)
		}
	})
	rules := correct.New(correct.Config{Rules: []correct.RuleConfig{{Name: correct.RulePunctuationMissingWrap, Enabled: true}}})
	handler := &pipeline.CorrectHandler{Rules: rules, Idempotency: qa.NewEngine(qa.Config{Enabled: true, Checks: rules.ConsumedIssueCodes()}, f.logger)}
	result, err := pipeline.RunRound(t.Context(), pipeline.Round{Handler: handler, Runtime: runtime, Store: durable, Concurrency: 1}, doc, nil, f.logger, progress.Nop{})
	return doc, result, err
}

func assertCorrectNoModelState(t *testing.T, f *rubyPipelineFixture) {
	t.Helper()
	ctx := t.Context()
	main, alignment, _, _ := f.calls()
	if main != 0 || alignment != 0 || f.client.WorkRequest.Query().CountX(ctx) != 0 || f.client.WorkCandidate.Query().CountX(ctx) != 0 {
		t.Fatal("local correction produced model or candidate state")
	}
}

func assertCorrectRow(t *testing.T, f *rubyPipelineFixture, id int, wantTarget string, wantVersion int64, wantIssues int) {
	t.Helper()
	row := f.client.Segment.GetX(t.Context(), id)
	if row.TargetText == nil || *row.TargetText != wantTarget || row.ContentVersion != wantVersion || row.Status != segment.StatusEdited || row.ReviewComment == nil || *row.ReviewComment != "keep review" || len(row.QualityIssues) != wantIssues {
		t.Fatalf("segment after local correction = %+v", row)
	}
	if issue := row.QualityIssues[len(row.QualityIssues)-1]; !issue.Dismissed() || issue.Note != "keep dismissal" {
		t.Fatalf("dismissed issue = %+v", issue)
	}
}

func TestCorrectRoundAtomicCompletionAndRecovery(t *testing.T) {
	ctx := t.Context()
	f, ids := correctFixture(t, 2)
	before, second := f.client.Segment.GetX(ctx, ids[0]).ContentVersion, f.client.Segment.GetX(ctx, ids[1]).ContentVersion
	trigger := fmt.Sprintf(`CREATE TRIGGER fail_correct_confirmation BEFORE INSERT ON job_round_segments WHEN NEW.segment_id = %d BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END`, ids[1])
	if _, err := f.client.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	doc, _, err := executeCorrectFixture(t, f, nil)
	var storage *pipeline.StorageError
	if !errors.As(err, &storage) || doc.Segments[1].Target != "对话" || doc.Segments[1].RoundWork.Completed {
		t.Fatalf("completion failure published local draft: err=%v segment=%+v", err, doc.Segments[1])
	}
	assertCorrectRow(t, f, ids[0], "「对话」", before+1, 1)
	assertCorrectRow(t, f, ids[1], "对话", second, 2)
	row := f.client.JobRound.GetX(ctx, f.roundIDs[0])
	if row.SegmentTotal != 2 || row.SegmentCompleted != 1 || f.client.JobRoundSegment.Query().CountX(ctx) != 1 {
		t.Fatalf("round after failed completion = %+v", row)
	}
	if _, err := f.client.ExecContext(ctx, "DROP TRIGGER fail_correct_confirmation"); err != nil {
		t.Fatal(err)
	}
	doc, result, err := executeCorrectFixture(t, f, nil)
	if err != nil || len(result.Resolved) != 2 || len(result.Unresolved) != 0 || doc.Segments[1].Target != "「对话」" {
		t.Fatalf("correct recovery = %+v, err=%v, segment=%+v", result, err, doc.Segments[1])
	}
	assertCorrectRow(t, f, ids[0], "「对话」", before+1, 1)
	assertCorrectRow(t, f, ids[1], "「对话」", second+1, 1)
	if row := f.client.JobRound.GetX(ctx, f.roundIDs[0]); row.SegmentCompleted != 2 || f.client.JobRoundSegment.Query().CountX(ctx) != 2 {
		t.Fatalf("round after recovery = %+v", row)
	}
	assertCorrectNoModelState(t, f)
}

func TestCorrectRoundExecutionFences(t *testing.T) {
	for _, scenario := range []string{"pause", "cancel", "old_epoch", "edit", "commit_before_projection"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f, ids := correctFixture(t, 1)
			before := f.client.Segment.GetX(ctx, ids[0]).ContentVersion
			wantTarget, wantVersion, wantIssues := "对话", before, 2
			var wantError error
			wantCompleted := 0
			doc, result, err := executeCorrectFixture(t, f, func(store *roundStore) pipeline.RoundStore {
				barrier := &correctCommitBarrier{roundStore: store}
				switch scenario {
				case "pause":
					barrier.before = func() { f.client.Job.UpdateOneID(f.jobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx) }
				case "cancel":
					wantError = workstate.ErrStopped
					barrier.before = func() { f.client.Job.UpdateOneID(f.jobID).SetStatus("cancelled").ExecX(ctx) }
				case "old_epoch":
					wantError = workstate.ErrStopped
					barrier.before = func() { f.client.Job.UpdateOneID(f.jobID).AddRetryEpoch(1).ExecX(ctx) }
				case "edit":
					wantTarget, wantVersion = "manual edit", before+1
					barrier.before = func() { f.client.Segment.UpdateOneID(ids[0]).SetTargetText("manual edit").ExecX(ctx) }
				case "commit_before_projection":
					wantError = errors.New("interrupted after durable local commit")
					wantTarget, wantVersion, wantIssues, wantCompleted = "「对话」", before+1, 1, 1
					barrier.after = wantError
				}
				return barrier
			})
			if !errors.Is(err, wantError) || doc.Segments[0].Target != "对话" || doc.Segments[0].RoundWork.Completed || len(result.Resolved) != 0 {
				t.Fatalf("local correction = %+v, err=%v, document=%+v", result, err, doc.Segments[0])
			}
			assertCorrectRow(t, f, ids[0], wantTarget, wantVersion, wantIssues)
			row := f.client.JobRound.GetX(ctx, f.roundIDs[0])
			if row.SegmentCompleted != wantCompleted || f.client.JobRoundSegment.Query().CountX(ctx) != wantCompleted {
				t.Fatalf("round after execution fence = %+v", row)
			}
			if scenario == "pause" || scenario == "commit_before_projection" {
				f.client.Job.UpdateOneID(f.jobID).SetStatus("running").SetPauseRequested(false).ExecX(ctx)
				_, result, err := executeCorrectFixture(t, f, nil)
				if err != nil || len(result.Resolved) != 1 {
					t.Fatalf("correct recovery = %+v, err=%v", result, err)
				}
				assertCorrectRow(t, f, ids[0], "「对话」", before+1, 1)
				if count := f.client.JobRoundSegment.Query().CountX(ctx); count != 1 {
					t.Fatalf("correct recovery created %d completion facts", count)
				}
			}
			if scenario == "edit" {
				if row := f.client.WorkItem.Query().Where(workitem.SegmentIDEQ(ids[0])).OnlyX(ctx); row.State != "stale" || len(result.Unresolved) != 1 {
					t.Fatalf("version conflict = %+v, result=%+v", row, result)
				}
			}
			assertCorrectNoModelState(t, f)
		})
	}
}

func TestCorrectRoundSealExecutionFences(t *testing.T) {
	for _, scenario := range []string{"pause", "cancel_after_pause", "paused_new_epoch"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f, ids := correctFixture(t, 1)
			before := f.client.Segment.GetX(ctx, ids[0]).ContentVersion
			var wantError error
			if scenario != "pause" {
				wantError = workstate.ErrStopped
			}
			_, result, err := executeCorrectFixture(t, f, func(store *roundStore) pipeline.RoundStore {
				return &correctCommitBarrier{roundStore: store, beforeSeal: func() {
					update := f.client.Job.UpdateOneID(f.jobID).SetStatus("pausing").SetPauseRequested(true)
					switch scenario {
					case "cancel_after_pause":
						update.SetStatus("cancelled")
					case "paused_new_epoch":
						update.AddRetryEpoch(1)
					}
					update.ExecX(ctx)
				}}
			})
			if !errors.Is(err, wantError) || len(result.Resolved) != 0 {
				t.Fatalf("paused or stopped seal = %+v, err=%v", result, err)
			}
			row := f.client.JobRound.GetX(ctx, f.roundIDs[0])
			if row.ManifestSealed || row.SegmentCompleted != 0 || f.client.WorkItem.Query().CountX(ctx) != 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
				t.Fatalf("paused or stopped seal persisted work: %+v", row)
			}
			assertCorrectRow(t, f, ids[0], "对话", before, 2)
			if scenario == "pause" {
				f.client.Job.UpdateOneID(f.jobID).SetStatus("running").SetPauseRequested(false).ExecX(ctx)
				_, result, err = executeCorrectFixture(t, f, nil)
				if err != nil || len(result.Resolved) != 1 || len(result.Unresolved) != 0 {
					t.Fatalf("correct after resume = %+v, err=%v", result, err)
				}
				assertCorrectRow(t, f, ids[0], "「对话」", before+1, 1)
			}
			assertCorrectNoModelState(t, f)
		})
	}
}

func TestCorrectRoundNoopConfirmsWithoutChangingContent(t *testing.T) {
	ctx := t.Context()
	f, ids := correctFixture(t, 1)
	before := f.client.Segment.UpdateOneID(ids[0]).SetSourceText("前缀 「（对话）」 后缀").SaveX(ctx)
	generation := f.client.Resource.GetX(ctx, f.resourceIDs[0]).TranslationGeneration
	doc, result, err := executeCorrectFixture(t, f, nil)
	if err != nil || len(result.Resolved) != 1 || !doc.Segments[0].RoundWork.Completed || doc.Segments[0].ContentVersion != before.ContentVersion {
		t.Fatalf("correct no-op = %+v, err=%v, segment=%+v", result, err, doc.Segments[0])
	}
	assertCorrectRow(t, f, ids[0], "对话", before.ContentVersion, 2)
	if after := f.client.Segment.GetX(ctx, ids[0]); !reflect.DeepEqual(after.QualityIssues, before.QualityIssues) {
		t.Fatalf("no-op issues = %+v, want %+v", after.QualityIssues, before.QualityIssues)
	}
	if row := f.client.Resource.GetX(ctx, f.resourceIDs[0]); row.TranslationGeneration != generation {
		t.Fatalf("no-op changed resource translation generation from %d to %d", generation, row.TranslationGeneration)
	}
	if fact := f.client.JobRoundSegment.Query().OnlyX(ctx); fact.Outcome != string(workstate.ConfirmedNoop) || fact.CommitID == nil {
		t.Fatalf("no-op completion fact = %+v", fact)
	}
	if round := f.client.JobRound.GetX(ctx, f.roundIDs[0]); round.SegmentCompleted != 1 {
		t.Fatalf("no-op completion count = %d", round.SegmentCompleted)
	}
	assertCorrectNoModelState(t, f)
}
