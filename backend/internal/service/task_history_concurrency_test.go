package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

// Observe the actual competing SQL attempt, rather than relying on sleeps or
// a goroutine merely having started. Both Ent mutations and raw lock SQL pass here.
type historySQLWatch struct {
	table     string
	attempted chan struct{}
	once      sync.Once
}

type historyObservedDriver struct {
	dialect.Driver
	watch atomic.Pointer[historySQLWatch]
}

func (d *historyObservedDriver) observe(query string) {
	if watch := d.watch.Load(); watch != nil && strings.HasPrefix(strings.TrimSpace(query), "UPDATE") && strings.Contains(query, watch.table) {
		watch.once.Do(func() { close(watch.attempted) })
	}
}

func (d *historyObservedDriver) Exec(ctx context.Context, query string, args, value any) error {
	d.observe(query)
	return d.Driver.Exec(ctx, query, args, value)
}
func (d *historyObservedDriver) Query(ctx context.Context, query string, args, value any) error {
	d.observe(query)
	return d.Driver.Query(ctx, query, args, value)
}
func (d *historyObservedDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &historyObservedTx{Tx: tx, driver: d}, nil
}
func (d *historyObservedDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	tx, err := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &historyObservedTx{Tx: tx, driver: d}, nil
}
func (d *historyObservedDriver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.observe(query)
	return d.Driver.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}).ExecContext(ctx, query, args...)
}

type historyObservedTx struct {
	dialect.Tx
	driver *historyObservedDriver
}

func (t *historyObservedTx) Exec(ctx context.Context, query string, args, value any) error {
	t.driver.observe(query)
	return t.Tx.Exec(ctx, query, args, value)
}
func (t *historyObservedTx) Query(ctx context.Context, query string, args, value any) error {
	t.driver.observe(query)
	return t.Tx.Query(ctx, query, args, value)
}
func (t *historyObservedTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.driver.observe(query)
	return t.Tx.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}).ExecContext(ctx, query, args...)
}

type historyRaceFixture struct {
	left, right                *ent.Client
	leftDriver, rightDriver    *historyObservedDriver
	owner, actor, org, project int
	target                     HistoryTarget
	policy                     TaskRetentionPolicy
}

func newHistoryRaceFixture(t *testing.T, dialectName string) historyRaceFixture {
	t.Helper()
	var open func() *sql.DB
	if dialectName == dialect.Postgres {
		dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("PostgreSQL history transaction races unverified: LINGUAFLOW_TEST_POSTGRES_DSN is not set")
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid PostgreSQL test DSN")
		}
		admin := stdlib.OpenDB(*cfg)
		schema := fmt.Sprintf("history_race_%d", time.Now().UnixNano())
		quoted := pgx.Identifier{schema}.Sanitize()
		if _, err := admin.ExecContext(context.Background(), "CREATE SCHEMA "+quoted); err != nil {
			admin.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
				t.Errorf("remove history test schema: %v", err)
			}
			_ = admin.Close()
		})
		open = func() *sql.DB {
			copy := cfg.Copy()
			copy.RuntimeParams["search_path"] = schema
			copy.RuntimeParams["timezone"] = "UTC"
			return stdlib.OpenDB(*copy)
		}
	} else {
		dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "history.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
		open = func() *sql.DB {
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			return db
		}
	}
	client := func() (*ent.Client, *historyObservedDriver) {
		db := open()
		db.SetMaxOpenConns(2)
		driver := &historyObservedDriver{Driver: database.NewDriver(entsql.OpenDB(dialectName, db))}
		c := ent.NewClient(ent.Driver(driver))
		t.Cleanup(func() { _ = c.Close() })
		return c, driver
	}
	left, ld := client()
	right, rd := client()
	ctx := context.Background()
	if err := left.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewInitializationService(left).Initialize(ctx, config.ModeServer, bootstrapForTest("history-race-owner", false)); err != nil {
		t.Fatal(err)
	}
	owner := left.User.Query().OnlyX(ctx)
	actor := createTestUser(t, left, "history-race-actor")
	users := NewUserService(left, nil)
	org, err := users.CreateOrganization(ctx, owner.ID, CreateOrganizationInput{Name: "History races", Slug: "history-races"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.AddMember(ctx, owner.ID, org.ID, AddOrgMemberInput{Username: actor.Username, Role: organizationRole(OrgRoleAdmin)}); err != nil {
		t.Fatal(err)
	}
	project := left.Project.Create().SetName("History race project").SetOwnerOrgID(org.ID).SaveX(ctx)
	anchor := historyTestNow.Add(-40 * 24 * time.Hour)
	task := left.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("completed").SetFinishedAt(anchor).SetRetentionAnchorAt(anchor).SaveX(ctx)
	left.SSEEvent.Create().SetJobID(task.ID).SetSeq(1).SetType("batch").SetLevel("info").SetMessage("retained until deletion commits").SaveX(ctx)
	settings, err := NewSettingsService(left).Patch(ctx, owner.ID, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: true, RetentionDays: 30, ExpectedRevision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	return historyRaceFixture{left, right, ld, rd, owner.ID, actor.ID, org.ID, project.ID, HistoryTarget{OperationTranslation, strconv.Itoa(task.ID), project.ID}, settings.TaskRetention}
}

func (f historyRaceFixture) history(client *ent.Client) *TaskHistoryService {
	s := NewTaskHistoryService(client, NewProjectService(client, NewUserService(client, nil)), &tasklife.Coordinator{}, nil)
	s.SetReady(true)
	s.SetLogger(discardLogger())
	s.now = func() time.Time { return historyTestNow }
	return s
}
func (f historyRaceFixture) retention() *retentionDeletion {
	return &retentionDeletion{Policy: f.policy, Cutoff: historyTestNow.Add(-30 * 24 * time.Hour), ScanID: "concurrent-scan"}
}

func historyRaceAwait(t *testing.T, ctx context.Context, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func historyRaceResult(t *testing.T, ctx context.Context, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return ctx.Err()
	}
}
func historyRaceObserve(driver *historyObservedDriver, table string) <-chan struct{} {
	watch := &historySQLWatch{table: table, attempted: make(chan struct{})}
	driver.watch.Store(watch)
	return watch.attempted
}
func historyRaceAuditGate(client *ent.Client, action string) (<-chan struct{}, func()) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	client.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if got, _ := m.(*ent.ActivityLogMutation).Action(); got == action {
				enterOnce.Do(func() { close(entered) })
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	return entered, func() { releaseOnce.Do(func() { close(release) }) }
}

func TestTaskHistorySQLiteTransactionCompetition(t *testing.T) {
	runHistoryTransactionRaces(t, dialect.SQLite)
}
func TestTaskHistoryPostgresTransactionCompetition(t *testing.T) {
	runHistoryTransactionRaces(t, dialect.Postgres)
}

func TestTaskHistorySQLiteWriteLockDeadline(t *testing.T) {
	f := newHistoryRaceFixture(t, dialect.SQLite)
	ctx := context.Background()
	tx, err := f.left.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE projects SET updated_at=updated_at WHERE id=$1", f.project); err != nil {
		t.Fatal(err)
	}
	svc := f.history(f.right)
	svc.deleteBudget = 25 * time.Millisecond
	started := time.Now()
	err = svc.Delete(ctx, f.actor, f.target)
	t.Logf("SQLite competing write lock including cancellation and rollback: %s", time.Since(started))
	if !errors.Is(err, ErrTaskCleanupDeferred) {
		t.Fatalf("write lock deadline=%v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if f.left.Job.Query().CountX(ctx) != 1 || f.left.SSEEvent.Query().CountX(ctx) != 1 || f.left.ActivityLog.Query().Where(activitylog.ActionEQ("job.history_deleted")).ExistX(ctx) {
		t.Fatal("cancelled lock wait changed history or audit")
	}
}

func runHistoryTransactionRaces(t *testing.T, dialectName string) {
	for _, scenario := range []string{"policy_close_first", "policy_extend_first", "delete_before_policy", "revoke_first", "delete_before_revoke", "barrier_first", "delete_before_barrier"} {
		t.Run(scenario, func(t *testing.T) {
			f := newHistoryRaceFixture(t, dialectName)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			leftResult, rightResult := make(chan error, 1), make(chan error, 1)
			var wantDeleteErr error
			switch scenario {
			case "policy_close_first", "policy_extend_first":
				entered, release := historyRaceAuditGate(f.left, "admin.task_retention.update")
				defer release()
				patch := TaskRetentionPatch{Enabled: false, RetentionDays: 30, ExpectedRevision: f.policy.Revision}
				if scenario == "policy_extend_first" {
					patch.Enabled = true
					patch.RetentionDays = 90
				}
				go func() {
					_, err := NewSettingsService(f.left).Patch(ctx, f.owner, SettingsPatch{TaskRetention: &patch})
					leftResult <- err
				}()
				historyRaceAwait(t, ctx, entered)
				attempted := historyRaceObserve(f.rightDriver, "system_settings")
				go func() { rightResult <- f.history(f.right).delete(ctx, 0, f.target, f.retention()) }()
				historyRaceAwait(t, ctx, attempted)
				release()
				if err := historyRaceResult(t, ctx, leftResult); err != nil {
					t.Fatal(err)
				}
				wantDeleteErr = ErrSettingsConflict
			case "delete_before_policy":
				entered, release := historyRaceAuditGate(f.left, "job.history_deleted")
				defer release()
				go func() { leftResult <- f.history(f.left).delete(ctx, 0, f.target, f.retention()) }()
				historyRaceAwait(t, ctx, entered)
				attempted := historyRaceObserve(f.rightDriver, "system_settings")
				go func() {
					_, err := NewSettingsService(f.right).Patch(ctx, f.owner, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: false, RetentionDays: 30, ExpectedRevision: f.policy.Revision}})
					rightResult <- err
				}()
				historyRaceAwait(t, ctx, attempted)
				release()
				if err := historyRaceResult(t, ctx, rightResult); err != nil {
					t.Fatal(err)
				}
				rightResult = leftResult
			case "revoke_first":
				entered, release := historyRaceAuditGate(f.left, "organization.member.remove")
				defer release()
				go func() { leftResult <- NewUserService(f.left, nil).RemoveMember(ctx, f.owner, f.org, f.actor) }()
				historyRaceAwait(t, ctx, entered)
				attempted := historyRaceObserve(f.rightDriver, "organizations")
				go func() { rightResult <- f.history(f.right).Delete(ctx, f.actor, f.target) }()
				historyRaceAwait(t, ctx, attempted)
				release()
				if err := historyRaceResult(t, ctx, leftResult); err != nil {
					t.Fatal(err)
				}
				wantDeleteErr = ErrForbidden
			case "delete_before_revoke":
				entered, release := historyRaceAuditGate(f.left, "job.history_deleted")
				defer release()
				go func() { leftResult <- f.history(f.left).Delete(ctx, f.actor, f.target) }()
				historyRaceAwait(t, ctx, entered)
				attempted := historyRaceObserve(f.rightDriver, "organizations")
				go func() { rightResult <- NewUserService(f.right, nil).RemoveMember(ctx, f.owner, f.org, f.actor) }()
				historyRaceAwait(t, ctx, attempted)
				release()
				if err := historyRaceResult(t, ctx, rightResult); err != nil {
					t.Fatal(err)
				}
				rightResult = leftResult
			case "barrier_first":
				tx, err := f.left.Tx(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err = tx.Project.UpdateOneID(f.project).SetStorageState("draining").AddStorageGeneration(1).Exec(ctx); err != nil {
					t.Fatal(err)
				}
				lockTable := "projects"
				if dialectName == dialect.SQLite {
					lockTable = "organizations"
				}
				attempted := historyRaceObserve(f.rightDriver, lockTable)
				go func() { rightResult <- f.history(f.right).Delete(ctx, f.actor, f.target) }()
				historyRaceAwait(t, ctx, attempted)
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				wantDeleteErr = ErrStorageMaintenance
			case "delete_before_barrier":
				entered, release := historyRaceAuditGate(f.left, "job.history_deleted")
				defer release()
				go func() { leftResult <- f.history(f.left).Delete(ctx, f.actor, f.target) }()
				historyRaceAwait(t, ctx, entered)
				attempted := historyRaceObserve(f.rightDriver, "projects")
				go func() {
					rightResult <- withOrganizationTransaction(ctx, f.right, func(tx *ent.Client) error {
						return tx.Project.UpdateOneID(f.project).SetStorageState("draining").AddStorageGeneration(1).Exec(ctx)
					})
				}()
				historyRaceAwait(t, ctx, attempted)
				release()
				if err := historyRaceResult(t, ctx, rightResult); err != nil {
					t.Fatal(err)
				}
				rightResult = leftResult
			}
			deleteErr := historyRaceResult(t, ctx, rightResult)
			if !errors.Is(deleteErr, wantDeleteErr) {
				t.Fatalf("delete error=%v want %v", deleteErr, wantDeleteErr)
			}
			id, _ := strconv.Atoi(f.target.ID)
			exists := f.left.Job.Query().Where(job.IDEQ(id)).ExistX(ctx)
			if exists != (wantDeleteErr != nil) {
				t.Fatalf("job exists=%v, deletion error=%v", exists, deleteErr)
			}
			audits := f.left.ActivityLog.Query().Where(activitylog.ActionEQ("job.history_deleted")).CountX(ctx)
			if wantDeleteErr != nil && audits != 0 || wantDeleteErr == nil && audits != 1 {
				t.Fatalf("unexpected deletion audits=%d", audits)
			}
			if remaining := f.left.SSEEvent.Query().CountX(ctx); wantDeleteErr != nil && remaining != 1 || wantDeleteErr == nil && remaining != 0 {
				t.Fatalf("event history partially removed: %d", remaining)
			}
			if strings.Contains(scenario, "barrier") && f.left.Project.GetX(ctx, f.project).StorageState != "draining" {
				t.Fatal("history deletion changed the barrier")
			}
		})
	}
}
