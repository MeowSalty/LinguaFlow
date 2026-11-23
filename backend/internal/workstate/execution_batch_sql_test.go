package workstate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
	"modernc.org/sqlite"
)

func normalizedExecutionSQL(q string) string {
	return strings.ToLower(strings.NewReplacer("\"", "", "`", "").Replace(q))
}

func executionBatchFixture(t *testing.T, n int) (context.Context, *ent.Client, *Store, Scope, []int, *storageSQLProbe) {
	t.Helper()
	ctx := t.Context()
	probe := &storageSQLProbe{}
	db := sql.OpenDB(storageSQLConnector{name: filepath.Join(t.TempDir(), "execution-batch.db") + "?_pragma=foreign_keys(1)", probe: probe, sqlite: &sqlite.Driver{}})
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, first := seedFixture(t, ctx, client)
	client.WorkItem.Delete().ExecX(ctx)
	client.JobRound.UpdateOneID(scope.RoundID).SetManifestSealed(false).SetSegmentTotal(0).ExecX(ctx)
	ids := []int{first.ID}
	for i := 1; i < n; i++ {
		ids = append(ids, client.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(i).SetSourceText("source").SaveX(ctx).ID)
	}
	return ctx, client, store, scope, ids, probe
}

func idPageWalk(ids []int) func(func([]int) error) error {
	return func(visit func([]int) error) error {
		for start := 0; start < len(ids); start += 16 {
			if err := visit(ids[start:min(start+16, len(ids))]); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestExecutionSealAndConfirmSQLPages(t *testing.T) {
	for _, n := range []int{1, 16, 17, 64} {
		t.Run(fmt.Sprintf("N%d", n), func(t *testing.T) {
			ctx, client, store, scope, ids, probe := executionBatchFixture(t, n)
			walk := func(visit func(int) error) error {
				for _, id := range ids {
					if err := visit(id); err != nil {
						return err
					}
				}
				return nil
			}
			probe.reset()
			if err := store.SealRoundStream(ctx, scope, n, walk); err != nil {
				t.Fatal(err)
			}
			sealed := probe.stop()
			validations, checkpoints, inserts, calibrationReads := 0, 0, 0, 0
			for _, call := range sealed.calls {
				q := normalizedExecutionSQL(call.query)
				if call.kind == "query" && strings.Contains(q, " from segments ") && strings.Contains(q, " in (") {
					validations++
				}
				if call.kind == "query" && strings.Contains(q, " from job_round_segments ") && strings.Contains(q, " in (") {
					if strings.Contains(q, "group by") {
						calibrationReads++ // Calibrate's fixed per-round authority read.
					} else {
						checkpoints++
					}
				}
				if strings.HasPrefix(q, "insert into work_items ") {
					inserts++
				}
			}
			pages := (n + 15) / 16
			if validations != pages || checkpoints != pages || inserts != pages || calibrationReads != 1 || sealed.begins != 1 || sealed.commits != 1 {
				t.Fatalf("seal work not page bounded: validations=%d checkpoints=%d inserts=%d calibration=%d tx=%d/%d", validations, checkpoints, inserts, calibrationReads, sealed.begins, sealed.commits)
			}
			probe.reset()
			if err := store.SealRoundStream(ctx, scope, n, walk); err != nil {
				t.Fatal(err)
			}
			replayed := probe.stop()
			membershipReads := 0
			for _, call := range replayed.calls {
				if strings.Contains(normalizedExecutionSQL(call.query), " from work_items ") {
					membershipReads++
				}
			}
			if membershipReads != pages+1 {
				t.Fatalf("sealed replay reads=%d want=%d", membershipReads, pages+1)
			}
			probe.reset()
			err := Transaction(ctx, client, func(tx *ent.Client) error {
				if err := LockJob(ctx, tx, scope.JobID); err != nil {
					return err
				}
				added, err := ConfirmIDStream(ctx, tx, scope.JobID, scope.RoundID, idPageWalk(ids))
				if err == nil && added != n {
					return fmt.Errorf("added %d want %d", added, n)
				}
				return err
			})
			confirmed := probe.stop()
			if err != nil {
				t.Fatal(err)
			}
			roundUpdates, jobUpdates, factInserts := 0, 0, 0
			for _, call := range confirmed.calls {
				q := normalizedExecutionSQL(call.query)
				if strings.HasPrefix(q, "update job_rounds set ") {
					roundUpdates++
				}
				if strings.HasPrefix(q, "update jobs set ") && strings.Contains(q, "progress_completed") {
					jobUpdates++
				}
				if strings.HasPrefix(q, "insert into job_round_segments ") {
					factInserts++
				}
			}
			if roundUpdates != 1 || jobUpdates != 1 || factInserts != pages || confirmed.commits != 1 {
				t.Fatalf("confirm repeated progress work: round=%d job=%d facts=%d commits=%d", roundUpdates, jobUpdates, factInserts, confirmed.commits)
			}
			if client.JobRound.GetX(ctx, scope.RoundID).SegmentCompleted != n || client.Job.GetX(ctx, scope.JobID).ProgressCompleted != int64(n) {
				t.Fatal("stream checkpoint changed authoritative progress")
			}
			t.Logf("real driver N=%d pages=%d seal submissions=%d replay=%d confirm=%d seal_tx=%d/%d confirm_tx=%d/%d", n, pages, len(sealed.calls), len(replayed.calls), len(confirmed.calls), sealed.begins, sealed.commits, confirmed.begins, confirmed.commits)
		})
	}
}

func TestExecutionPageFailureRollsBackSealAndConfirm(t *testing.T) {
	ctx, client, store, scope, ids, _ := executionBatchFixture(t, 17)
	broken := append([]int(nil), ids...)
	broken[16] = 999999
	err := store.SealRoundStream(ctx, scope, len(broken), func(visit func(int) error) error {
		for _, id := range broken {
			if err := visit(id); err != nil {
				return err
			}
		}
		return nil
	})
	if !errors.Is(err, ErrManifest) || client.WorkItem.Query().CountX(ctx) != 0 || client.JobRound.GetX(ctx, scope.RoundID).ManifestSealed {
		t.Fatalf("late invalid seal left a prefix: %v", err)
	}
	if _, err := store.SealRound(ctx, scope, ids); err != nil {
		t.Fatal(err)
	}
	err = Transaction(ctx, client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, scope.JobID); err != nil {
			return err
		}
		_, err := ConfirmIDStream(ctx, tx, scope.JobID, scope.RoundID, idPageWalk(broken))
		return err
	})
	if !errors.Is(err, ErrManifest) || client.JobRoundSegment.Query().CountX(ctx) != 0 || client.Job.GetX(ctx, scope.JobID).ProgressCompleted != 0 {
		t.Fatalf("late invalid checkpoint left a prefix: %v", err)
	}
}

func TestReadStageCountsBatchOneStatement(t *testing.T) {
	ctx, client, _, scope, _, probe := executionBatchFixture(t, 1)
	ids := []int{scope.JobID}
	projectID := client.Job.GetX(ctx, scope.JobID).ProjectID
	for i := 1; i < 100; i++ {
		ids = append(ids, client.Job.Create().SetProjectID(projectID).SetExecutionPlanID(1).SetStatus("running").SaveX(ctx).ID)
	}
	for _, count := range []int{0, 1, 100} {
		probe.reset()
		observed, err := ReadStageCountsBatch(ctx, client, ids[:count])
		s := probe.stop()
		if err != nil || len(observed) != count {
			t.Fatalf("count=%d rows=%d err=%v", count, len(observed), err)
		}
		want := int64(1)
		if count == 0 {
			want = 0
		}
		if s.queries != want {
			t.Fatalf("count=%d stage SQL=%d want=%d", count, s.queries, want)
		}
		for _, row := range observed {
			if !row.AsOf.Equal(observed[scope.JobID].AsOf) {
				t.Fatal("batch observation times differ")
			}
		}
		t.Logf("real driver stage jobs=%d SQL=%d rows=%d bytes=%d", count, s.queries, s.rows, s.valueBytes)
	}
	if client.WorkItem.Query().Where(workitem.JobIDEQ(scope.JobID)).CountX(ctx) != 0 || client.JobRound.Query().Where(jobround.JobIDEQ(scope.JobID)).CountX(ctx) != 1 {
		t.Fatal("stage read mutated state")
	}
}

func TestExecutionFreezeCaptureArchiveBulkSQL(t *testing.T) {
	const n = 17
	ctx, client, store, scope, ids, probe := executionBatchFixture(t, n)
	if _, err := store.SealRound(ctx, scope, ids); err != nil {
		t.Fatal(err)
	}
	currentExecution(t, ctx, client, scope)
	first := client.Segment.GetX(ctx, ids[0])
	request, capture, _ := rawRequest(scope, first)
	request.InputMembers = nil
	request.SegmentIDs = ids
	for i, id := range ids {
		other, _, _ := rawRequest(scope, client.Segment.GetX(ctx, id))
		member := other.InputMembers[0]
		member.Key.Ordinal = i
		member.PromptID = fmt.Sprintf("prompt-%d", i)
		member.CandidateID = fmt.Sprintf("candidate-%d", i)
		request.InputMembers = append(request.InputMembers, member)
	}
	probe.reset()
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	frozen := probe.stop()
	projectedHook := 0
	client.WorkRequest.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			_, captured := mutation.Field(workrequest.FieldCapturePayload)
			value, err := next.Mutate(ctx, mutation)
			if captured && err == nil {
				row := value.(*ent.WorkRequest)
				if mutation.Op() != ent.OpUpdateOne || row.ID <= 0 || len(row.CapturePayload) != 0 {
					t.Error("discarded update lost hook/ID projection contract")
				}
				projectedHook++
			}
			return value, err
		})
	})
	capture.Payload = []byte(strings.Repeat("paid-response", 5000))
	probe.reset()
	if _, err := store.CaptureResponse(ctx, capture); err != nil {
		t.Fatal(err)
	}
	captured := probe.stop()
	if projectedHook != 1 {
		t.Fatalf("capture hook calls=%d", projectedHook)
	}
	for _, call := range captured.calls {
		q := normalizedExecutionSQL(call.query)
		if strings.Contains(strings.SplitN(q, " from ", 2)[0], "work_requests.capture_payload") {
			t.Fatal("capture update read back paid response body")
		}
	}
	probe.reset()
	if _, err := store.PublishOutputs(ctx, integrityPublication(request, 0, 16, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishOutputs(ctx, integrityPublication(request, 16, n, true)); err != nil {
		t.Fatal(err)
	}
	archived := probe.stop()
	countPrefix := func(s storageSQLSnapshot, prefix string) int {
		count := 0
		for _, call := range s.calls {
			if strings.HasPrefix(normalizedExecutionSQL(call.query), prefix) {
				count++
			}
		}
		return count
	}
	if countPrefix(frozen, "insert into work_request_input_members ") != 2 || countPrefix(captured, "update work_items set ") != 2 || countPrefix(archived, "insert into work_response_members ") != 2 {
		t.Fatalf("member submissions grew per row: freeze=%d capture=%d archive=%d", countPrefix(frozen, "insert into work_request_input_members "), countPrefix(captured, "update work_items set "), countPrefix(archived, "insert into work_response_members "))
	}
	if err := client.WorkRequest.UpdateOneID(999999).Select(workrequest.FieldID).Exec(ctx); !ent.IsNotFound(err) {
		t.Fatalf("projection removed NotFound: %v", err)
	}
	t.Logf("real driver members=%d input_INSERT=2 capture_work_UPDATE=2 output_INSERT=2 capture_return_bytes=%d submissions=%d/%d/%d", n, captured.valueBytes, len(frozen.calls), len(captured.calls), len(archived.calls))
}

func TestExecutionCaptureSecondPageZeroMatchRollsBackPaidResponse(t *testing.T) {
	const n = 17
	ctx, client, store, scope, ids, probe := executionBatchFixture(t, n)
	if _, err := store.SealRound(ctx, scope, ids); err != nil {
		t.Fatal(err)
	}
	currentExecution(t, ctx, client, scope)
	request, capture, _ := rawRequest(scope, client.Segment.GetX(ctx, ids[0]))
	request.InputMembers = nil
	request.SegmentIDs = ids
	for i, id := range ids {
		other, _, _ := rawRequest(scope, client.Segment.GetX(ctx, id))
		member := other.InputMembers[0]
		member.Key.Ordinal = i
		member.PromptID = fmt.Sprintf("prompt-%d", i)
		member.CandidateID = fmt.Sprintf("candidate-%d", i)
		request.InputMembers = append(request.InputMembers, member)
	}
	if err := store.ReserveRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	before := client.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).Order(ent.Asc(workitem.FieldID)).AllX(ctx)
	lastID := before[len(before)-1].ID
	injected := false
	client.WorkItem.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if !injected && mutation.Op() == ent.OpUpdate {
				if owner, ok := mutation.Field(workitem.FieldResponseRequestID); ok && owner == request.ID {
					m := mutation.(*ent.WorkItemMutation)
					matched, err := m.IDs(ctx)
					if err != nil {
						return nil, err
					}
					for _, id := range matched {
						if id == lastID {
							injected = true
							// This real same-transaction change invalidates the second
							// page's original state CAS after the first page was saved.
							if err := m.Client().WorkItem.UpdateOneID(id).SetState("resolved").Select(workitem.FieldID).Exec(ctx); err != nil {
								return nil, err
							}
						}
					}
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	probe.reset()
	_, err := store.CaptureResponse(ctx, capture)
	failed := probe.stop()
	if !injected || !errors.Is(err, ErrStale) || failed.begins != 1 || failed.commits != 0 {
		t.Fatalf("zero-match paid capture accepted: injected=%v err=%v tx=%d/%d", injected, err, failed.begins, failed.commits)
	}
	after := client.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).Order(ent.Asc(workitem.FieldID)).AllX(ctx)
	for i, row := range after {
		if row.State != before[i].State || row.ResponseRequestID != before[i].ResponseRequestID || row.MainAttempts != before[i].MainAttempts || row.MainNetworkAttempts != before[i].MainNetworkAttempts {
			t.Fatal("second-page CAS failure left a changed member prefix or attempt debit")
		}
	}
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(request.ID)).OnlyX(ctx)
	if row.State != "reserved" || row.ResponseDigest != "" || row.CaptureBytes != 0 || row.PromotionState != "inputs_ready" || client.UsageRecord.Query().CountX(ctx) != 0 {
		t.Fatal("failed capture published response or accounting")
	}
	if _, err := store.CaptureResponse(ctx, capture); err != nil {
		t.Fatalf("paid response could not be saved after rolled-back CAS: %v", err)
	}
	row = client.WorkRequest.Query().Where(workrequest.IdentityEQ(request.ID)).OnlyX(ctx)
	if row.CaptureBytes != int64(len(capture.Payload)) || row.ResponseDigest != payloadDigest(capture.Payload) || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("paid response retry lost durable payload or exactly-once accounting")
	}
	t.Logf("real second capture page returned zero matches; rollback commits=0 and paid payload retry preserved %d members", n)
}
