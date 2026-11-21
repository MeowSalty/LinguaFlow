package workstate

import (
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
)

func TestCommitReusesLockedJobAndCandidateSQLFacts(t *testing.T) {
	ctx, client, _, _, request, probe, _, _ := storageSQLFixture(t)
	jobRow := client.Job.GetX(ctx, request.JobID)
	resource := client.Resource.GetX(ctx, request.ResourceID)
	round := client.JobRound.GetX(ctx, request.JobRoundID)
	scope := Scope{JobID: request.JobID, ResourceID: request.ResourceID, RoundID: request.JobRoundID, JobResourceID: round.JobResourceID, RetryEpoch: jobRow.RetryEpoch, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
	row := client.Segment.Query().OnlyX(ctx)
	store := NewStore(client)
	candidate := ready(scope, row)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	probe.reset()
	result, err := store.Commit(ctx, input(candidate))
	physical := probe.stop()
	if err != nil || result.Outcome != Committed || result.ContentVersion != 2 {
		t.Fatalf("commit=%+v err=%v", result, err)
	}
	jobLocks, candidateReads, roundReads := 0, 0, 0
	for _, call := range physical.calls {
		q := strings.ToLower(strings.NewReplacer("\"", "", "`", "").Replace(call.query))
		if call.kind == "exec" && strings.HasPrefix(q, "update jobs set ") && strings.Contains(q, job.FieldRetryEpoch) {
			jobLocks++
		}
		if call.kind == "query" && strings.Contains(q, " from work_candidates ") {
			projection := strings.SplitN(q, " from ", 2)[0]
			if strings.Contains(projection, "baseline_version") && strings.Contains(projection, "identity") && strings.Contains(projection, "state") {
				candidateReads++
			}
		}
		if call.kind == "query" && strings.Contains(q, " from job_rounds ") {
			projection := strings.SplitN(q, " from ", 2)[0]
			if strings.Contains(projection, jobround.FieldJobResourceID) && strings.Contains(projection, jobround.FieldManifestSealed) {
				roundReads++
				_, columns := scopeSelectedColumns(t, q)
				if len(columns) != 4 {
					t.Fatalf("commit fetched unused round fields: %v", columns)
				}
			}
		}
	}
	if jobLocks != 1 || candidateReads != 1 || roundReads != 1 || physical.queries != 19 || physical.begins != 1 || physical.commits != 1 {
		t.Fatalf("physical SQL reused facts incorrectly: locks=%d candidate_reads=%d round_reads=%d queries=%d begin/commit=%d/%d", jobLocks, candidateReads, roundReads, physical.queries, physical.begins, physical.commits)
	}
	t.Logf("actual database/sql boundary: job_lock_updates=%d candidate_authority_reads=%d round_authority_reads=%d queries=%d rows=%d value_bytes=%d; round previously had separate guard projection and full Get", jobLocks, candidateReads, roundReads, physical.queries, physical.rows, physical.valueBytes)
}

func TestCommitRetainsManifestFactsAtOriginalMemberBoundary(t *testing.T) {
	for _, name := range []string{"unsealed", "member_epoch_first"} {
		t.Run(name, func(t *testing.T) {
			ctx, client, _, _, request, probe, _, _ := storageSQLFixture(t)
			jobRow := client.Job.GetX(ctx, request.JobID)
			resource := client.Resource.GetX(ctx, request.ResourceID)
			round := client.JobRound.GetX(ctx, request.JobRoundID)
			scope := Scope{JobID: request.JobID, ResourceID: request.ResourceID, RoundID: request.JobRoundID, JobResourceID: round.JobResourceID, RetryEpoch: jobRow.RetryEpoch, SourceGeneration: resource.SourceGeneration, SourceRevisionID: resource.CurrentSourceRevisionID}
			row := client.Segment.Query().OnlyX(ctx)
			store := NewStore(client)
			candidate := ready(scope, row)
			if err := store.SaveCandidate(ctx, candidate); err != nil {
				t.Fatal(err)
			}
			client.JobRound.UpdateOneID(scope.RoundID).SetManifestSealed(false).ExecX(ctx)
			wantErr := ErrManifest
			if name == "member_epoch_first" {
				client.WorkItem.Update().SetRetryEpoch(scope.RetryEpoch + 1).ExecX(ctx)
				wantErr = ErrStopped
			}
			probe.reset()
			result, err := store.Commit(ctx, input(candidate))
			physical := probe.stop()
			if !errors.Is(err, wantErr) || result.Outcome != Fatal || physical.begins != 1 || physical.commits != 0 {
				t.Fatalf("manifest/member order changed: outcome=%s err=%v tx=%d/%d", result.Outcome, err, physical.begins, physical.commits)
			}
			after := client.Segment.GetX(ctx, row.ID)
			if after.ContentVersion != row.ContentVersion || !sameString(after.TargetText, row.TargetText) {
				t.Fatal("unsealed/stale member applied a result")
			}
			if n, err := client.JobRoundSegment.Query().Count(ctx); err != nil || n != 0 {
				t.Fatalf("failed manifest check wrote confirmation: n=%d err=%v", n, err)
			}
		})
	}
}
