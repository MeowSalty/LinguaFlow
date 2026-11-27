package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

const terminalProviderMessage = "fixture provider rejected the translation"

func terminalProviderFixture(t *testing.T, status int, recoverOnCall int32) (*rubyPipelineFixture, *atomic.Int32) {
	t.Helper()
	var f *rubyPipelineFixture
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); recoverOnCall > 0 && n >= recoverOnCall {
			f.serveHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": terminalProviderMessage, "type": "fixture_error"}})
	}))
	t.Cleanup(upstream.Close)
	f = newRubyPipelineFixtureWithClient(t, newRegistryTestClient(t), upstream.URL, 1)
	f.snapshot.Strategy.Ruby.Enabled = false
	f.snapshot.RubyRetry = nil
	f.client.Segment.Update().Where(segment.ResourceIDEQ(f.resourceIDs[0])).SetSourceText("one test segment").ExecX(context.Background())
	return f, &calls
}

func TestJobRunnerRetainsTerminalProviderFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		attempts  int
		wantPools []int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, attempts: 2, wantPools: []int{0}},
		{name: "forbidden", status: http.StatusForbidden, attempts: 2, wantPools: []int{0}},
		{name: "nonretryable", status: http.StatusBadRequest, attempts: 2, wantPools: []int{0, 1, 2}},
		{name: "transient_exhaustion", status: http.StatusServiceUnavailable, attempts: 0, wantPools: []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := terminalProviderFixture(t, tc.status, 0)
			f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = tc.attempts
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
				t.Fatal(err)
			}
			assertTerminalProviderFailure(t, ctx, f, tc.status)
			assertFailedProviderRequests(t, ctx, f, tc.status, calls.Load(), tc.wantPools)
		})
	}
}

func TestJobRunnerRecoveredFallbackRetainsOnlyUnresolvedProviderFailure(t *testing.T) {
	for _, takeOver := range []bool{false, true} {
		name := "skipped_successor"
		if takeOver {
			name = "successful_successor"
		}
		t.Run(name, func(t *testing.T) {
			f, calls := terminalProviderFixture(t, http.StatusUnauthorized, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			first := f.snapshot.Rounds[0]
			second := first
			translate := *first.Translate
			second.Translate = &translate
			f.snapshot.Rounds = append(f.snapshot.Rounds, second)
			if !takeOver {
				// Explicit selection reprocesses a translated segment in the first
				// round. A pending-only successor does not select that segment.
				row := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
				f.client.Segment.UpdateOneID(row.ID).SetStatus(segment.StatusTranslated).SetTargetText("existing translation").ExecX(ctx)
				f.client.JobResource.UpdateOneID(f.jobResources[0]).SetSegmentIds([]int{row.ID}).ExecX(ctx)
				f.snapshot.ExplicitSegmentSelection = true
			}
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			secondID := createJobRoundRow(t, f.client, f.jobID, f.jobResources[0], 1, "translate")
			failure := errors.New("injected interruption after closing the predecessor")
			var inject atomic.Bool
			inject.Store(true)
			f.client.JobRound.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					status, _ := mutation.Field("status")
					if status == service.JobRoundStatusRunning && calls.Load() == 1 && inject.CompareAndSwap(true, false) {
						return nil, failure
					}
					return next.Mutate(ctx, mutation)
				})
			})
			// An error while opening a successor makes this resource terminal.
			// Retry uses the service transition and keeps the first closed round.
			if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
				t.Fatal(err)
			}
			firstRow := f.client.JobRound.GetX(ctx, f.roundIDs[0])
			if inject.Load() || firstRow.Status != service.JobRoundStatusCompleted || calls.Load() != 1 {
				t.Fatalf("did not interrupt after the actual failed provider call: round=%s calls=%d", firstRow.Status, calls.Load())
			}
			if _, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID); err != nil {
				t.Fatal(err)
			}
			if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
				t.Fatal(err)
			}
			if takeOver {
				job := f.client.Job.GetX(ctx, f.jobID)
				item := f.client.JobResource.GetX(ctx, f.jobResources[0])
				if calls.Load() != 2 || job.Status != service.JobStatusCompleted || job.ErrorMessage != nil || item.ErrorMessage != nil || item.WarningMessage != nil {
					t.Fatalf("successor did not clear the previous provider failure: calls=%d job=%+v resource=%+v", calls.Load(), job, item)
				}
				if round := f.client.JobRound.GetX(ctx, secondID); round.Status != service.JobRoundStatusCompleted || round.ErrorMessage != nil {
					t.Fatalf("successful successor carried old error: %+v", round)
				}
			} else {
				assertTerminalProviderFailure(t, ctx, f, http.StatusUnauthorized)
				if calls.Load() != 1 || f.client.JobRound.GetX(ctx, secondID).Status != service.JobRoundStatusSkipped {
					t.Fatalf("skipped successor dispatched work: calls=%d", calls.Load())
				}
			}
			assertFailedProviderRequests(t, ctx, f, http.StatusUnauthorized, 1, []int{0})
		})
	}
}

func TestJobRunnerFallbackKeepsUntakenMemberFailure(t *testing.T) {
	f, calls := terminalProviderFixture(t, http.StatusForbidden, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	existing := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).OnlyX(ctx)
	f.client.Segment.UpdateOneID(existing.ID).SetStatus(segment.StatusTranslated).SetTargetText("existing translation").ExecX(ctx)
	pending := f.client.Segment.Create().SetResourceID(f.resourceIDs[0]).SetSegmentIndex(1).SetSourceText("untranslated member").SaveX(ctx)
	f.client.JobResource.UpdateOneID(f.jobResources[0]).SetSegmentCount(2).SetSegmentIds([]int{existing.ID, pending.ID}).ExecX(ctx)
	f.client.Resource.UpdateOneID(f.resourceIDs[0]).SetTotalSegments(2).ExecX(ctx)
	f.snapshot.ExplicitSegmentSelection = true
	f.snapshot.Rounds[0].Translate.BatchSize = 1
	second := f.snapshot.Rounds[0]
	translate := *second.Translate
	second.Translate = &translate
	f.snapshot.Rounds = append(f.snapshot.Rounds, second)
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	secondID := createJobRoundRow(t, f.client, f.jobID, f.jobResources[0], 1, "translate")
	if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	assertTerminalProviderFailure(t, ctx, f, http.StatusForbidden)
	if row := f.client.JobRound.GetX(ctx, f.roundIDs[0]); row.ErrorMessage == nil || !strings.Contains(*row.ErrorMessage, "1 个未解决段") {
		t.Fatalf("predecessor did not keep exactly one untouched failed member: %+v", row)
	}
	if row := f.client.JobRound.GetX(ctx, secondID); row.Status != service.JobRoundStatusCompleted || row.ErrorMessage != nil {
		t.Fatalf("successful successor carried old failure: %+v", row)
	}
	if calls.Load() != 3 || f.client.Segment.GetX(ctx, pending.ID).Status != segment.StatusTranslated {
		t.Fatalf("fallback did not translate the member it selected: calls=%d", calls.Load())
	}
}

func TestJobRunnerPauseAfterReservationPreservesMainAttempt(t *testing.T) {
	f, calls := terminalProviderFixture(t, http.StatusUnauthorized, 1)
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	var paused atomic.Bool
	f.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			v, err := next.Mutate(ctx, mutation)
			if err == nil && mutation.Op() == ent.OpCreate && paused.CompareAndSwap(false, true) && !f.runner.Pause(f.jobID) {
				return nil, errors.New("worker gate was not registered")
			}
			return v, err
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if status := f.client.Job.GetX(ctx, f.jobID).Status; status != service.JobStatusPaused || calls.Load() != 0 {
		t.Fatalf("pre-dispatch pause did not drain: status=%s calls=%d", status, calls.Load())
	}
	if _, err := f.jobs.ResumeJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	rows := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Order(ent.Asc(workrequest.FieldID)).AllX(ctx)
	if len(rows) != 2 || rows[0].State != "cancelled_before_dispatch" || rows[0].UsageRecordID != nil || rows[1].State != "completed" || rows[1].MemberPool != 0 || calls.Load() != 1 || f.client.Job.GetX(ctx, f.jobID).Status != service.JobStatusCompleted {
		t.Fatalf("resume lost the unspent attempt: calls=%d requests=%+v", calls.Load(), rows)
	}
}

func TestJobRunnerRecoversTerminalProviderAccountingWithoutDispatch(t *testing.T) {
	f, calls := terminalProviderFixture(t, http.StatusUnauthorized, 0)
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	interrupted := interruptProviderFailureAccounting(f)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := f.runner.ProcessOne(ctx, f.jobID); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if status := f.client.Job.GetX(ctx, f.jobID).Status; !interrupted.Load() || status != service.JobStatusRunning || calls.Load() != 1 {
		t.Fatalf("did not interrupt the in-flight execution after provider accounting: status=%s calls=%d", status, calls.Load())
	}
	if err := f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	assertTerminalProviderFailure(t, ctx, f, http.StatusUnauthorized)
	assertFailedProviderRequests(t, ctx, f, http.StatusUnauthorized, calls.Load(), []int{0})
}

func TestJobRunnerRecoversProviderFailurePoolAndBackoff(t *testing.T) {
	for _, tc := range []struct {
		status         int
		nextPool       int
		waitForBackoff bool
	}{
		{status: http.StatusBadRequest, nextPool: 1},
		{status: http.StatusTooManyRequests, nextPool: 0, waitForBackoff: true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			f, calls := terminalProviderFixture(t, tc.status, 2)
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			interrupted := interruptProviderFailureAccounting(f)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := f.runner.ProcessOne(ctx, f.jobID); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !interrupted.Load() || calls.Load() != 1 {
				t.Fatalf("did not interrupt after the first HTTP request: calls=%d", calls.Load())
			}
			member := f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).OnlyX(ctx)
			if member.PoolIndex != tc.nextPool || (member.NextAttemptAt != nil) != tc.waitForBackoff || !strings.Contains(member.LastError, http.StatusText(tc.status)) {
				t.Fatalf("provider accounting lost the next pool/backoff or root cause: %+v", member)
			}
			if err := f.jobs.PrepareRecovery(ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.runner.ProcessOne(ctx, f.jobID); err != nil {
				t.Fatal(err)
			}
			rows := f.client.WorkRequest.Query().Where(workrequest.JobRoundIDEQ(f.roundIDs[0])).Order(ent.Asc(workrequest.FieldID)).AllX(ctx)
			if len(rows) != 2 || calls.Load() != 2 || rows[0].State != "failed" || rows[1].State != "completed" || rows[1].MemberPool != tc.nextPool {
				t.Fatalf("wrong HTTP/ReserveRequest accounting after restart: calls=%d requests=%+v", calls.Load(), rows)
			}
			if member.NextAttemptAt != nil && rows[1].CreatedAt.Before(*member.NextAttemptAt) {
				t.Fatalf("recovery retried before the saved timeout: deadline=%s dispatch=%s", member.NextAttemptAt, rows[1].CreatedAt)
			}
			if job := f.client.Job.GetX(ctx, f.jobID); job.Status != service.JobStatusCompleted || job.ErrorMessage != nil {
				t.Fatalf("later success carried the old provider failure: %+v", job)
			}
		})
	}
}

func interruptProviderFailureAccounting(f *rubyPipelineFixture) *atomic.Bool {
	var interrupted atomic.Bool
	f.client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			v, err := next.Mutate(ctx, mutation)
			status, _ := mutation.Field("state")
			if err == nil && status == "failed" && interrupted.CompareAndSwap(false, true) {
				// The accounting transaction still joins under the worker lifetime.
				f.runner.Cancel(f.jobID)
			}
			return v, err
		})
	})
	return &interrupted
}

func assertTerminalProviderFailure(t *testing.T, ctx context.Context, f *rubyPipelineFixture, status int) {
	t.Helper()
	job := f.client.Job.GetX(ctx, f.jobID)
	resource := f.client.JobResource.GetX(ctx, f.jobResources[0])
	round := f.client.JobRound.GetX(ctx, f.roundIDs[0])
	if job.Status != service.JobStatusFailed || resource.Status != service.JobResourceStatusFailed || round.Status != service.JobRoundStatusFailed {
		t.Fatalf("terminal statuses are job=%s resource=%s round=%s", job.Status, resource.Status, round.Status)
	}
	for _, message := range []*string{job.ErrorMessage, resource.ErrorMessage, round.ErrorMessage} {
		if message == nil {
			t.Fatal("missing terminal cause")
		}
		if !strings.Contains(*message, http.StatusText(status)) || strings.Contains(*message, terminalProviderMessage) || strings.Contains(*message, "preserve all protected placeholders") {
			t.Fatalf("wrong terminal cause: %s", *message)
		}
	}
}

func assertFailedProviderRequests(t *testing.T, ctx context.Context, f *rubyPipelineFixture, status int, calls int32, pools []int) {
	t.Helper()
	rows := f.client.WorkRequest.Query().Where(workrequest.JobRoundIDEQ(f.roundIDs[0])).Order(ent.Asc(workrequest.FieldID)).AllX(ctx)
	if len(rows) != len(pools) || int(calls) != len(rows) {
		t.Fatalf("HTTP/ReserveRequest count = %d/%d, want %d", calls, len(rows), len(pools))
	}
	for i, request := range rows {
		if request.MemberPool != pools[i] || request.State != "failed" || request.UsageRecordID == nil || !strings.Contains(request.LastError, http.StatusText(status)) || strings.Contains(request.LastError, terminalProviderMessage) {
			t.Fatalf("wrong durable failed request for pool %d: %+v", pools[i], request)
		}
	}
}
