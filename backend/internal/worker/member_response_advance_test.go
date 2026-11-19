package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type memberAdvanceSQLStore struct {
	*roundStore
	checkpoints   []pipeline.MainResponseHeader
	businessFault error
}

func (s *memberAdvanceSQLStore) PublishOutputs(ctx context.Context, in pipeline.OutputPublication) (pipeline.MainResponseHeader, error) {
	h, e := s.roundStore.PublishOutputs(ctx, in)
	if e == nil {
		s.checkpoints = append(s.checkpoints, h)
	}
	return h, e
}
func (s *memberAdvanceSQLStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) (pipeline.MainResponseHeader, error) {
	h, e := s.roundStore.PublishBusiness(ctx, id, out, install)
	if e == nil && s.businessFault != nil {
		return h, permanentStoreError{s.businessFault}
	}
	return h, e
}

func TestResponseAdvanceDeferredPreservesPublishedHeader(t *testing.T) {
	for _, mode := range []string{"business_lane", "rollback_after_publish"} {
		t.Run("sqlite/"+mode, func(t *testing.T) { testResponseAdvanceSQL(t, mode) })
	}
}

func TestMemberSQLPostCommitCacheFailurePreservesConfirmedHeader(t *testing.T) {
	testResponseAdvanceSQL(t, "postcommit_cache_failure")
}

func TestMemberResponseHeaderAdapterScope(t *testing.T) {
	for _, mode := range []string{"scope_drift", "job_scope_zero"} {
		t.Run(mode, func(t *testing.T) {
			f := newRubyPipelineFixture(t, 1, 1)
			ctx := t.Context()
			_, adapter := f.round(t, 0)
			row := f.client.WorkRequest.Create().SetIdentity("header-scope").SetJobID(f.jobID).
				SetResourceID(f.resourceIDs[0]).SetJobRoundID(f.roundIDs[0]).SetRetryEpoch(adapter.scope.RetryEpoch).
				SetSegmentIds([]int{}).SetStage("main").SetBackendID(0).SetBudgetModel("stage_separated").SetInputDigest("input").
				SetState("completed").SetResponseFormatVersion(execution.ResponseFormatVersion).SetSnapshotDigest(adapter.digest).
				SetResponseDigest("capture").SetPromotionState("business_ready").SaveX(ctx)
			confirmed, err := adapter.ResponseHeader(ctx, row.Identity)
			if err != nil || confirmed.ID != row.Identity || confirmed.Phase != responsecontract.BusinessReady {
				t.Fatalf("original SQL header unavailable: %v", err)
			}
			if mode == "job_scope_zero" {
				jobScope := *adapter
				jobScope.scope.ResourceID, jobScope.scope.RoundID, jobScope.scope.JobResourceID = 0, 0, 0
				actual, err := jobScope.ResponseHeader(ctx, row.Identity)
				if err != nil || actual != confirmed || actual.ResourceID == 0 || actual.RoundID == 0 {
					t.Fatalf("job adapter rejected its actual scoped SQL header: %v", err)
				}
				return
			}
			job := f.client.Job.GetX(ctx, f.jobID)
			other := f.client.Job.Create().SetProjectID(job.ProjectID).SetExecutionPlanID(job.ExecutionPlanID).SaveX(ctx)
			for _, change := range []struct {
				column string
				bad    any
				good   any
			}{
				{"job_id", other.ID, f.jobID},
				{"resource_id", f.resourceIDs[1], f.resourceIDs[0]},
				{"job_round_id", f.roundIDs[1], f.roundIDs[0]},
				{"snapshot_digest", "foreign-snapshot", adapter.digest},
			} {
				// All mutated IDs name real FK rows; only the authority changes.
				query := "UPDATE work_requests SET " + change.column + "=? WHERE identity=?"
				if _, err := f.client.ExecContext(ctx, query, change.bad, row.Identity); err != nil {
					t.Fatal(err)
				}
				actual, err := adapter.ResponseHeader(ctx, row.Identity)
				if !errors.Is(err, workstate.ErrManifest) || actual != (pipeline.MainResponseHeader{}) {
					t.Fatalf("changed %s escaped adapter guard: zero=%t err=%v", change.column, actual == (pipeline.MainResponseHeader{}), err)
				}
				if _, err := f.client.ExecContext(ctx, query, change.good, row.Identity); err != nil {
					t.Fatal(err)
				}
				actual, err = adapter.ResponseHeader(ctx, row.Identity)
				if err != nil || actual != confirmed {
					t.Fatalf("restored %s changed confirmed SQL header: %v", change.column, err)
				}
			}
		})
	}
}

func testResponseAdvanceSQL(t *testing.T, mode string) {
	t.Helper()
	f, http := newHandoffHTTPFixture(t, "translate", 1, 17, 17)
	http.open()
	inline := execution.DefaultInlineTermExtraction()
	inline.Enabled = true
	f.snapshot.Rounds[0].Translate.InlineTermExtraction = &inline
	f.snapshot.GlossaryEnabled = true
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	project := f.client.Job.QueryProject(f.client.Job.GetX(t.Context(), f.jobID)).OnlyX(t.Context())
	dictionary, e := service.NewDatabaseGlossary(service.WithExecutionJobActor(t.Context(), f.ownerID, f.jobID), f.client, project)
	if e != nil {
		t.Fatal(e)
	}
	gate := pipeline.NewPauseGate()
	r, e := NewExecutionRuntime(f.snapshot, f.pool, gate, pipeline.DefaultCandidateLimits())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	executionCtx := service.WithExecutionJobActor(t.Context(), f.ownerID, f.jobID)
	var selectedGlossary glossary.Glossary = dictionary
	var lease *service.ExecutionInputLease
	if mode == "postcommit_cache_failure" {
		root, err := resourcebudget.New(resourcebudget.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		rootScope := resourcebudget.Scope{ActorID: f.ownerID, JobID: f.jobID}
		if err = r.Admission.AttachRootBudget(root, rootScope); err != nil {
			t.Fatal(err)
		}
		executionCtx = service.WithExecutionInputBudget(executionCtx, root, rootScope, service.DefaultExecutionInputBytes)
		selectedGlossary, lease, e = service.LoadExecutionGlossary(executionCtx, f.client, project, true)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(lease.Close)
		http.transform = func(content any) any {
			result := content.(map[string]any)
			result["glossary"] = []map[string]string{{"source": "term", "target": "译文"}}
			return result
		}
	}
	eng, e := f.factory.BuildEngine(pipeline.WithExecutionRuntime(executionCtx, r), f.snapshot, engine.RuntimeResources{Glossary: selectedGlossary}, progress.Nop{})
	if e != nil {
		t.Fatal(e)
	}
	defer eng.Close()
	doc, base := f.round(t, 0)
	eng.PrepareDocument(doc, nil)
	defer r.CloseDocument(doc)
	seed := &handoffPauseBoundaryStore{roundStore: base, phase: "raw_after", pause: func(ctx context.Context) error {
		_, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID)
		if err == nil {
			gate.Pause()
		}
		return err
	}}
	_, e = pipeline.RunStagedRound(t.Context(), eng.Rounds()[0], doc, seed, r, f.logger, progress.Nop{})
	if e != nil || !gate.Paused() {
		t.Fatalf("reliable capture barrier: %v", e)
	}
	headers, _, e := base.ResponseHeaders(t.Context(), 0, 16)
	if e != nil || len(headers) != 1 || headers[0].Phase != responsecontract.CapturePending {
		t.Fatalf("paid capture missing: %+v %v", headers, e)
	}
	header := headers[0]
	s := &memberAdvanceSQLStore{roundStore: base}
	fault := errors.New("rollback after SQL business publisher")
	if mode == "business_lane" {
		if e = r.Admission.RestoreResponseOwnership("other", 0, base.scope.ResourceID, responsecontract.ResponseFacts{PublicKind: responsecontract.PublicNone, BusinessReservation: 4096}, responsecontract.OutputsReady, false); e != nil {
			t.Fatal(e)
		}
		if ok, e := r.Admission.TryBusinessOwner("other"); e != nil || !ok {
			t.Fatalf("other lane=%t %v", ok, e)
		}
	} else if mode == "rollback_after_publish" {
		s.businessFault = fault
	}
	var cacheOwner string
	var cacheBytes int64
	if mode == "postcommit_cache_failure" {
		f.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
				if _, ok := m.Field("glossary_receipt"); ok {
					tx, err := m.(*ent.WorkRequestMutation).Tx()
					if err != nil {
						return nil, err
					}
					tx.OnCommit(func(next ent.Committer) ent.Committer {
						return ent.CommitFunc(func(ctx context.Context, tx *ent.Tx) error {
							if err := next.Commit(ctx, tx); err != nil {
								return err
							}
							for owner, bytes := range r.Admission.BuildAllocationSnapshot() {
								if strings.HasPrefix(owner, "glossary-cache:") && bytes > 0 {
									cacheOwner, cacheBytes = owner, bytes
									return r.Admission.MoveBuildBytes(owner, "postcommit-cache-drift", bytes)
								}
							}
							return errors.New("committed dictionary has no actual cache owner")
						})
					})
				}
				return next.Mutate(ctx, m)
			})
		})
	}
	handler := eng.Rounds()[0].Handler.(*pipeline.TranslateHandler)
	out, e := pipeline.AdvanceResponse(pipeline.WithExecutionRuntime(executionCtx, r), eng.Rounds()[0], handler, header, s, r, f.logger)
	confirmed, err := base.ResponseHeader(t.Context(), header.ID)
	wantedPhase := responsecontract.OutputsReady
	if mode == "postcommit_cache_failure" {
		wantedPhase = responsecontract.BusinessReady
	}
	if err != nil || out.Header != confirmed || confirmed.Phase != wantedPhase || confirmed.ArchiveCursor != 17 || confirmed.ModelOutputBytes <= 0 || confirmed.CaptureBytes != 0 || len(s.checkpoints) != 2 || s.checkpoints[0].ArchiveCursor != 16 {
		t.Fatalf("latest authority/header/checkpoint mismatch: got=%+v actual=%+v err=%v/%v", out.Header, confirmed, e, err)
	}
	if mode == "business_lane" {
		if e != nil || out.Kind != pipeline.ResponseDeferred || out.Wait.Reason != "business_lane" {
			t.Fatalf("Deferred entered system error: %+v %v", out, e)
		}
		changed := out.Wait.CapacityChanged
		r.Admission.ReleaseDurableResponse("other")
		select {
		case <-changed:
		default:
			t.Fatal("lane release did not wake")
		}
		resumed, e := pipeline.AdvanceResponse(t.Context(), eng.Rounds()[0], handler, out.Header, s, r, f.logger)
		if e != nil || resumed.Header.Phase != responsecontract.BusinessReady || resumed.Header.Cursor != 1 || len(s.checkpoints) != 2 {
			t.Fatalf("resume replayed paid capture/archive: %+v %v", resumed, e)
		}
	} else if mode == "rollback_after_publish" {
		if !errors.Is(e, fault) {
			t.Fatalf("rollback became local/deferred: %v", e)
		}
		row := f.client.WorkRequest.Query().OnlyX(t.Context())
		if len(row.GlossaryReceipt) != 0 || row.PromotionCursor != 0 {
			t.Fatal("transaction provisional business fact escaped rollback")
		}
	} else {
		if !errors.Is(e, backend.ErrBuildCapacity) || cacheOwner == "" || cacheBytes <= 0 || r.Admission.BuildBytes("postcommit-cache-drift") != cacheBytes || confirmed.Cursor != 0 || f.client.GlossaryEntry.Query().CountX(t.Context()) != 1 {
			t.Fatalf("SQL postcommit cache failure changed confirmed facts: phase=%s cursor=%d cache_bytes=%d err=%v", confirmed.Phase, confirmed.Cursor, cacheBytes, e)
		}
		if err := r.Admission.MoveBuildBytes("postcommit-cache-drift", cacheOwner, cacheBytes); err != nil {
			t.Fatal(err)
		}
		replayCtx := r.BuildContext(pipeline.WithExecutionRuntime(executionCtx, r), "postcommit-cache-replay")
		business, err := selectedGlossary.(glossary.ResponseProcessor).ProcessResponse(replayCtx, glossary.ResponseProcess{RequestID: header.ID, CaptureEpoch: header.CaptureEpoch, InputDigest: header.ResponseDigest})
		if err != nil {
			t.Fatal("committed cache did not reconcile", err)
		}
		business.Release()
		if err := r.Admission.ReleaseBuild("postcommit-cache-replay"); err != nil {
			t.Fatal(err)
		}
		if r.Admission.BuildBytes(cacheOwner) != 0 || r.Admission.BuildBytes("postcommit-cache-drift") != 0 {
			t.Fatal("committed cache retry retained its original transfer owner")
		}
		lease.Close()
	}
	http.mu.Lock()
	calls := http.calls
	http.mu.Unlock()
	if calls != 1 || f.client.UsageRecord.Query().CountX(t.Context()) != 1 {
		t.Fatalf("paid response repeated: calls=%d", calls)
	}
	for _, row := range f.client.WorkItem.Query().AllX(t.Context()) {
		if row.MainAttempts != 1 {
			t.Fatal("response attempt repeated")
		}
	}
}
