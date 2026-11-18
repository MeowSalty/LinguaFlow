package workstate

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

type stageObservationQuery struct {
	query string
	args  []any
}

type stageObservationDriver struct {
	dialect.Driver
	mu      sync.Mutex
	queries []stageObservationQuery
}

func (d *stageObservationDriver) Query(ctx context.Context, q string, args, rows any) error {
	if strings.Contains(q, "confirmed_work") || strings.Contains(q, "stage_work_item") {
		d.mu.Lock()
		d.queries = append(d.queries, stageObservationQuery{q, append([]any(nil), args.([]any)...)})
		d.mu.Unlock()
	}
	return d.Driver.Query(ctx, q, args, rows)
}

func (d *stageObservationDriver) observations() []stageObservationQuery {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]stageObservationQuery(nil), d.queries...)
}

// Each confirmed member is observed immediately. Historical checkpoints and
// completed candidates grow with N, while the fixed round matrix and live
// candidate population stay small. The hot queries must use those small sets.
func TestReadStageCountsSealedMemberGrowth(t *testing.T) {
	for _, n := range []int{32, 64, 128, 256} {
		t.Run(fmt.Sprintf("N%d", n), func(t *testing.T) {
			ctx := t.Context()
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "stage-growth.db")+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			driver := &stageObservationDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db))}
			client := ent.NewClient(ent.Driver(driver))
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
			if _, err := store.SealRound(ctx, scope, ids); err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				item := client.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID), workitem.SegmentIDEQ(id)).OnlyX(ctx)
				candidate := client.WorkCandidate.Create().SetIdentity(fmt.Sprintf("stage-candidate-%d", id)).SetWorkItemID(item.ID).
					SetDtoVersion(1).SetSnapshotDigest("frozen").SetMode("translate").SetSourceGeneration(0).
					SetBaselineVersion(1).SetBaselineStatus("pending").SetState("ready_to_commit").SaveX(ctx)
				if err := Transaction(ctx, client, func(tx *ent.Client) error {
					if err := LockJob(ctx, tx, scope.JobID); err != nil {
						return err
					}
					facts := []Confirmation{{SegmentID: id}}
					added, err := Confirm(ctx, tx, scope.JobID, scope.RoundID, facts)
					if err != nil || added != 1 {
						return fmt.Errorf("new fact added=%d: %w", added, err)
					}
					added, err = Confirm(ctx, tx, scope.JobID, scope.RoundID, facts)
					if err != nil || added != 0 {
						return fmt.Errorf("repeated fact added=%d: %w", added, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				counts, err := ReadStageCounts(ctx, client, scope.JobID)
				if err != nil || counts.ConfirmedWork != int64(i+1) || counts.ReadyToCommit != 1 {
					t.Fatalf("member %d stages=%+v err=%v", i, counts, err)
				}
				client.WorkCandidate.UpdateOne(candidate).SetState("completed").ExecX(ctx)
			}
			queries := driver.observations()
			if len(queries) != 2*n {
				t.Fatalf("hot scalar/candidate queries=%d want=%d", len(queries), 2*n)
			}
			for _, observation := range queries[len(queries)-2:] {
				rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+observation.query, observation.args...)
				if err != nil {
					t.Fatal(err)
				}
				var plan []string
				for rows.Next() {
					var id, parent, unused int
					var detail string
					if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
						t.Fatal(err)
					}
					plan = append(plan, detail)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				_ = rows.Close()
				joined := strings.Join(plan, "; ")
				if strings.Contains(joined, "LIST SUBQUERY") || strings.Contains(joined, "SCAN work_items") || strings.Contains(joined, "SCAN job_round_segments") {
					t.Fatalf("stage observation rebuilt historical member set: %s", joined)
				}
				if strings.Contains(observation.query, "confirmed_work") {
					if !strings.Contains(observation.query, "CASE WHEN") || !strings.Contains(observation.query, "segment_completed") {
						t.Fatalf("sealed observation did not select its authoritative scalar: %s", observation.query)
					}
				} else if !strings.Contains(joined, "workcandidate_state_id") || !strings.Contains(joined, "PRIMARY KEY") {
					t.Fatalf("live candidate observation did not use state index/item primary key: %s", joined)
				}
				t.Logf("N=%d stage_plan=%s", n, joined)
			}
			actual := client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID)).CountX(ctx)
			counts, err := ReadStageCounts(ctx, client, scope.JobID)
			if err != nil || counts.ConfirmedWork != int64(actual) || counts.ReadyToCommit != 0 {
				t.Fatalf("final stages=%+v actual=%d err=%v", counts, actual, err)
			}
		})
	}
}

func TestReadStageCountsSealedRecoveryAndDeletion(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, input(candidate)); err != nil {
		t.Fatal(err)
	}
	client.JobRound.UpdateOneID(scope.RoundID).SetSegmentCompleted(0).ExecX(ctx)
	if err := Transaction(ctx, client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, scope.JobID); err != nil {
			return err
		}
		return Calibrate(ctx, tx, scope.JobID)
	}); err != nil {
		t.Fatal(err)
	}
	counts, err := ReadStageCounts(ctx, client, scope.JobID)
	if err != nil || counts.ConfirmedWork != 1 {
		t.Fatalf("recovery stages=%+v err=%v", counts, err)
	}
	if err := Transaction(ctx, client, func(tx *ent.Client) error {
		jobs, err := LockResourceJobs(ctx, tx, scope.ResourceID)
		if err != nil {
			return err
		}
		if err := BeforeDeleteSegments(ctx, tx, []int{seg.ID}); err != nil {
			return err
		}
		if err := tx.Segment.DeleteOneID(seg.ID).Exec(ctx); err != nil {
			return err
		}
		return CalibrateJobs(ctx, tx, jobs)
	}); err != nil {
		t.Fatal(err)
	}
	counts, err = ReadStageCounts(ctx, client, scope.JobID)
	if err != nil || counts.ConfirmedWork != 0 || client.JobRound.GetX(ctx, scope.RoundID).SegmentCompleted != 0 {
		t.Fatalf("deletion stages=%+v err=%v", counts, err)
	}
	client.JobRound.Delete().Where(jobround.JobIDEQ(scope.JobID)).ExecX(ctx)
	counts, err = ReadStageCounts(ctx, client, scope.JobID)
	if err != nil || counts.ConfirmedWork != 0 {
		t.Fatalf("empty round matrix stages=%+v err=%v", counts, err)
	}
}

func TestReadStageCountsUnknownACKAndRepeatedCommit(t *testing.T) {
	ctx, client, store, scope, seg, faults := memberDeltaFixture(t)
	candidate := ready(scope, seg)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	faults.loseACK.Store(true)
	for i := 0; i < 2; i++ {
		result, err := store.Commit(ctx, input(candidate))
		if err != nil || result.Outcome != AlreadyCommitted {
			t.Fatalf("commit %d=%+v err=%v", i, result, err)
		}
		counts, err := ReadStageCounts(ctx, client, scope.JobID)
		actual := client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(scope.RoundID)).CountX(ctx)
		if err != nil || counts.ConfirmedWork != 1 || counts.ConfirmedWork != int64(actual) || counts.ReadyToCommit != 0 {
			t.Fatalf("commit %d stages=%+v actual=%d err=%v", i, counts, actual, err)
		}
	}
}

func TestStageObservationSQLDialects(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			confirmed := entsql.Dialect(driver).Select().From(entsql.Table(jobround.Table))
			selectStageConfirmedWork(confirmed)
			q, args := confirmed.Query()
			if len(args) != 0 || !strings.Contains(q, "CASE WHEN") || strings.Contains(q, "?") {
				t.Fatalf("confirmed query=%s args=%v", q, args)
			}
			candidate := entsql.Dialect(driver).Select().From(entsql.Table(workcandidate.Table))
			stageCandidateJob(17)(candidate)
			cq, args := candidate.Query()
			if len(args) != 1 || args[0] != 17 || !strings.Contains(cq, "EXISTS") {
				t.Fatalf("candidate query=%s args=%v", cq, args)
			}
			if driver == dialect.Postgres && (strings.Contains(q, "`") || strings.Contains(cq, "`")) {
				t.Fatalf("PostgreSQL query contains SQLite identifier quoting: confirmed=%s candidate=%s", q, cq)
			}
		})
	}
}
