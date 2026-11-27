package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type extractRecoveryStore struct {
	*roundStore
	afterCapture, afterSettlement error
}

func (s *extractRecoveryStore) CaptureResponse(ctx context.Context, response pipeline.CapturedResponse) (pipeline.MainResponseHeader, error) {
	header, err := s.roundStore.CaptureResponse(ctx, response)
	if err == nil && s.afterCapture != nil {
		err = permanentStoreError{s.afterCapture}
	}
	return header, err
}

func (s *extractRecoveryStore) SettleStoredResponse(ctx context.Context, doc *pipeline.Document, settlement pipeline.StoredResponseSettlement) error {
	if err := s.roundStore.SettleStoredResponse(ctx, doc, settlement); err != nil {
		return err
	}
	if s.afterSettlement != nil {
		return permanentStoreError{s.afterSettlement}
	}
	return nil
}

func TestCapturedExtractReplaysAfterCaptureAndGlossarySettlementInterruptions(t *testing.T) {
	ctx := t.Context()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		const response = `{"glossary":[{"source":"new term","target":"译文","notes":" note "},{"source":"NEW TERM","target":"译文"},{"source":"existing term","target":"不同"},{"source":"X","target":"短"}]}`
		if err := json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": response}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
		}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(upstream.Close)
	driver := pa.Driver()
	var client *ent.Client
	if driver == "postgres" {
		client, _, _ = pa.Open(t)
	} else {
		client = newRegistryTestClient(t)
	}
	f := newRubyPipelineFixtureWithDriver(t, client, driver, upstream.URL, 2)
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.snapshot.GlossaryEnabled = true
	f.snapshot.Rounds = []service.JobRoundSnapshot{{
		Mode: "extract", Backend: f.snapshot.Rounds[0].Backend,
		Extract: &service.JobExtractRoundSnapshot{TemplateContent: templates.EmbeddedBootstrapTemplate(), BatchSize: 2, Concurrency: 1, MinSourceLen: 2},
	}}
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	f.client.JobRound.UpdateOneID(f.roundIDs[0]).SetMode("extract").ExecX(ctx)
	j := f.client.Job.GetX(ctx, f.jobID)
	project := j.QueryProject().OnlyX(ctx)
	f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("existing term").SetSource("existing term").SetTarget("原有").SaveX(ctx)

	run := func(afterCapture, afterSettlement error) (pipeline.RunRoundResult, error) {
		t.Helper()
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		runCtx = service.WithExecutionJobActor(runCtx, f.ownerID, f.jobID)
		runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		var preloaded *workstate.ResponseHeader
		var preloadedHandoff int64
		if afterSettlement != nil {
			headers, _, err := workstate.NewStore(f.client).LoadResponseHeaders(runCtx, f.jobID, 0, 1)
			if err != nil || len(headers) != 1 {
				t.Fatalf("load preexisting response inventory: %+v, %v", headers, err)
			}
			header := headers[0]
			facts := header.Facts()
			handoff := facts.InputMemberBytes + facts.CaptureBytes + facts.OutputMemberBytes + backend.RequestMetadataBytes
			if err := runtime.Admission.RestoreInventory(int64(header.MemberCount-header.Cursor), 0, 0, handoff); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Admission.RestoreRootMetadata(header.MetadataBytes); err != nil {
				t.Fatal(err)
			}
			preloaded = &header
			preloadedHandoff = handoff
		}
		root, err := resourcebudget.New(resourcebudget.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		scope := resourcebudget.Scope{ActorID: f.ownerID, JobID: f.jobID}
		if h := preloaded; h != nil {
			source := resourcebudget.SourceFunc(func(_ context.Context, emit func(resourcebudget.Record) error) error {
				return emit(resourcebudget.Record{
					Scope: scope, Identity: fmt.Sprintf("job:%d:runtime", f.jobID),
					Usage: resourcebudget.Vector{UnconfirmedMembers: int64(h.MemberCount - h.Cursor), PayloadBytes: preloadedHandoff + h.MetadataBytes},
				})
			})
			if err := root.Rebuild(runCtx, source); err != nil {
				t.Fatal(err)
			}
			if err := root.RebuildActor(runCtx, scope.ActorID, source); err != nil {
				t.Fatal(err)
			}
		}
		if err := runtime.Admission.AttachRootBudget(root, scope); err != nil {
			t.Fatal(err)
		}
		if h := preloaded; h != nil {
			// JobRunner reattaches the saved business owner before entering its
			// resource, so its window entry has no consumer yet.
			if err := runtime.Admission.RestoreRequestMetadata(h.RequestID, h.MemberCount, h.MetadataBytes); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Admission.RestoreResponseOwnership(h.RequestID, 0, h.Scope.ResourceID, h.Facts(), h.Phase, false); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Window.RestoreRaw(h.RequestID, h.MemberCount-h.Cursor, h.Scope.ResourceID, h.Scope.RoundID, nil); err != nil {
				t.Fatal(err)
			}
		}
		runCtx = service.WithExecutionInputBudget(runCtx, root, scope, service.DefaultExecutionInputBytes)
		dictionary, lease, err := service.LoadExecutionGlossary(runCtx, f.client, project, true)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(runCtx, runtime), f.snapshot, engine.RuntimeResources{Glossary: dictionary}, progress.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		doc, durable := f.round(t, 0)
		durable.driver = driver
		eng.PrepareDocument(doc, nil)
		defer runtime.CloseDocument(doc)
		round := eng.Rounds()[0]
		round.Store = &extractRecoveryStore{roundStore: durable, afterCapture: afterCapture, afterSettlement: afterSettlement}
		round.Runtime = runtime
		return pipeline.RunRound(runCtx, round, doc, nil, f.logger, progress.Nop{})
	}
	request := func() *ent.WorkRequest {
		return f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(ctx)
	}
	checkPending := func() {
		t.Helper()
		row := request()
		if row.PromotionState != "capture_pending" || row.CaptureBytes == 0 || len(row.GlossaryReceipt) != 0 || row.UsageRecordID == nil {
			t.Fatalf("capture is not recoverable: %+v", row)
		}
		if calls.Load() != 1 || f.client.GlossaryEntry.Query().CountX(ctx) != 1 || f.client.JobRoundSegment.Query().CountX(ctx) != 0 {
			t.Fatal("extract changed provider usage, dictionary, or progress before settlement")
		}
	}
	afterCapture := errors.New("interrupted after capture")
	if _, err := run(afterCapture, nil); !errors.Is(err, afterCapture) {
		t.Fatalf("capture interruption: %v", err)
	}
	checkPending()
	id := request().Identity
	afterSettlement := errors.New("interrupted after glossary and work writes, before commit")
	if _, err := run(nil, afterSettlement); !errors.Is(err, afterSettlement) {
		t.Fatalf("settlement interruption: %v", err)
	}
	checkPending()

	var lostACK atomic.Bool
	f.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if _, ok := mutation.Field(workrequest.FieldGlossaryReceipt); ok && lostACK.CompareAndSwap(false, true) {
				tx, err := mutation.(*ent.WorkRequestMutation).Tx()
				if err != nil {
					return nil, err
				}
				tx.OnCommit(func(next ent.Committer) ent.Committer {
					return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
						if err := next.Commit(ctx, tx); err != nil {
							return err
						}
						return errors.New("glossary settlement committed but acknowledgement was lost")
					})
				})
			}
			return next.Mutate(ctx, mutation)
		})
	})
	for range 2 {
		result, err := run(nil, nil)
		if err != nil || len(result.Resolved) != 2 {
			t.Fatalf("extract recovery: %+v, err=%v", result, err)
		}
	}
	row := request()
	var receipt glossary.ResponseReceipt
	if err := json.Unmarshal(row.GlossaryReceipt, &receipt); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(canonical)); row.PublicDigest != want {
		t.Fatalf("receipt digest = %s, want %s", row.PublicDigest, want)
	}
	if !lostACK.Load() || calls.Load() != 1 || row.Identity != id || row.PromotionState != "promoted" || row.CaptureBytes != 0 || receipt.RequestID != id || receipt.InputDigest != row.ResponseDigest || receipt.Added != 1 || receipt.Skipped != 1 {
		t.Fatalf("response settled incorrectly: calls=%d request=%+v receipt=%+v", calls.Load(), row, receipt)
	}
	if got := f.client.JobRound.GetX(ctx, f.roundIDs[0]).SegmentCompleted; got != 2 || f.client.JobRoundSegment.Query().CountX(ctx) != 2 {
		t.Fatalf("round completed %d members", got)
	}
	if got := f.client.Job.GetX(ctx, f.jobID).ProgressCompleted; got != 2 {
		t.Fatalf("job completed %d members", got)
	}
	newTerm := f.client.GlossaryEntry.Query().Where(glossaryentry.SourceKeyEQ("new term")).OnlyX(ctx)
	existing := f.client.GlossaryEntry.Query().Where(glossaryentry.SourceKeyEQ("existing term")).OnlyX(ctx)
	if newTerm.Target != "译文" || newTerm.Notes != "note" || newTerm.Mandatory || existing.Target != "原有" || f.client.GlossaryEntry.Query().CountX(ctx) != 2 {
		t.Fatalf("glossary merge changed terms: new=%+v existing=%+v", newTerm, existing)
	}
	f.assertUsage(t, 1)
}
