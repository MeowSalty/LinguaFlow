package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

type interruptedQualitySettlement struct {
	*roundStore
	err error
}

func TestCapturedQualityRecoveryRetriesInSingleMemberWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		finish := "stop"
		if calls.Add(1) == 1 {
			finish = "length"
		}
		const response = `{"issues":[]}`
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": finish}},
		}); err != nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), upstream.URL, 1)
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot.Rounds = []service.JobRoundSnapshot{{
		Mode: pipeline.RoundModeSemanticQA, Backend: f.snapshot.Rounds[0].Backend,
		SemanticQA: &service.JobSemanticQARoundSnapshot{
			TemplateContent: templates.EmbeddedSemanticQATemplate(), BatchSize: 1, Concurrency: 1,
			SegmentScope: "all", Retry: execution.RetryConfig{MaxAttempts: 1},
		},
	}}
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetMode(pipeline.RoundModeSemanticQA).ExecX(ctx)
	f.client.Segment.Update().SetTargetText("target").SetStatus(segment.StatusTranslated).SaveX(ctx)
	limits := pipeline.DefaultCandidateLimits()
	limits.Segments = 1
	runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(ctx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	doc, store := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	defer runtime.CloseDocument(doc)
	round := eng.Rounds()[0]
	round.Store, round.Runtime = store, runtime
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	result, err := pipeline.RunRound(ctx, round, doc, nil, logger, progress.Nop{})
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0}) || calls.Load() != 2 {
		row := f.client.WorkItem.Query().OnlyX(t.Context())
		t.Fatalf("retry failed in the one-member window: result=%+v calls=%d reason=%q err=%v\n%s", result, calls.Load(), row.LastError, err, logs.String())
	}
	if remaining := f.client.WorkRequest.Query().Where(workrequest.PromotionStateNEQ("promoted")).CountX(ctx); remaining != 0 || runtime.Window.Snapshot().Segments != 0 {
		t.Fatalf("retry left an unfinished request or window slot: requests=%d window=%+v", remaining, runtime.Window.Snapshot())
	}
}

func (s *interruptedQualitySettlement) SettleStoredResponse(context.Context, *pipeline.Document, pipeline.StoredResponseSettlement) error {
	return permanentStoreError{s.err}
}

type qualityBuildPressure struct {
	*roundStore
	root     *resourcebudget.RootBudget
	reads    int
	released bool
}

func (s *qualityBuildPressure) LoadCapture(ctx context.Context, id string, limit int64) (pipeline.CapturedResponse, error) {
	capture, err := s.roundStore.LoadCapture(ctx, id, limit)
	if err != nil {
		return capture, err
	}
	s.reads++
	if s.reads == 1 {
		holder := resourcebudget.Scope{ActorID: 1, CallID: "occupied-quality-parser"}
		pressure := resourcebudget.Vector{BuildWorkers: 1}
		if err := s.root.Claim(holder, holder.CallID, pressure); err != nil {
			capture.Release()
			return pipeline.CapturedResponse{}, err
		}
		release := capture.Release
		// The captured response is loaded, and the next build is the handler's
		// parser. Retiring this first read wakes the same capture's waiter.
		capture.Release = func() {
			release()
			s.released = s.root.Release(holder, holder.CallID, pressure) == nil
		}
	}
	return capture, nil
}

func TestCapturedQualityRecoveryKeepsNewerSegmentAndDoesNotRepeatProvider(t *testing.T) {
	for _, mode := range []string{pipeline.RoundModeSemanticQA, pipeline.RoundModeAdjudicate} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			response := `{"issues":[{"id":"0","code":"calque","message":"中文 \"quoted\" issue","snippet":"original"},{"id":"1","code":"calque","message":"stale issue"}]}`
			if mode == pipeline.RoundModeAdjudicate {
				response = `{"verdicts":[{"id":"0","issue_code":"source_residual","verdict":"false_positive","reason":"proper noun"},{"id":"1","issue_code":"source_residual","verdict":"false_positive","reason":"stale decision"}]}`
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{
					"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}},
					"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
				}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(upstream.Close)
			f := newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), upstream.URL, 3)
			f.snapshot.Strategy.Ruby.Enabled = false
			f.snapshot.RubyRetry = nil
			spec := service.JobRoundSnapshot{Mode: mode, Backend: f.snapshot.Rounds[0].Backend}
			if mode == pipeline.RoundModeSemanticQA {
				spec.SemanticQA = &service.JobSemanticQARoundSnapshot{TemplateContent: templates.EmbeddedSemanticQATemplate(), Concurrency: 1, BatchSize: 3, SegmentScope: "all"}
			} else {
				spec.Adjudicate = &service.JobAdjudicateRoundSnapshot{TemplateContent: templates.EmbeddedAdjudicationTemplate(), Concurrency: 1, BatchSize: 3, AdjudicateCodes: []string{"source_residual"}}
			}
			f.snapshot.Rounds = []service.JobRoundSnapshot{spec}
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetMode(mode).ExecX(ctx)
			originalIssue := qa.QualityIssue{Code: "source_residual", Message: "original issue", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending}
			rows := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).Order(segment.BySegmentIndex()).AllX(ctx)
			for i, row := range rows {
				issue := originalIssue
				if i == 2 && mode == pipeline.RoundModeSemanticQA {
					issue.Code = "calque"
				}
				rows[i] = f.client.Segment.UpdateOneID(row.ID).SetTargetText("original target").SetStatus(segment.StatusTranslated).SetQualityIssues([]qa.QualityIssue{issue}).SaveX(ctx)
			}
			run := func(interrupted error, pressure *qualityBuildPressure) (pipeline.RunRoundResult, error) {
				t.Helper()
				runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Close()
				eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(runCtx, runtime), f.snapshot, engine.RuntimeResources{}, progress.Nop{})
				if err != nil {
					t.Fatal(err)
				}
				defer eng.Close()
				doc, store := f.round(t, 0)
				eng.PrepareDocument(doc, nil)
				defer runtime.CloseDocument(doc)
				round := eng.Rounds()[0]
				round.Store, round.Runtime = store, runtime
				if interrupted != nil {
					round.Store = &interruptedQualitySettlement{roundStore: store, err: interrupted}
				}
				if pressure != nil {
					if err := runtime.Admission.AttachRootBudget(pressure.root, resourcebudget.Scope{ActorID: f.ownerID, JobID: f.jobID}); err != nil {
						t.Fatal(err)
					}
					pressure.roundStore = store
					round.Store = pressure
				}
				return pipeline.RunRound(runCtx, round, doc, nil, f.logger, progress.Nop{})
			}
			interrupted := errors.New("interrupted before captured quality settlement")
			if _, err := run(interrupted, nil); !errors.Is(err, interrupted) {
				t.Fatalf("expected interrupted settlement, got %v", err)
			}
			pending := f.client.WorkRequest.Query().OnlyX(ctx)
			if calls.Load() != 1 || pending.PromotionState != "capture_pending" || pending.CaptureBytes == 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
				t.Fatalf("interrupted round did not retain an unapplied capture: calls=%d phase=%s bytes=%d", calls.Load(), pending.PromotionState, pending.CaptureBytes)
			}
			newerIssue := originalIssue
			newerIssue.Message = "newer issue with same match key"
			edit := f.client.Segment.UpdateOneID(rows[1].ID).SetQualityIssues([]qa.QualityIssue{newerIssue})
			if mode == pipeline.RoundModeSemanticQA {
				edit.SetTargetText("replacement target")
			}
			newer := edit.SaveX(ctx)
			root, err := resourcebudget.New(resourcebudget.Policy{Instance: resourcebudget.Limits{MaxBuildWorkers: 1}})
			if err != nil {
				t.Fatal(err)
			}
			pressure := &qualityBuildPressure{root: root}
			for _, fault := range []*qualityBuildPressure{pressure, nil} {
				result, err := run(nil, fault)
				if err != nil || !reflect.DeepEqual(result.Resolved, []int{0, 2}) || !reflect.DeepEqual(result.Unresolved, []int{1}) {
					t.Fatalf("unexpected recovery result: %+v err=%v", result, err)
				}
			}
			if pressure.reads != 2 || !pressure.released {
				t.Fatalf("expected one deferred and one successful read of the retained capture, reads=%d released=%t", pressure.reads, pressure.released)
			}
			settled := f.client.WorkRequest.GetX(ctx, pending.ID)
			if calls.Load() != 1 || settled.PromotionState != "promoted" || settled.CaptureBytes != 0 || settled.InputMemberBytes != 0 || f.client.JobRoundSegment.Query().CountX(ctx) != 2 || f.client.JobRound.GetX(ctx, f.roundIDs[0]).SegmentCompleted != 2 {
				t.Fatalf("capture did not settle exactly once: calls=%d phase=%s checkpoint count=%d", calls.Load(), settled.PromotionState, f.client.JobRoundSegment.Query().CountX(ctx))
			}
			first := f.client.Segment.GetX(ctx, rows[0].ID)
			if first.ContentVersion != rows[0].ContentVersion+1 || first.Status != rows[0].Status || !reflect.DeepEqual(first.TargetText, rows[0].TargetText) {
				t.Fatalf("captured response overwrote content or applied more than once: %+v", first)
			}
			if mode == pipeline.RoundModeSemanticQA {
				if len(first.QualityIssues) != 2 || first.QualityIssues[1].Message != `中文 "quoted" issue` || qa.MatchedText(first.QualityIssues[1]) != "original" {
					t.Fatalf("semantic QA did not persist the captured issue and span: %+v", first.QualityIssues)
				}
			} else if len(first.QualityIssues) != 1 || !first.QualityIssues[0].Dismissed() || first.QualityIssues[0].Note != "proper noun" {
				t.Fatalf("adjudication did not persist the captured false-positive verdict: %+v", first.QualityIssues)
			}
			second := f.client.Segment.GetX(ctx, rows[1].ID)
			if second.ContentVersion != newer.ContentVersion || !reflect.DeepEqual(second.TargetText, newer.TargetText) || !reflect.DeepEqual(second.QualityIssues, []qa.QualityIssue{newerIssue}) {
				t.Fatalf("capture changed the newer member: %+v", second)
			}
			third := f.client.Segment.GetX(ctx, rows[2].ID)
			if third.ContentVersion != rows[2].ContentVersion+1 {
				t.Fatalf("unchanged member with omitted result did not settle exactly once: %+v", third)
			}
			if mode == pipeline.RoundModeSemanticQA {
				if third.QualityIssues == nil || len(third.QualityIssues) != 0 {
					t.Fatalf("successful empty scan did not clear old semantic issues to an empty array: %+v", third.QualityIssues)
				}
			} else if !reflect.DeepEqual(third.QualityIssues, []qa.QualityIssue{originalIssue}) {
				t.Fatalf("missing verdict changed pending issues: %+v", third.QualityIssues)
			}
			f.assertUsage(t, 1)
		})
	}
}
