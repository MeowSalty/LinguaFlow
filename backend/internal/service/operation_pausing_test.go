package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestOperationsPausingDiscoveryMatchesJobs(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "pausing-filters")
	p := createTestProject(t, c, "pausing-filters", actor.ID)
	at := time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.UTC)
	active := map[string][]string{
		OperationTranslation:  {"pending", "running", "pausing", "paused"},
		OperationGlossarySync: {"pending", "running"},
		OperationStorage:      {"pending", "running", "waiting_retry", "needs_action"},
	}
	terminal := []string{"completed", "failed", "cancelled"}
	seeded := map[string]map[string]string{}
	for kind, statuses := range active {
		seeded[kind] = map[string]string{}
		for _, status := range append(slices.Clone(statuses), terminal...) {
			var id int
			switch kind {
			case OperationTranslation:
				id = seedQueryJob(t, c, p.ID, status, JobTriggerManual, at).ID
			case OperationGlossarySync:
				id = seedOperationSync(t, c, p.ID, actor.ID, status, at).ID
			case OperationStorage:
				id = seedOperationStorage(t, c, p.ID, actor.ID, status, at).ID
			}
			seeded[kind][status] = fmt.Sprintf("%s:%d", kind, id)
		}
	}
	svc := NewOperationQueryService(c)
	jobs := newJobRoundTestService(t, c, nil)
	filters := []AccessibleJobListOptions{{}, {State: "active"}, {State: "all"}, {State: "terminal"}}
	for _, status := range []string{"pending", "running", "pausing", "paused", "completed", "failed", "cancelled", "waiting_retry", "needs_action"} {
		filters = append(filters, AccessibleJobListOptions{Status: status})
	}
	for _, kind := range []string{"", OperationTranslation, OperationGlossarySync, OperationStorage} {
		for _, filter := range filters {
			t.Run(fmt.Sprintf("%s/state=%s/status=%s", kind, filter.State, filter.Status), func(t *testing.T) {
				page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: kind, AccessibleJobListOptions: filter})
				if err != nil {
					t.Fatal(err)
				}
				var want []string
				for source, rows := range seeded {
					if kind != "" && source != kind {
						continue
					}
					for status, key := range rows {
						included := false
						switch {
						case filter.Status != "":
							included = status == filter.Status
						case filter.State == "all":
							included = true
						case filter.State == "terminal":
							included = slices.Contains(terminal, status)
						default:
							included = slices.Contains(active[source], status)
						}
						if included {
							want = append(want, key)
						}
					}
				}
				got := operationKeys(page.Items)
				slices.Sort(got)
				slices.Sort(want)
				if page.Items == nil || page.NextCursor != "" || !slices.Equal(got, want) {
					t.Fatalf("got %v cursor=%q, want %v", got, page.NextCursor, want)
				}
				if kind != OperationTranslation || filter.Status == "waiting_retry" || filter.Status == "needs_action" {
					return
				}
				jobPage, err := jobs.ListAccessibleJobs(ctx, actor.ID, filter)
				if err != nil {
					t.Fatal(err)
				}
				if len(jobPage.Items) != len(page.Items) {
					t.Fatalf("Job and Operation lists differ: %d vs %d", len(jobPage.Items), len(page.Items))
				}
				for i, row := range page.Items {
					if row.Job.ID != jobPage.Items[i].ID || row.Job.Status != jobPage.Items[i].Status {
						t.Fatalf("Job and Operation state differ at index %d", i)
					}
				}
			})
		}
		for _, state := range []string{"active", "all", "terminal"} {
			_, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: kind, AccessibleJobListOptions: AccessibleJobListOptions{State: state, Status: JobStatusPausing}})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("state/status must remain exclusive: type=%s state=%s err=%v", kind, state, err)
			}
		}
	}
	// Callers may extend their own set without changing later Job/Operation queries.
	statuses := activeJobStatuses()
	statuses[0] = JobStatusFailed
	if !slices.Equal(activeJobStatuses(), active[OperationTranslation]) {
		t.Fatal("active job statuses were mutated by another caller")
	}
}

func TestOperationsPausingStablePagination(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "pausing-pages")
	p := createTestProject(t, c, "pausing-pages", actor.ID)
	at := time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.UTC)
	for i := range 11 {
		seedQueryJob(t, c, p.ID, JobStatusPausing, JobTriggerManual, at.Add(time.Duration(i/3)*time.Nanosecond))
	}
	seedOperationSync(t, c, p.ID, actor.ID, "pending", at)
	seedOperationStorage(t, c, p.ID, actor.ID, "pending", at)
	svc := NewOperationQueryService(c)
	before, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{}, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != (OperationCounts{Pending: 2, Pausing: 11}) {
		t.Fatalf("summary before pagination: %+v", before)
	}
	for _, filter := range []AccessibleJobListOptions{{}, {State: "active"}, {State: "all"}, {Status: JobStatusPausing}} {
		all, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: filter})
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 13
		if filter.Status != "" {
			wantCount = 11
		}
		if len(all.Items) != wantCount {
			t.Fatalf("filter=%+v: got %d, want %d", filter, len(all.Items), wantCount)
		}
		for _, limit := range []int{1, 2, 4, 10} {
			filter.Limit = limit
			filter.Cursor = ""
			var keys []string
			seen := map[string]bool{}
			for {
				page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: filter})
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range operationKeys(page.Items) {
					if seen[key] {
						t.Fatalf("duplicate operation across pages: %s", key)
					}
					seen[key] = true
					keys = append(keys, key)
				}
				if page.NextCursor == "" {
					break
				}
				if len(page.Items) == 0 || page.NextCursor == filter.Cursor {
					t.Fatal("pagination did not advance")
				}
				filter.Cursor = page.NextCursor
			}
			if !slices.Equal(keys, operationKeys(all.Items)) {
				t.Fatalf("limit=%d got=%v want=%v", limit, keys, operationKeys(all.Items))
			}
		}
	}
	after, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{}, at.Add(time.Minute))
	if err != nil || *before != *after {
		t.Fatalf("pagination affected summary: before=%+v after=%+v err=%v", before, after, err)
	}
}

func assertOperationCounts(t *testing.T, got *OperationsCountsSummary, translation, sync, storage OperationCounts) {
	t.Helper()
	wantTotal := OperationCounts{
		Pending:      translation.Pending + sync.Pending + storage.Pending,
		Running:      translation.Running + sync.Running + storage.Running,
		Pausing:      translation.Pausing + sync.Pausing + storage.Pausing,
		Paused:       translation.Paused + sync.Paused + storage.Paused,
		RecentFailed: translation.RecentFailed + sync.RecentFailed + storage.RecentFailed,
		WaitingRetry: translation.WaitingRetry + sync.WaitingRetry + storage.WaitingRetry,
		NeedsAction:  translation.NeedsAction + sync.NeedsAction + storage.NeedsAction,
	}
	if got.ByType.Translation != translation || got.ByType.GlossarySync != sync || got.ByType.Storage != storage || got.Total != wantTotal {
		t.Fatalf("counts=%+v, want translation=%+v sync=%+v storage=%+v total=%+v", got, translation, sync, storage, wantTotal)
	}
	for name, counts := range map[string]OperationCounts{"total": got.Total, "translation": got.ByType.Translation, "glossary_sync": got.ByType.GlossarySync, "storage": got.ByType.Storage} {
		encoded, err := json.Marshal(counts)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]int
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"pending", "running", "pausing", "paused", "recent_failed", "waiting_retry", "needs_action"} {
			if value, ok := fields[field]; !ok || value < 0 {
				t.Fatalf("%s must contain nonnegative %s, JSON=%s", name, field, encoded)
			}
		}
	}
}

func TestOperationsPausingOnlySummary(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "pausing-only")
	p := createTestProject(t, c, "pausing-only", actor.ID)
	now := time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.UTC)
	row := seedQueryJob(t, c, p.ID, JobStatusPausing, JobTriggerManual, now.Add(-8*24*time.Hour))
	svc := NewOperationQueryService(c)
	summary, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	assertOperationCounts(t, summary, OperationCounts{Pausing: 1}, OperationCounts{}, OperationCounts{})
	page, err := svc.List(ctx, actor.ID, OperationListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Job.ID != row.ID || page.Items[0].Job.Status != JobStatusPausing {
		t.Fatalf("old pausing operation was not discoverable: page=%+v err=%v", page, err)
	}
	jobSummary, err := newJobRoundTestService(t, c, nil).getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, now)
	if err != nil || jobSummary.Pending != 0 || jobSummary.Running != 0 || jobSummary.Pausing != 1 || jobSummary.Paused != 0 || jobSummary.RecentFailed != 0 {
		t.Fatalf("Job summary differs from Operations: %+v err=%v", jobSummary, err)
	}
}

func TestOperationsPausingSummaryScopesAndFailureWindow(t *testing.T) {
	ctx := context.Background()
	c, db, _ := jobQueryRecordingClient(t)
	actor := createTestUser(t, c, "pausing-summary")
	other := createTestUser(t, c, "pausing-hidden")
	org := c.Organization.Create().SetName("pausing-org").SetSlug("pausing-org").SaveX(ctx)
	member := c.OrgMembership.Create().SetUserID(actor.ID).SetOrganizationID(org.ID).SetRole("member").SaveX(ctx)
	own := createTestProject(t, c, "pausing-own", actor.ID)
	shared := c.Project.Create().SetName("pausing-shared").SetOwnerOrgID(org.ID).SaveX(ctx)
	hidden := createTestProject(t, c, "pausing-hidden", other.ID)
	now := time.Date(2026, 10, 9, 12, 0, 0, 123456789, time.FixedZone("offset", 8*3600))
	since := now.Add(-7 * 24 * time.Hour)
	for _, p := range []*ent.Project{own, shared, hidden} {
		for _, status := range []string{"pending", "running", "pausing", "paused", "waiting_retry", "needs_action"} {
			seedQueryJob(t, c, p.ID, status, JobTriggerManual, now.Add(-time.Minute))
			seedOperationSync(t, c, p.ID, other.ID, status, now.Add(-time.Minute))
		}
		seedQueryJob(t, c, p.ID, JobStatusPausing, "file_update", now.Add(-8*24*time.Hour))
		for _, status := range []string{"pending", "running", "waiting_retry", "needs_action"} {
			seedOperationStorage(t, c, p.ID, other.ID, status, now.Add(-time.Minute))
		}
		// Legacy or externally corrupted rows must not enter unsupported buckets.
		for _, status := range []string{"pausing", "paused"} {
			row := seedOperationStorage(t, c, p.ID, other.ID, "cancelled", now.Add(-time.Minute))
			if _, err := db.ExecContext(ctx, "UPDATE storage_tasks SET status = ? WHERE id = ?", status, row.ID); err != nil {
				t.Fatal(err)
			}
		}
		for _, at := range []time.Time{since.Add(-time.Nanosecond), since, now.Add(-time.Nanosecond), now, now.Add(time.Nanosecond)} {
			seedQueryJob(t, c, p.ID, "failed", JobTriggerManual, at)
			seedOperationSync(t, c, p.ID, other.ID, "failed", at)
			seedOperationStorage(t, c, p.ID, other.ID, "failed", at)
		}
	}
	svc := NewOperationQueryService(c)
	translation := OperationCounts{Pending: 1, Running: 1, Pausing: 2, Paused: 1, RecentFailed: 2}
	sync := OperationCounts{Pending: 1, Running: 1, RecentFailed: 2}
	storage := OperationCounts{Pending: 1, Running: 1, RecentFailed: 2, WaitingRetry: 1, NeedsAction: 1}
	zero := OperationCounts{}
	for _, tc := range []struct {
		name                       string
		opts                       OperationSummaryOptions
		translation, sync, storage OperationCounts
	}{
		{"all readable projects", OperationSummaryOptions{}, OperationCounts{Pending: 2, Running: 2, Pausing: 4, Paused: 2, RecentFailed: 4}, OperationCounts{Pending: 2, Running: 2, RecentFailed: 4}, OperationCounts{Pending: 2, Running: 2, RecentFailed: 4, WaitingRetry: 2, NeedsAction: 2}},
		{"owned project", OperationSummaryOptions{ProjectID: own.ID}, translation, sync, storage},
		{"organization project", OperationSummaryOptions{ProjectID: shared.ID}, translation, sync, storage},
		{"hidden project", OperationSummaryOptions{ProjectID: hidden.ID}, zero, zero, zero},
		{"missing project", OperationSummaryOptions{ProjectID: 999999}, zero, zero, zero},
		{"translation", OperationSummaryOptions{TaskType: OperationTranslation, ProjectID: own.ID}, translation, zero, zero},
		{"glossary sync", OperationSummaryOptions{TaskType: OperationGlossarySync, ProjectID: own.ID}, zero, sync, zero},
		{"storage", OperationSummaryOptions{TaskType: OperationStorage, ProjectID: own.ID}, zero, zero, storage},
		{"manual trigger", OperationSummaryOptions{TaskType: OperationTranslation, ProjectID: own.ID, TriggerType: JobTriggerManual}, OperationCounts{Pending: 1, Running: 1, Pausing: 1, Paused: 1, RecentFailed: 2}, zero, zero},
		{"old file update", OperationSummaryOptions{TaskType: OperationTranslation, ProjectID: own.ID, TriggerType: "file_update"}, OperationCounts{Pausing: 1}, zero, zero},
		{"unused trigger", OperationSummaryOptions{TaskType: OperationTranslation, ProjectID: own.ID, TriggerType: "web_edit"}, zero, zero, zero},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary, err := svc.summaryAt(ctx, actor.ID, tc.opts, now)
			if err != nil {
				t.Fatal(err)
			}
			assertOperationCounts(t, summary, tc.translation, tc.sync, tc.storage)
			if summary.AsOf != now.UTC() || summary.RecentFailedSince != since.UTC() {
				t.Fatalf("window lost UTC or timestamp precision: %+v", summary)
			}
		})
	}
	for _, kind := range []string{OperationTranslation, OperationGlossarySync, OperationStorage} {
		page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: kind, AccessibleJobListOptions: AccessibleJobListOptions{ProjectID: own.ID}})
		want := map[string]int{OperationTranslation: 5, OperationGlossarySync: 2, OperationStorage: 4}[kind]
		if err != nil || len(page.Items) != want {
			t.Fatalf("unsupported statuses entered active %s list: %+v err=%v", kind, page, err)
		}
	}
	for _, kind := range []string{OperationGlossarySync, OperationStorage} {
		for _, status := range []string{"pausing", "paused"} {
			page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: kind, AccessibleJobListOptions: AccessibleJobListOptions{Status: status}})
			if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
				t.Fatalf("unsupported %s/%s leaked: %+v err=%v", kind, status, page, err)
			}
		}
	}
	page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: OperationTranslation, AccessibleJobListOptions: AccessibleJobListOptions{ProjectID: own.ID, TriggerType: "file_update", Status: JobStatusPausing}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ProjectID() != own.ID || page.Items[0].Job.TriggerType != "file_update" {
		t.Fatalf("scoped old pausing list: %+v err=%v", page, err)
	}
	first, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{ProjectID: shared.ID, Status: JobStatusPausing, Limit: 1}})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("expected pausing continuation: %+v err=%v", first, err)
	}
	c.OrgMembership.DeleteOne(member).ExecX(ctx)
	next, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{ProjectID: shared.ID, Status: JobStatusPausing, Cursor: first.NextCursor}})
	if err != nil || len(next.Items) != 0 || next.NextCursor != "" {
		t.Fatalf("revoked pausing page leaked: %+v err=%v", next, err)
	}
	summary, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	assertOperationCounts(t, summary, translation, sync, storage)
	summary, err = svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{ProjectID: shared.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	assertOperationCounts(t, summary, zero, zero, zero)
	for _, opts := range []OperationSummaryOptions{{TaskType: "invalid"}, {ProjectID: -1}, {TriggerType: JobTriggerManual}, {TaskType: OperationGlossarySync, TriggerType: JobTriggerManual}, {TaskType: OperationStorage, TriggerType: JobTriggerManual}} {
		if _, err := svc.summaryAt(ctx, actor.ID, opts, now); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid summary scope accepted: %+v err=%v", opts, err)
		}
	}
}
