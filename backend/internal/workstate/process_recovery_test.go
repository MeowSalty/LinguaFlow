package workstate

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// The child acknowledges a committed boundary and then waits without closing
// SQLite. The parent kills it and opens a fresh connection. This covers actual
// process loss; HTTP/DTO reconstruction is covered by the worker integration
// and pipeline protocol tests, respectively.
func TestProcessKillPreservesDurableWork(t *testing.T) {
	for _, phase := range []string{"draft", "partial", "ready", "pausing", "confirmed"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkstateCrashHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "LINGUAFLOW_WORKSTATE_CRASH_PATH="+path, "LINGUAFLOW_WORKSTATE_CRASH_PHASE="+phase)
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
			scanner := bufio.NewScanner(stdout)
			acknowledged := false
			for scanner.Scan() {
				if scanner.Text() == "WORKSTATE_DURABLE" {
					acknowledged = true
					break
				}
			}
			if !acknowledged {
				err := cmd.Wait()
				t.Fatalf("child did not persist boundary: %v %s", err, stderr.String())
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("expected abnormal process termination")
			}
			// Reopening repeatedly must not refresh pool or attempt budgets.
			for range 3 {
				func() {
					client := openCrashDatabase(t, path)
					defer client.Close()
					job := client.Job.Query().OnlyX(ctx)
					round := client.JobRound.Query().OnlyX(ctx)
					jr := client.JobResource.Query().OnlyX(ctx)
					seg := client.Segment.Query().OnlyX(ctx)
					scope := Scope{JobID: job.ID, ResourceID: *seg.ResourceID, JobResourceID: jr.ID, RoundID: round.ID, RetryEpoch: job.RetryEpoch}
					store := NewStore(client)
					if err := Transaction(ctx, client, func(tx *ent.Client) error {
						if err := LockJob(ctx, tx, job.ID); err != nil {
							return err
						}
						return MarkUnknown(ctx, tx, job.ID)
					}); err != nil {
						t.Fatal(err)
					}
					state, err := store.LoadRound(ctx, scope)
					if err != nil || !state.Sealed || state.Total != 1 || len(state.Members) != 1 || len(state.Work) != 1 {
						t.Fatalf("manifest after crash: %+v %v", state, err)
					}
					work := state.Work[0]
					if work.Cursor.PoolIndex != 2 || work.Cursor.MainAttempts != 1 || work.RetryEpoch != 0 {
						t.Fatalf("restart reset pool/epoch/attempt: %+v", work)
					}
					candidates, _, err := store.LoadCandidates(ctx, scope, 0, 16)
					if err != nil {
						t.Fatal(err)
					}
					if phase == "confirmed" {
						if len(candidates) != 0 || len(state.Completed) != 1 || job.ProgressCompleted != 1 || seg.TargetText == nil || *seg.TargetText != "translated" || seg.ContentVersion != 2 {
							t.Fatal("confirmed crash lost atomic result or retained draft")
						}
						ok, err := store.LookupCommit(ctx, "commit-1")
						if err != nil || !ok {
							t.Fatalf("lost stable commit receipt: %v", err)
						}
					} else {
						if len(candidates) != 1 || len(state.Completed) != 0 || job.ProgressCompleted != 0 || seg.TargetText != nil {
							t.Fatal("draft crash changed official completion")
						}
						candidate := candidates[0]
						if !bytes.Equal(candidate.Payload, crashPayload(phase)) || candidate.ParentRequestID != "crash-main" || candidate.SnapshotDigest != "frozen" {
							t.Fatalf("candidate facts changed: %+v", candidate)
						}
						want := "pending_alignment"
						if phase == "ready" {
							want = "ready_to_commit"
						}
						if candidate.State != want {
							t.Fatalf("candidate state=%s want=%s", candidate.State, want)
						}
					}
					if phase != "draft" {
						if work.Cursor.AlignmentAttempts != 1 || work.Cursor.AlignmentNetworkAttempts != 1 {
							t.Fatalf("restart refreshed alignment budget: %+v", work.Cursor)
						}
					}
					if phase == "pausing" {
						paused, err := store.PauseRequested(ctx, job.ID)
						if err != nil || !paused {
							t.Fatal("crash lost pause intent")
						}
						err = store.ReserveRequest(ctx, Request{ID: "must-not-dispatch", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BudgetModel: "stage_separated", InputDigest: "frozen"})
						if !errors.Is(err, ErrStopped) {
							t.Fatalf("pausing crash permitted admission: %v", err)
						}
					}
					wantCalls := 2
					if phase == "draft" {
						wantCalls = 1
					}
					if client.UsageRecord.Query().CountX(ctx) != wantCalls {
						t.Fatal("repeated recovery duplicated actual request usage")
					}
				}()
			}
		})
	}
}

func openCrashDatabase(t *testing.T, path string) *ent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
}

func crashPayload(phase string) []byte {
	if phase == "draft" {
		return []byte(`{"verified":[],"missing":["r1","r2"]}`)
	}
	if phase == "partial" || phase == "pausing" {
		return []byte(`{"verified":["r1"],"missing":["r2"]}`)
	}
	return []byte(`{"verified":["r1","r2"],"missing":[]}`)
}

func TestWorkstateCrashHelper(t *testing.T) {
	path := os.Getenv("LINGUAFLOW_WORKSTATE_CRASH_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	phase := os.Getenv("LINGUAFLOW_WORKSTATE_CRASH_PHASE")
	client := openCrashDatabase(t, path)
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, seg := seedFixture(t, ctx, client)
	if err := store.SaveCursor(ctx, scope, seg.ID, Cursor{PoolIndex: 2, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	request := Request{ID: "crash-main", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", MainAttempt: true, BackendID: 1, BudgetModel: "stage_separated", InputDigest: "main", MaxMainAttempts: 1}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, request.ID, RequestResult{State: "completed", UsageKnown: true, InputTokens: 7, OutputTokens: 11}); err != nil {
		t.Fatal(err)
	}
	candidate := ready(scope, seg)
	candidate.State, candidate.ParentRequestID, candidate.Payload = "pending_alignment", request.ID, crashPayload("draft")
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if phase != "draft" {
		request.ID, request.InputDigest, request.Stage, request.CandidateID = "crash-alignment", "alignment", "ruby_alignment", candidate.ID
		request.MainAttempt, request.AlignmentAttempt, request.MaxAlignmentAttempts = false, true, 1
		if err := store.ReserveRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordRequest(ctx, request.ID, RequestResult{State: "received", UsageKnown: true, InputTokens: 3, OutputTokens: 5}); err != nil {
			t.Fatal(err)
		}
		candidate.Version++
		candidate.Payload = crashPayload(phase)
		if phase == "ready" || phase == "confirmed" {
			candidate.State = "ready_to_commit"
		}
		if err := store.SaveCandidate(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "pausing" {
		client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	}
	if phase == "confirmed" {
		if result, err := store.Commit(ctx, input(candidate)); err != nil || result.Outcome != Committed {
			t.Fatalf("commit=%+v %v", result, err)
		}
	}
	fmt.Fprintln(os.Stdout, "WORKSTATE_DURABLE")
	// The parent keeps stdin open until it kills this process. No database
	// Close, shutdown hook, or test cleanup executes on the successful path.
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}
