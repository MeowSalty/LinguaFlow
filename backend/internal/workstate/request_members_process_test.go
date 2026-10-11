package workstate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestBatchProcessKillPreservesPartialMemberHandoff(t *testing.T) {
	for _, phase := range []string{"partial", "pausing"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "batch-crash.db")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBatchWorkstateCrashHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "LINGUAFLOW_BATCH_CRASH_PATH="+path, "LINGUAFLOW_BATCH_CRASH_PHASE="+phase)
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
				if scanner.Text() == "BATCH_MEMBER_DURABLE" {
					acknowledged = true
					break
				}
			}
			if !acknowledged {
				t.Fatalf("child failed before member handoff: %v %s", cmd.Wait(), stderr.String())
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("expected abrupt process termination")
			}
			for range 3 {
				func() {
					client := openCrashDatabase(t, path)
					defer client.Close()
					job := client.Job.Query().OnlyX(ctx)
					round := client.JobRound.Query().OnlyX(ctx)
					resource := client.Resource.Query().OnlyX(ctx)
					scope := Scope{JobID: job.ID, ResourceID: resource.ID, RoundID: round.ID, RetryEpoch: job.RetryEpoch}
					if err := Transaction(ctx, client, func(tx *ent.Client) error {
						if err := LockJob(ctx, tx, job.ID); err != nil {
							return err
						}
						return MarkUnknown(ctx, tx, job.ID)
					}); err != nil {
						t.Fatal(err)
					}
					store := NewStore(client)
					candidates, _, err := store.LoadCandidates(ctx, scope, 0, 10)
					if err != nil || len(candidates) != 2 || candidates[0].Version != 2 || candidates[1].Version != 1 {
						t.Fatalf("crash lost member versions: %+v, %v", candidates, err)
					}
					var saved struct {
						ForceSingle bool     `json:"force_single_alignment"`
						Verified    []string `json:"validated"`
						RequestID   string   `json:"last_alignment_request_id"`
					}
					if err := json.Unmarshal(candidates[0].Payload, &saved); err != nil || !saved.ForceSingle || len(saved.Verified) != 1 || saved.RequestID != "crash-batch" {
						t.Fatalf("crash lost saved isolation/progress: %s, %v", candidates[0].Payload, err)
					}
					want := []attemptCounters{{Alignment: 1}, {Alignment: 1, AlignmentNetwork: 1, Network: 1}}
					if got := batchCounters(ctx, client); !reflect.DeepEqual(got, want) {
						t.Fatalf("reopening changed per-member budget: %+v", got)
					}
					request := client.WorkRequest.Query().OnlyX(ctx)
					members, err := decodeRequestMembers(request.Members)
					if err != nil || len(members) != 2 || request.State != "unknown" {
						t.Fatalf("crash lost shared invocation: %+v, %v", request, err)
					}
					if err := store.AbortRequest(ctx, request.Identity); !errors.Is(err, ErrStopped) {
						t.Fatalf("recovery refunded uncertain member budgets: %v", err)
					}
					if client.UsageRecord.Query().CountX(ctx) != 1 || client.JobRoundSegment.Query().CountX(ctx) != 0 || job.ProgressCompleted != 0 {
						t.Fatal("repeated reopening multiplied usage or accepted a private draft")
					}
					if phase == "pausing" && (!job.PauseRequested || job.Status != "pausing") {
						t.Fatal("kill lost durable pause intent")
					}
				}()
			}
		})
	}
}

func TestBatchWorkstateCrashHelper(t *testing.T) {
	path := os.Getenv("LINGUAFLOW_BATCH_CRASH_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	client := openCrashDatabase(t, path)
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, first := seedFixture(t, ctx, client)
	second := client.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(1).SetSourceText("second").SaveX(ctx)
	client.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(second.ID).SetRetryEpoch(0).ExecX(ctx)
	client.JobRound.UpdateOneID(scope.RoundID).SetSegmentTotal(2).ExecX(ctx)
	request := Request{ID: "crash-batch", Scope: scope, Stage: "alignment", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "batch-input", MaxAlignmentAttempts: 2, MaxNetworkAttempts: 2}
	var candidates []Candidate
	for i, seg := range []*ent.Segment{first, second} {
		c := ready(scope, seg)
		c.ID, c.WorkID, c.State, c.DTOVersion = fmt.Sprintf("member-%d", i), WorkIdentity(scope, seg.ID), "pending_alignment", 2
		if err := store.SaveCandidate(ctx, c); err != nil {
			t.Fatal(err)
		}
		candidates = append(candidates, c)
		request.SegmentIDs = append(request.SegmentIDs, seg.ID)
		request.Members = append(request.Members, RequestMember{SegmentID: seg.ID, WorkID: c.WorkID, CandidateID: c.ID, CandidateVersion: 1})
	}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRequest(ctx, request.ID, RequestResult{State: "received", UsageKnown: true, InputTokens: 7, OutputTokens: 11}); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LINGUAFLOW_BATCH_CRASH_PHASE") == "pausing" {
		client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
	}
	c := responseCandidate(t, candidates[0], request.ID, true)
	cur, err := store.LoadCursor(ctx, scope, c.SegmentID)
	if err != nil {
		t.Fatal(err)
	}
	cur.PromptPhase, cur.NetworkAttempts, cur.AlignmentNetworkAttempts = "alignment_complete", 0, 0
	if err := store.SaveCandidateWithCursor(ctx, c, cur); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "BATCH_MEMBER_DURABLE")
	// The parent kills the helper before the second member save or any Close.
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
}
