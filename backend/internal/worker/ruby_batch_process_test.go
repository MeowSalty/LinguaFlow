package worker

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func openRubyBatchProcessClient(t *testing.T, path string) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
	return client
}

func loadRubyBatchProcessFixture(t *testing.T, path string) *rubyPipelineFixture {
	t.Helper()
	ctx := context.Background()
	client := openRubyBatchProcessClient(t, path)
	jobRow := client.Job.Query().OnlyX(ctx)
	owner := client.User.Query().OnlyX(ctx)
	f := &rubyPipelineFixture{client: client, jobID: jobRow.ID, ownerID: owner.ID, pool: backend.NewLimiterPool(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(f.pool.Shutdown)
	encoded, err := json.Marshal(jobRow.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	f.snapshot = &service.JobExecutionSnapshot{}
	if err := json.Unmarshal(encoded, f.snapshot); err != nil {
		t.Fatal(err)
	}
	for _, jr := range client.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(jobRow.ID))).WithResource().Order(ent.Asc(jobresource.FieldID)).AllX(ctx) {
		f.jobResources = append(f.jobResources, jr.ID)
		f.resourceIDs = append(f.resourceIDs, jr.Edges.Resource.ID)
		round := client.JobRound.Query().Where(jobround.JobResourceIDEQ(jr.ID)).OnlyX(ctx)
		f.roundIDs = append(f.roundIDs, round.ID)
	}
	f.pool.Initialize(map[int]int{f.snapshot.RubyRetry.Backend.ID: 0})
	f.initializeServices(t)
	return f
}

func TestRubyBatchHTTPProcessKillBetweenMemberSavesRecoversWithoutReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "worker-batch-crash.db")
	// The provider stays in the parent process. Both executions therefore use
	// the identical frozen URL, credentials and real OpenAI HTTP adapter, and
	// the recorder observes every request across the crash boundary.
	recorder := &rubyPipelineFixture{}
	upstream := httptest.NewServer(http.HandlerFunc(recorder.serveHTTP))
	t.Cleanup(upstream.Close)
	client := openRubyBatchProcessClient(t, path)
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	seed := newRubyPipelineFixtureWithClient(t, client, upstream.URL, 4)
	seed.snapshot.Rounds[0].Translate.BatchSize = 4
	batch, words, waitMS := 4, 0, 100
	seed.snapshot.RubyRetry.BatchSize = &batch
	seed.snapshot.RubyRetry.MaxWordsPerBatch = &words
	seed.snapshot.RubyRetry.BatchWaitMS = &waitMS
	seed.persistSnapshot(t)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRubyBatchHTTPProcessCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "LINGUAFLOW_RUBY_BATCH_PROCESS_DB="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	acknowledged := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if scanner.Text() == "RUBY_BATCH_FIRST_MEMBER_DURABLE" {
			acknowledged = true
			break
		}
	}
	if !acknowledged {
		t.Fatalf("worker failed before durable member boundary: %v %s", cmd.Wait(), stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("expected actual abnormal worker process termination")
	}

	f := loadRubyBatchProcessFixture(t, path)
	rows := f.client.WorkCandidate.Query().Where(workcandidate.HasWorkItemWith(workitem.JobRoundIDEQ(f.roundIDs[0]))).Order(ent.Asc(workcandidate.FieldID)).AllX(ctx)
	if len(rows) != 4 {
		t.Fatalf("crash retained %d main candidates, want 4", len(rows))
	}
	savedID := ""
	for _, row := range rows {
		if row.Version == 2 {
			if savedID != "" || row.State != "ready_to_commit" {
				t.Fatalf("unexpected extra saved member: %+v", row)
			}
			savedID = row.Identity
		} else if row.Version != 1 || row.State != "pending_alignment" {
			t.Fatalf("crash corrupted an unsaved member: %+v", row)
		}
	}
	if savedID == "" {
		t.Fatal("kill lost the acknowledged member save")
	}
	for _, w := range f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).AllX(ctx) {
		wantNetwork := 1
		if w.CandidateID == savedID {
			wantNetwork = 0
		}
		if w.MainAttempts != 1 || w.AlignmentAttempts != 1 || w.AlignmentNetworkAttempts != wantNetwork {
			t.Fatalf("kill lost a member's pre-debited attempts: %+v", w)
		}
	}
	request := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID), workrequest.StageEQ("alignment")).OnlyX(ctx)
	if request.State != "received" || request.UsageRecordID == nil || !request.UsageKnown {
		t.Fatalf("kill lost received shared invocation: %+v", request)
	}
	if f.client.JobRoundSegment.Query().CountX(ctx) != 0 || f.client.Job.GetX(ctx, f.jobID).ProgressCompleted != 0 {
		t.Fatal("partial response handoff prematurely confirmed work")
	}
	f.assertUsage(t, 2)
	if err := f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.client.WorkRequest.GetX(ctx, request.ID).State; got != "unknown" {
		t.Fatalf("recovery request state=%s", got)
	}
	_, recovered := f.round(t, 0)
	candidates, _, err := recovered.Candidates(ctx, 0, 10)
	if err != nil || len(candidates) != 4 {
		t.Fatalf("recover durable candidates: %v", err)
	}
	for _, c := range candidates {
		if c.ID == savedID {
			if !c.Ready || c.NetworkAttempt != 0 || c.LogicalAttempt != 1 {
				t.Fatalf("saved member recovery=%+v", c)
			}
		} else if c.Ready || c.NetworkAttempt != 1 || c.LogicalAttempt != 0 {
			t.Fatalf("unknown member budget reset on recovery: %+v", c)
		}
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("post-kill execution=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 3)
	mainCalls, alignmentCalls, _, _ := recorder.calls()
	requests := alignmentHTTPRequests(recorder)
	if mainCalls != 1 || alignmentCalls != 2 || len(requests[0].Alignments) != 4 || len(requests[1].Alignments) != 3 {
		t.Fatalf("process recovery replayed work: main=%d align=%+v", mainCalls, requests)
	}
	for _, member := range requests[1].Alignments {
		if member.CandidateID == savedID {
			t.Fatal("process recovery resent the durably saved member")
		}
	}
	for _, w := range f.client.WorkItem.Query().Where(workitem.JobRoundIDEQ(f.roundIDs[0])).AllX(ctx) {
		if w.MainAttempts != 1 || w.AlignmentAttempts != 1 || w.AlignmentNetworkAttempts != 0 {
			t.Fatalf("recovery refreshed rather than continued unknown budget: %+v", w)
		}
	}
	// Repeated recovery consumes completed facts without requests, checkpoints,
	// segment content-version increments or usage records being repeated.
	_, result, err = f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("completed replay=%+v, %v", result, err)
	}
	f.assertAccepted(t, 0, 4)
	f.assertUsage(t, 3)
	if main, align, _, _ := recorder.calls(); main != 1 || align != 2 {
		t.Fatal("completed replay sent new HTTP")
	}
	if f.client.Job.GetX(ctx, f.jobID).ProgressCompleted != 4 {
		t.Fatal("process recovery repeated completion accounting")
	}
}

func TestRubyBatchHTTPProcessCrashHelper(t *testing.T) {
	path := os.Getenv("LINGUAFLOW_RUBY_BATCH_PROCESS_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	f := loadRubyBatchProcessFixture(t, path)
	memberSaves := 0
	_, _, err := f.execute(t, 0, nil, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, beforeSave: func(_ context.Context, c *pipeline.Candidate) error {
			if c.LogicalAttempt == 1 {
				memberSaves++
				if memberSaves == 2 {
					fmt.Fprintln(os.Stdout, "RUBY_BATCH_FIRST_MEMBER_DURABLE")
					// Keep the real worker alive with the first committed save,
					// an open SQLite connection and the shared HTTP response.
					// Parent kills us before this second save or any shutdown.
					_, _ = bufio.NewReader(os.Stdin).ReadByte()
					return fmt.Errorf("parent closed process barrier without killing worker")
				}
			}
			return nil
		}}
	})
	t.Fatalf("worker unexpectedly passed process kill boundary: %v", err)
}
