package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
)

func seedOperationSync(t *testing.T, c *ent.Client, projectID, actor int, status string, at time.Time) *ent.SyncTask {
	t.Helper()
	ctx := context.Background()
	entry, err := c.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(projectID)).First(ctx)
	if ent.IsNotFound(err) {
		entry, err = c.GlossaryEntry.Create().SetProjectID(projectID).SetSource("term").SetSourceKey("term").SetTarget("old").Save(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	task, err := c.SyncTask.Create().SetProjectID(projectID).SetActorUserID(actor).SetEntryID(entry.ID).SetOldTarget("secret-old").SetNewTarget("secret-new").SetSegmentIds("[999]").SetResourceIds("[999]").SetTotalSegments(10).SetProcessedSegments(3).SetStatus(status).SetCreatedAt(at.Add(-time.Hour)).SetUpdatedAt(at).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func operationKeys(rows []OperationRow) []string {
	result := make([]string, 0, len(rows))
	for _, r := range rows {
		result = append(result, fmt.Sprintf("%s:%d", r.TaskType, r.ID()))
	}
	return result
}
func TestOperationsMixedOrderingFiltersAndCursor(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "operation-query")
	p := createTestProject(t, c, "mixed", actor.ID)
	at := time.Date(2026, 9, 29, 1, 2, 3, 123456789, time.UTC)
	svc := NewOperationQueryService(c)
	for _, status := range []string{"pending", "running", "paused", "completed", "failed", "cancelled"} {
		seedQueryJob(t, c, p.ID, status, "manual", at)
		if status != "paused" {
			seedOperationSync(t, c, p.ID, actor.ID, status, at)
		}
	}
	// Numeric IDs 10 and 2 must retain integer order; both source tables reuse IDs.
	for range 6 {
		seedQueryJob(t, c, p.ID, "pending", "manual", at)
		seedOperationSync(t, c, p.ID, actor.ID, "pending", at)
	}
	all, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) != 23 {
		t.Fatalf("got %d", len(all.Items))
	}
	for i, row := range all.Items {
		if row.ProjectName != "mixed" {
			t.Fatal(row.ProjectName)
		}
		if i > 0 {
			prior := all.Items[i-1]
			if prior.TaskType < row.TaskType || (prior.TaskType == row.TaskType && prior.ID() < row.ID()) {
				t.Fatal("unstable mixed order")
			}
		}
	}
	for _, limit := range []int{1, 2, 7, 10} {
		var got []string
		cursor := ""
		for {
			page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all", Limit: limit, Cursor: cursor}})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, operationKeys(page.Items)...)
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		if !slices.Equal(got, operationKeys(all.Items)) {
			t.Fatalf("limit %d: %v", limit, got)
		}
	}
	for _, kind := range []string{"", OperationTranslation, OperationGlossarySync} {
		for _, status := range []string{"pending", "running", "paused", "completed", "failed", "cancelled"} {
			page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: kind, AccessibleJobListOptions: AccessibleJobListOptions{Status: status}})
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.Items {
				if kind != "" && row.TaskType != kind {
					t.Fatal("type filter")
				}
				actual := ""
				if row.Job != nil {
					actual = row.Job.Status
				} else {
					actual = row.SyncTask.Status
				}
				if actual != status {
					t.Fatal("status filter")
				}
			}
			if kind == OperationGlossarySync && status == "paused" && len(page.Items) != 0 {
				t.Fatal("sync paused")
			}
		}
	}
	page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, opts := range []OperationListOptions{
		{TaskType: "unknown"}, {AccessibleJobListOptions: AccessibleJobListOptions{TriggerType: "manual"}},
		{TaskType: OperationGlossarySync, AccessibleJobListOptions: AccessibleJobListOptions{TriggerType: "manual"}},
		{AccessibleJobListOptions: AccessibleJobListOptions{State: "all", Cursor: page.NextCursor}},
		{TaskType: OperationTranslation, AccessibleJobListOptions: AccessibleJobListOptions{Cursor: page.NextCursor}},
		{AccessibleJobListOptions: AccessibleJobListOptions{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"id":1,"updated_at":"2026-09-29T00:00:00Z"}`))}},
	} {
		if _, err := svc.List(ctx, actor.ID, opts); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("opts=%+v err=%v", opts, err)
		}
	}
	// Omitted active and explicit active are the same filter; page size may change.
	if _, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "active", Limit: 2, Cursor: page.NextCursor}}); err != nil {
		t.Fatal(err)
	}
	before := at.Add(time.Nanosecond)
	from := at.In(time.FixedZone("offset", 8*3600))
	timed, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all", UpdatedFrom: &from, UpdatedBefore: &before}})
	if err != nil || len(timed.Items) != len(all.Items) {
		t.Fatalf("nanosecond window: %v %v", timed, err)
	}
}

func TestOperationsCountsPermissionsAndRevocation(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "op-member")
	other := createTestUser(t, c, "op-other")
	admin := createTestUser(t, c, "op-admin")
	c.User.UpdateOne(admin).SetRole(SystemRoleAdmin).ExecX(ctx)
	org := c.Organization.Create().SetName("op-org").SetSlug("op-org").SaveX(ctx)
	member := c.OrgMembership.Create().SetUserID(actor.ID).SetOrganizationID(org.ID).SetRole("member").SaveX(ctx)
	p := c.Project.Create().SetName("shared").SetOwnerOrgID(org.ID).SaveX(ctx)
	own := createTestProject(t, c, "personal", other.ID)
	now := time.Date(2026, 9, 29, 0, 0, 0, 999, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	for _, projectID := range []int{p.ID, own.ID} {
		for _, status := range []string{"pending", "running", "paused"} {
			seedQueryJob(t, c, projectID, status, "manual", now.Add(-time.Minute))
		}
		seedOperationSync(t, c, projectID, other.ID, "pending", now.Add(-time.Minute))
		for _, at := range []time.Time{since.Add(-time.Nanosecond), since, now.Add(-time.Nanosecond), now} {
			seedQueryJob(t, c, projectID, "failed", "manual", at)
			seedOperationSync(t, c, projectID, other.ID, "failed", at)
		}
	}
	svc := NewOperationQueryService(c)
	summary, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != (OperationCounts{2, 1, 1, 4}) || summary.ByType.GlossarySync.Paused != 0 {
		t.Fatalf("counts: %+v", summary)
	}
	only, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{TaskType: OperationTranslation}, now)
	if err != nil || only.ByType.GlossarySync != (OperationCounts{}) {
		t.Fatalf("type counts: %+v %v", only, err)
	}
	first, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{Limit: 1}})
	if err != nil || first.NextCursor == "" {
		t.Fatal("expected continuation", err)
	}
	c.OrgMembership.DeleteOne(member).ExecX(ctx)
	empty, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{Cursor: first.NextCursor}})
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("revocation: %+v %v", empty, err)
	}
	for _, id := range []int{actor.ID, admin.ID} {
		summary, err := svc.summaryAt(ctx, id, OperationSummaryOptions{}, now)
		if err != nil || summary.Total != (OperationCounts{}) {
			t.Fatalf("unauthorized count: %+v %v", summary, err)
		}
	}
}

func TestOperationsQueriesAreBoundedAndIndexed(t *testing.T) {
	c, db, recorder := jobQueryRecordingClient(t)
	actor := createTestUser(t, c, "op-cost")
	p := createTestProject(t, c, "bounded", actor.ID)
	now := time.Now().UTC()
	for range 12 {
		seedQueryJob(t, c, p.ID, "pending", "manual", now)
		seedOperationSync(t, c, p.ID, actor.ID, "pending", now)
	}
	svc := NewOperationQueryService(c)
	recorder.queries = nil
	if _, err := svc.List(context.Background(), actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{Limit: 3}}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.queries) != 3 {
		t.Fatalf("list queries: %v", recorder.queries)
	}
	for _, q := range recorder.queries {
		for _, sensitive := range []string{"old_target", "new_target", "segment_ids", "resource_ids", "execution_config"} {
			if strings.Contains(q, sensitive) {
				t.Fatalf("heavy projection: %s", q)
			}
		}
	}
	recorder.queries = nil
	if _, err := svc.Summary(context.Background(), actor.ID, OperationSummaryOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.queries) != 2 {
		t.Fatalf("summary queries: %v", recorder.queries)
	}
	if err := c.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"EXPLAIN QUERY PLAN SELECT id FROM sync_tasks WHERE updated_at >= ? ORDER BY updated_at DESC,id DESC LIMIT 4", "EXPLAIN QUERY PLAN SELECT id FROM sync_tasks WHERE status='pending' AND updated_at >= ? ORDER BY updated_at DESC,id DESC LIMIT 4"} {
		rows, err := db.Query(q, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		indexed := false
		for rows.Next() {
			var a, b, d int
			var detail string
			if err := rows.Scan(&a, &b, &d, &detail); err != nil {
				t.Fatal(err)
			}
			indexed = indexed || strings.Contains(strings.ToUpper(detail), "USING") && strings.Contains(strings.ToUpper(detail), "INDEX")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !indexed {
			t.Fatal("sync query did not use index")
		}
	}
}

func TestOperationsPostgresTimePagination(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 3, MaxIdleConns: 2}
	db, c, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	unlock, err := database.AcquireMigrationLock(ctx, db, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Schema.Create(ctx)
	unlockErr := unlock()
	if err != nil || unlockErr != nil {
		t.Fatal(err, unlockErr)
	}
	actor := createTestUser(t, c, fmt.Sprintf("op-pg-%d", time.Now().UnixNano()))
	p := createTestProject(t, c, "op-pg", actor.ID)
	defer func() {
		c.SyncTask.Delete().Where(synctask.ProjectIDEQ(p.ID)).Exec(context.Background())
		c.Job.Delete().Where(job.ProjectIDEQ(p.ID)).Exec(context.Background())
		c.GlossaryEntry.Delete().Where(glossaryentry.ProjectIDEQ(p.ID)).Exec(context.Background())
		c.Project.DeleteOne(p).Exec(context.Background())
		c.User.DeleteOne(actor).Exec(context.Background())
	}()
	at := time.Date(2026, 9, 29, 0, 0, 0, 123456000, time.UTC)
	seedQueryJob(t, c, p.ID, "pending", "manual", at)
	seedOperationSync(t, c, p.ID, actor.ID, "pending", at)
	later := seedOperationSync(t, c, p.ID, actor.ID, "pending", at.Add(time.Microsecond))
	svc := NewOperationQueryService(c)
	cursor := ""
	var got []string
	for {
		page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{Limit: 1, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, operationKeys(page.Items)...)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(got) != 3 || got[0] != fmt.Sprintf("glossary_sync:%d", later.ID) {
		t.Fatal(got)
	}
	from, before := at.Add(time.Nanosecond), at.Add(time.Microsecond+time.Nanosecond)
	page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{UpdatedFrom: &from, UpdatedBefore: &before}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID() != later.ID {
		t.Fatalf("microsecond boundary: %+v %v", page, err)
	}
}
