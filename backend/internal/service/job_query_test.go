package service

import (
	"context"
	stdsql "database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func seedQueryJob(t *testing.T, client *ent.Client, projectID int, status, trigger string, updated time.Time) *ent.Job {
	t.Helper()
	row, err := client.Job.Create().
		SetProjectID(projectID).
		SetExecutionPlanID(1).
		SetStatus(status).
		SetTriggerType(trigger).
		SetCreatedAt(updated.Add(-time.Hour)).
		SetUpdatedAt(updated).
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	return row
}

func queryJobIDs(rows []*ent.Job) []int {
	ids := make([]int, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func assertQueryJobIDs(t *testing.T, page *AccessibleJobPage, want ...int) {
	t.Helper()
	got := queryJobIDs(page.Items)
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("job IDs = %v, want %v", got, want)
	}
	if page.Items == nil {
		t.Fatal("items must be a non-nil slice, including empty results")
	}
}

func TestListAccessibleJobsFilters(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	svc := newJobRoundTestService(client, nil)
	actor := createTestUser(t, client, "query-filter")
	project := createTestProject(t, client, "first", actor.ID)
	otherProject := createTestProject(t, client, "second", actor.ID)
	base := time.Date(2026, 9, 20, 12, 0, 0, 123456789, time.UTC)
	statuses := []string{JobStatusPending, JobStatusRunning, JobStatusPaused, JobStatusCompleted, JobStatusFailed, JobStatusCancelled}
	rows := make([]*ent.Job, len(statuses))
	for i, status := range statuses {
		rows[i] = seedQueryJob(t, client, project.ID, status, "manual", base.Add(time.Duration(i)*time.Second))
	}
	fileJob := seedQueryJob(t, client, otherProject.ID, JobStatusRunning, "file_update", base.Add(6*time.Second))
	glossaryJob := seedQueryJob(t, client, otherProject.ID, JobStatusPending, "glossary_change", base.Add(7*time.Second))
	webJob := seedQueryJob(t, client, otherProject.ID, JobStatusPaused, "web_edit", base.Add(8*time.Second))
	from, before := base.Add(time.Second), base.Add(4*time.Second)
	tests := []struct {
		name string
		opts AccessibleJobListOptions
		want []int
	}{
		{"default includes paused", AccessibleJobListOptions{}, []int{webJob.ID, glossaryJob.ID, fileJob.ID, rows[2].ID, rows[1].ID, rows[0].ID}},
		{"explicit active", AccessibleJobListOptions{State: "active", ProjectID: project.ID}, []int{rows[2].ID, rows[1].ID, rows[0].ID}},
		{"terminal", AccessibleJobListOptions{State: "terminal"}, []int{rows[5].ID, rows[4].ID, rows[3].ID}},
		{"all", AccessibleJobListOptions{State: "all"}, []int{webJob.ID, glossaryJob.ID, fileJob.ID, rows[5].ID, rows[4].ID, rows[3].ID, rows[2].ID, rows[1].ID, rows[0].ID}},
		{"combined half-open time window", AccessibleJobListOptions{State: "all", ProjectID: project.ID, TriggerType: "manual", UpdatedFrom: &from, UpdatedBefore: &before}, []int{rows[3].ID, rows[2].ID, rows[1].ID}},
		{"status excludes default active filter", AccessibleJobListOptions{Status: JobStatusFailed}, []int{rows[4].ID}},
		{"lower bound only", AccessibleJobListOptions{State: "all", ProjectID: project.ID, UpdatedFrom: &before}, []int{rows[5].ID, rows[4].ID}},
		{"upper bound only", AccessibleJobListOptions{State: "all", UpdatedBefore: &from}, []int{rows[0].ID}},
		{"unknown project", AccessibleJobListOptions{ProjectID: 999999}, nil},
		{"file trigger", AccessibleJobListOptions{TriggerType: "file_update"}, []int{fileJob.ID}},
		{"glossary trigger", AccessibleJobListOptions{TriggerType: "glossary_change"}, []int{glossaryJob.ID}},
		{"web trigger", AccessibleJobListOptions{TriggerType: "web_edit"}, []int{webJob.ID}},
	}
	for i, status := range statuses {
		tests = append(tests, struct {
			name string
			opts AccessibleJobListOptions
			want []int
		}{"status " + status, AccessibleJobListOptions{Status: status, ProjectID: project.ID}, []int{rows[i].ID}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, err := svc.ListAccessibleJobs(ctx, actor.ID, tt.opts)
			if err != nil {
				t.Fatalf("ListAccessibleJobs: %v", err)
			}
			assertQueryJobIDs(t, page, tt.want...)
			if page.NextCursor != "" {
				t.Fatalf("unexpected next cursor %q on final page", page.NextCursor)
			}
		})
	}
}

func TestListAccessibleJobsRejectsInvalidOptions(t *testing.T) {
	client := testClient(t)
	svc := newJobRoundTestService(client, nil)
	before := time.Now().UTC()
	after := before.Add(time.Second)
	tests := []struct {
		name string
		opts AccessibleJobListOptions
	}{
		{"invalid state", AccessibleJobListOptions{State: "recent"}},
		{"invalid status", AccessibleJobListOptions{Status: "awaiting_review"}},
		{"invalid trigger", AccessibleJobListOptions{TriggerType: "scheduler"}},
		{"state and status conflict", AccessibleJobListOptions{State: "active", Status: "pending"}},
		{"all and status conflict", AccessibleJobListOptions{State: "all", Status: "failed"}},
		{"negative limit", AccessibleJobListOptions{Limit: -1}},
		{"excessive limit", AccessibleJobListOptions{Limit: 101}},
		{"negative project", AccessibleJobListOptions{ProjectID: -1}},
		{"equal time bounds", AccessibleJobListOptions{UpdatedFrom: &before, UpdatedBefore: &before}},
		{"reversed time bounds", AccessibleJobListOptions{UpdatedFrom: &after, UpdatedBefore: &before}},
		{"legacy numeric cursor", AccessibleJobListOptions{Cursor: "12345"}},
		{"malformed base64 cursor", AccessibleJobListOptions{Cursor: "!!invalid!!"}},
		{"empty cursor object", AccessibleJobListOptions{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{}`))}},
		{"invalid cursor json", AccessibleJobListOptions{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`not json`))}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.ListAccessibleJobs(context.Background(), 1, tt.opts)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want ErrInvalidInput", err)
			}
		})
	}
	for _, opts := range []JobSummaryOptions{{ProjectID: -1}, {TriggerType: "scheduler"}} {
		if _, err := svc.GetJobsSummary(context.Background(), 1, opts); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("summary options %+v: error = %v, want ErrInvalidInput", opts, err)
		}
	}
}

func TestListAccessibleJobsPermissionsMatchProjectAccess(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	svc := newJobRoundTestService(client, nil)
	actor := createTestUser(t, client, "query-member")
	other := createTestUser(t, client, "query-other")
	admin := createTestUser(t, client, "query-system-admin")
	if err := client.User.UpdateOneID(admin.ID).SetRole("admin").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	org, err := client.Organization.Create().SetName("query-org").SetSlug("query-org").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(actor.ID).SetRole("member").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	own := createTestProject(t, client, "owned", actor.ID)
	foreign := createTestProject(t, client, "foreign", other.ID)
	shared, err := client.Project.Create().SetName("shared").SetOwnerOrgID(org.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := client.Project.Create().SetName("personal-owner-takes-precedence").SetOwnerUserID(other.ID).SetOwnerOrgID(org.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := client.Project.Create().SetName("no-owner").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Minute)
	projects := []*ent.Project{own, foreign, shared, conflict, orphan}
	jobs := make(map[int]*ent.Job)
	for i, project := range projects {
		row := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base.Add(time.Duration(i)*time.Second))
		if err := client.Job.UpdateOneID(row.ID).SetCreatedByID(other.ID).SetUpdatedAt(row.UpdatedAt).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		jobs[project.ID] = row
	}
	for _, role := range []string{"member", "admin", "owner", "invalid-role"} {
		t.Run(role, func(t *testing.T) {
			if err := client.OrgMembership.UpdateOneID(membership.ID).SetRole(role).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := []int{jobs[own.ID].ID}
			if role != "invalid-role" {
				want = append([]int{jobs[shared.ID].ID}, want...)
			}
			assertQueryJobIDs(t, page, want...)
			projectRows, err := svc.projects.ListProjectsForUser(ctx, actor.ID)
			if err != nil {
				t.Fatal(err)
			}
			visible := map[int]bool{}
			for _, p := range projectRows {
				visible[p.ID] = true
			}
			for _, p := range projects {
				_, accessErr := svc.projects.GetProject(ctx, actor.ID, p.ID)
				if visible[p.ID] != (accessErr == nil) {
					t.Errorf("project %d: list visibility = %v, access error = %v", p.ID, visible[p.ID], accessErr)
				}
				filtered, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{ProjectID: p.ID})
				if err != nil {
					t.Fatal(err)
				}
				if (len(filtered.Items) == 1) != visible[p.ID] {
					t.Errorf("project %d: job visibility differs from project access", p.ID)
				}
			}
			counts, err := svc.GetJobsSummary(ctx, actor.ID, JobSummaryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if counts.Pending != len(want) {
				t.Errorf("pending count = %d, want %d", counts.Pending, len(want))
			}
		})
	}
	page, err := svc.ListAccessibleJobs(ctx, admin.ID, AccessibleJobListOptions{State: "all"})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, page)
	counts, err := svc.GetJobsSummary(ctx, admin.ID, JobSummaryOptions{})
	if err != nil || counts.Pending != 0 {
		t.Fatalf("system admin counts = %+v, err = %v; want no implicit project access", counts, err)
	}
	// The personal owner still has access when both owner fields are populated.
	page, err = svc.ListAccessibleJobs(ctx, other.ID, AccessibleJobListOptions{ProjectID: conflict.ID})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, page, jobs[conflict.ID].ID)
}

func TestListAccessibleJobsPaginationPrecisionAndUpdates(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	svc := newJobRoundTestService(client, nil)
	actor := createTestUser(t, client, "query-pagination")
	project := createTestProject(t, client, "pagination", actor.ID)
	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	oldest := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base.Add(100*time.Nanosecond))
	middle := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base.Add(200*time.Nanosecond))
	tieLow := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base.Add(300*time.Nanosecond))
	tieHigh := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base.Add(300*time.Nanosecond))
	var cursor string
	for i, want := range []int{tieHigh.ID, tieLow.ID, middle.ID, oldest.ID} {
		page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		assertQueryJobIDs(t, page, want)
		if i < 3 && page.NextCursor == "" {
			t.Fatalf("page %d missing cursor", i)
		}
		if i == 3 && page.NextCursor != "" {
			t.Fatal("last page must have no cursor")
		}
		cursor = page.NextCursor
	}
	first, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, first, tieHigh.ID, tieLow.ID)
	if err := client.Job.UpdateOneID(oldest.ID).SetUpdatedAt(base.Add(time.Hour)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, second, middle.ID)
	refreshed, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, refreshed, oldest.ID, tieHigh.ID)
}

func TestListAccessibleJobsRechecksAccessOnEachPage(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	svc := newJobRoundTestService(client, nil)
	actor := createTestUser(t, client, "revoked-member")
	org, err := client.Organization.Create().SetName("revoke-org").SetSlug("revoke-org").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(actor.ID).SetRole("member").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.Project.Create().SetName("revoked-project").SetOwnerOrgID(org.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		seedQueryJob(t, client, project.ID, JobStatusPending, "manual", time.Now().UTC().Add(time.Duration(i)*time.Second))
	}
	first, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, error = %v", first, err)
	}
	if err := client.OrgMembership.DeleteOneID(membership.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, second)
	if second.NextCursor != "" {
		t.Fatal("inaccessible continuation must not disclose another cursor")
	}
	counts, err := svc.GetJobsSummary(ctx, actor.ID, JobSummaryOptions{})
	if err != nil || counts.Pending != 0 {
		t.Fatalf("after membership removal: counts = %+v, error = %v", counts, err)
	}
}

func TestListAccessibleJobsTimestampZonesAndMonotonicValues(t *testing.T) {
	t.Run("equivalent RFC3339 offsets", func(t *testing.T) {
		client := testClient(t)
		ctx := context.Background()
		svc := newJobRoundTestService(client, nil)
		actor := createTestUser(t, client, "query-timezones")
		project := createTestProject(t, client, "timezones", actor.ID)
		base := time.Date(2026, 9, 20, 12, 0, 0, 123456789, time.UTC)
		east := time.FixedZone("CST", 8*60*60)
		west := time.FixedZone("PDT", -7*60*60)
		oldest := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.Add(-time.Nanosecond))
		utcJob := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base)
		eastJob := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.In(east))
		westJob := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.In(west))
		newest := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.Add(time.Nanosecond).In(east))
		var cursor string
		for _, want := range []int{newest.ID, westJob.ID, eastJob.ID, utcJob.ID, oldest.ID} {
			page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", Limit: 1, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			assertQueryJobIDs(t, page, want)
			cursor = page.NextCursor
		}
		if cursor != "" {
			t.Fatal("last timezone page must have no continuation")
		}
		from, before := base.In(west), base.Add(time.Nanosecond).In(east)
		page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", UpdatedFrom: &from, UpdatedBefore: &before})
		if err != nil {
			t.Fatal(err)
		}
		assertQueryJobIDs(t, page, westJob.ID, eastJob.ID, utcJob.ID)
		counts, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, before)
		if err != nil || counts.RecentFailed != 4 {
			t.Fatalf("timezone summary = %+v, error = %v, want 4 failures before upper bound", counts, err)
		}
	})
	t.Run("native time.Now persisted values", func(t *testing.T) {
		client := testClient(t)
		ctx := context.Background()
		svc := newJobRoundTestService(client, nil)
		actor := createTestUser(t, client, "query-monotonic")
		project := createTestProject(t, client, "monotonic", actor.ID)
		base := time.Now()
		oldest := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base.Add(-time.Nanosecond))
		tieLow := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base)
		tieHigh := seedQueryJob(t, client, project.ID, JobStatusPending, "manual", base)
		var cursor string
		for _, want := range []int{tieHigh.ID, tieLow.ID, oldest.ID} {
			page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 1, Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			assertQueryJobIDs(t, page, want)
			cursor = page.NextCursor
		}
		if cursor != "" {
			t.Fatal("last native timestamp page must have no continuation")
		}
	})
}

func TestJobsSummaryWindowCountsAndRetry(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	svc := newJobRoundTestService(client, nil)
	actor := createTestUser(t, client, "query-counts")
	project := createTestProject(t, client, "counts", actor.ID)
	otherProject := createTestProject(t, client, "counts-second", actor.ID)
	asOf := time.Date(2026, 9, 29, 12, 30, 0, 123456789, time.UTC)
	since := asOf.Add(-7 * 24 * time.Hour)
	for i := range 101 {
		seedQueryJob(t, client, project.ID, JobStatusPending, "manual", since.Add(-time.Duration(i+1)*time.Hour))
	}
	seedQueryJob(t, client, project.ID, JobStatusRunning, "manual", since.Add(-time.Hour))
	seedQueryJob(t, client, project.ID, JobStatusPaused, "manual", since.Add(-time.Hour))
	seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", since.Add(-time.Nanosecond))
	seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", since)
	seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", asOf.Add(-time.Nanosecond))
	seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", asOf)
	seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", asOf.Add(time.Nanosecond))
	seedQueryJob(t, client, otherProject.ID, JobStatusRunning, "file_update", since)
	seedQueryJob(t, client, otherProject.ID, JobStatusFailed, "file_update", since)
	seedQueryJob(t, client, project.ID, JobStatusCompleted, "manual", since)
	retryJob, _ := seedJobCancelRetry(t, client, project.ID, JobStatusFailed, []string{JobResourceStatusFailed})
	if err := client.Job.UpdateOneID(retryJob.ID).SetUpdatedAt(asOf.Add(-time.Hour)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	counts, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pending != 101 || counts.Running != 2 || counts.Paused != 1 || counts.RecentFailed != 4 {
		t.Fatalf("counts = %+v, want pending=101 running=2 paused=1 recent_failed=4", counts)
	}
	if !counts.AsOf.Equal(asOf) || !counts.RecentFailedSince.Equal(since) {
		t.Fatalf("incorrect summary window: %+v", counts)
	}
	defaultPage, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{})
	if err != nil || len(defaultPage.Items) != 50 || defaultPage.NextCursor == "" {
		t.Fatalf("default pagination: page = %+v, error = %v", defaultPage, err)
	}
	maxPage, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: 100})
	if err != nil || len(maxPage.Items) != 100 || maxPage.NextCursor == "" {
		t.Fatalf("maximum pagination: page = %+v, error = %v", maxPage, err)
	}
	filtered, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{ProjectID: otherProject.ID, TriggerType: "file_update"}, asOf)
	if err != nil || filtered.Pending != 0 || filtered.Running != 1 || filtered.Paused != 0 || filtered.RecentFailed != 1 {
		t.Fatalf("filtered summary = %+v, error = %v", filtered, err)
	}
	failedPage, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Status: JobStatusFailed, UpdatedFrom: &counts.RecentFailedSince, UpdatedBefore: &counts.AsOf})
	if err != nil || len(failedPage.Items) != counts.RecentFailed {
		t.Fatalf("failed list does not match summary window: page = %+v, summary = %+v, err = %v", failedPage, counts, err)
	}
	if _, err := svc.RetryJob(ctx, actor.ID, retryJob.ID); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	after, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, asOf)
	if err != nil || after.RecentFailed != 3 || after.Pending != 102 {
		t.Fatalf("retry must remove a current failure: summary = %+v, err = %v", after, err)
	}
	missing, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{ProjectID: 999999}, asOf)
	if err != nil || missing.Pending != 0 || missing.Running != 0 || missing.Paused != 0 || missing.RecentFailed != 0 {
		t.Fatalf("missing project summary = %+v, err = %v", missing, err)
	}
}

type jobQueryRecorder struct {
	dialect.Driver
	queries []string
	args    [][]any
}

func (d *jobQueryRecorder) Query(ctx context.Context, query string, args, v any) error {
	d.queries = append(d.queries, query)
	if values, ok := args.([]any); ok {
		d.args = append(d.args, append([]any(nil), values...))
	} else {
		d.args = append(d.args, nil)
	}
	return d.Driver.Query(ctx, query, args, v)
}

func jobQueryRecordingClient(t *testing.T) (*ent.Client, *stdsql.DB, *jobQueryRecorder) {
	t.Helper()
	db, err := stdsql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	recorder := &jobQueryRecorder{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db))}
	client := ent.NewClient(ent.Driver(recorder))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	return client, db, recorder
}

func TestListAccessibleJobsUsesBoundedLightweightQueries(t *testing.T) {
	client, _, recorder := jobQueryRecordingClient(t)
	ctx := context.Background()
	svc := newJobRoundTestService(client, nil)
	actor := createTestUser(t, client, "lightweight-query")
	for i := range 12 {
		project := createTestProject(t, client, fmt.Sprintf("projection-%d", i), actor.ID)
		row := seedQueryJob(t, client, project.ID, JobStatusRunning, "manual", time.Now().UTC())
		started := row.CreatedAt
		if err := client.Job.UpdateOneID(row.ID).
			SetExecutionConfig(map[string]any{"secret": strings.Repeat("large configuration", 1000)}).
			SetErrorMessage("private detail").SetStartedAt(started).
			SetResourceCount(8).SetCompletedResources(3).SetFailedResources(1).
			SetProgressTotal(100).SetProgressCompleted(40).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{1, 12} {
		recorder.queries = nil
		page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(recorder.queries) != 2 {
			t.Fatalf("limit %d: %d queries, want one job and one project query: %v", limit, len(recorder.queries), recorder.queries)
		}
		for _, query := range recorder.queries {
			for _, unwanted := range []string{"execution_config", "error_message", "job_resources", "job_rounds", "sse_events"} {
				if strings.Contains(query, unwanted) {
					t.Errorf("list reads detail field/table %q: %s", unwanted, query)
				}
			}
		}
		for _, row := range page.Items {
			if row.ExecutionConfig != nil || row.ErrorMessage != nil || row.Edges.CreatedBy != nil || row.Edges.JobResources != nil || row.Edges.JobRounds != nil {
				t.Errorf("list eagerly loaded private configuration or detail edges for job %d", row.ID)
			}
			if row.Edges.Project == nil || row.Edges.Project.ID != row.ProjectID || row.Edges.Project.Name == "" || row.Edges.Project.Config != nil {
				t.Errorf("project must load only id/name: %+v", row.Edges.Project)
			}
			if row.ResourceCount != 8 || row.CompletedResources != 3 || row.FailedResources != 1 || row.ProgressTotal != 100 || row.ProgressCompleted != 40 || row.StartedAt == nil {
				t.Errorf("summary lost progress fields: %+v", row)
			}
		}
	}
	recorder.queries = nil
	counts, err := svc.GetJobsSummary(ctx, actor.ID, JobSummaryOptions{})
	if err != nil || counts.Running != 12 {
		t.Fatalf("summary = %+v, err = %v", counts, err)
	}
	if len(recorder.queries) != 1 || !strings.Contains(strings.ToUpper(recorder.queries[0]), "COUNT(") {
		t.Fatalf("summary must aggregate in one database query: %v", recorder.queries)
	}
}

func TestJobsQueryIndexesSurviveRepeatedMigration(t *testing.T) {
	client, db, _ := jobQueryRecordingClient(t)
	ctx := context.Background()
	for range 2 {
		if err := client.Schema.Create(ctx); err != nil {
			t.Fatalf("repeat schema migration: %v", err)
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT i.name, p.name FROM pragma_index_list('jobs') AS i JOIN pragma_index_info(i.name) AS p ORDER BY i.name, p.seqno`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	indexes := map[string][]string{}
	for rows.Next() {
		var indexName, column string
		if err := rows.Scan(&indexName, &column); err != nil {
			t.Fatal(err)
		}
		indexes[indexName] = append(indexes[indexName], column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{{"project_id", "id"}, {"updated_at", "id"}, {"status", "updated_at", "id"}} {
		found := false
		for _, columns := range indexes {
			if reflect.DeepEqual(columns, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing job index %v; have %v", want, indexes)
		}
	}
}
