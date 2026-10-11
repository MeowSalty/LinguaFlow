package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

type operationHTTPItem struct {
	TaskID   string `json:"task_id"`
	TaskType string `json:"task_type"`
	Status   string `json:"status"`
}

type operationHTTPPage struct {
	Items      []operationHTTPItem `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

func operationHTTPList(t *testing.T, router http.Handler, query, token string) operationHTTPPage {
	t.Helper()
	w := jobQueryRequest(router, "/api/v1/operations?"+query, token)
	page := jobQueryDecode[operationHTTPPage](t, w)
	assertP4ResponseSchema(t, "/operations", w)
	return page
}

func operationHTTPStorage(t *testing.T, c *ent.Client, p *ent.Project, actor *ent.User, status storagetask.Status, at time.Time) {
	t.Helper()
	key := fmt.Sprintf("operation-pausing-%d-%s", p.ID, status)
	c.StorageTask.Create().SetOperationID(key).SetIdempotencyKey(key).SetRequestHash("hash").
		SetProjectID(p.ID).SetActorID(actor.ID).SetKind("repair").SetStatus(status).
		SetUpdatedAt(at).SaveX(context.Background())
}

func TestOperationsHTTPPausingFiltersAndJobProjection(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	p := jobQueryProject(t, c, u.ID)
	at := time.Now().UTC().Add(-30 * 24 * time.Hour)
	for _, status := range []string{"pending", "running", "pausing", "paused", "completed", "failed", "cancelled"} {
		jobQuerySeed(t, c, p.ID, status, "manual", at)
	}
	operationAPISeed(t, c, p, u, at)
	operationHTTPStorage(t, c, p, u, storagetask.StatusWaitingRetry, at)
	router := s.newRouter()
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	active := []string{"translation:paused", "translation:pausing", "translation:running", "translation:pending", "storage:waiting_retry", "glossary_sync:running"}
	terminal := []string{"translation:cancelled", "translation:failed", "translation:completed"}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", active},
		{"state=active", active},
		{"state=all", append(slices.Clone(terminal), active...)},
		{"state=terminal", terminal},
		{"task_type=translation", active[:4]},
		{"task_type=translation&state=active", active[:4]},
		{"task_type=translation&state=all", append(slices.Clone(terminal), active[:4]...)},
		{"task_type=translation&state=terminal", terminal},
		{"status=pausing", []string{"translation:pausing"}},
		{"task_type=translation&status=pausing", []string{"translation:pausing"}},
		{"task_type=translation&status=pausing&trigger_type=manual&project_id=" + strconv.Itoa(p.ID), []string{"translation:pausing"}},
		{"task_type=translation&status=pausing&trigger_type=file_update", nil},
		{"task_type=glossary_sync&status=pausing", nil},
		{"task_type=storage&status=pausing", nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			page := operationHTTPList(t, router, tc.query, token)
			var got []string
			for _, item := range page.Items {
				got = append(got, item.TaskType+":"+item.Status)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("operations = %v, want %v", got, tc.want)
			}
		})
	}
	for _, state := range []string{"active", "all", "terminal"} {
		operations := operationHTTPList(t, router, "task_type=translation&state="+state, token)
		jobResponse := jobQueryRequest(router, "/api/v1/jobs?state="+state, token)
		jobs := jobQueryDecode[JobSummaryListResponse](t, jobResponse)
		assertP4ResponseSchema(t, "/jobs", jobResponse)
		if len(operations.Items) != len(jobs.Items) {
			t.Fatalf("%s: operations=%+v jobs=%+v", state, operations.Items, jobs.Items)
		}
		for i, item := range operations.Items {
			if item.TaskID != strconv.Itoa(jobs.Items[i].Id) || item.Status != string(jobs.Items[i].Status) {
				t.Fatalf("%s: operation %+v differs from Job %+v", state, item, jobs.Items[i])
			}
		}
	}
	w := jobQueryRequest(router, "/api/v1/operations?status=pausing", token)
	page := jobQueryDecode[OperationListResponse](t, w)
	item, err := page.Items[0].AsTranslationOperation()
	if err != nil || item.Status != TranslationOperationStatusPausing || item.CanDelete || item.FinishedAt != nil {
		t.Fatalf("pausing projection=%+v err=%v", item, err)
	}
	if !slices.Equal(item.SupportedActions, []TranslationOperationSupportedActions{"view", "pause", "resume", "cancel", "retry", "delete"}) {
		t.Fatalf("type capabilities changed: %+v", item.SupportedActions)
	}
	for _, private := range []string{"execution_config", "draining_requests", "private"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("operation exposes %s: %s", private, w.Body.String())
		}
	}
}

func TestOperationsHTTPPausingDiscoveryAcrossPages(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	p := jobQueryProject(t, c, u.ID)
	at := time.Now().UTC().Add(-30 * 24 * time.Hour)
	older := jobQuerySeed(t, c, p.ID, "pausing", "manual", at)
	newer := jobQuerySeed(t, c, p.ID, "pausing", "manual", at)
	for range 51 {
		jobQuerySeed(t, c, p.ID, "pending", "manual", at.Add(time.Hour))
	}
	operationAPISeed(t, c, p, u, at)
	operationHTTPStorage(t, c, p, u, storagetask.StatusRunning, at)
	router := s.newRouter()
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	page := operationHTTPList(t, router, "", token)
	if len(page.Items) != 50 || page.NextCursor == nil {
		t.Fatalf("first page=%+v", page)
	}
	for _, item := range page.Items {
		if item.Status == "pausing" {
			t.Fatal("fixture must require continuation to discover pausing")
		}
	}
	firstCursor := *page.NextCursor
	seen := make(map[string]bool)
	var pausingIDs []string
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("operation pagination did not terminate")
		}
		for _, item := range page.Items {
			key := item.TaskType + ":" + item.TaskID
			if seen[key] {
				t.Fatalf("duplicate operation across pages: %s", key)
			}
			seen[key] = true
			if item.Status == "pausing" {
				pausingIDs = append(pausingIDs, item.TaskID)
			}
		}
		if page.NextCursor == nil {
			break
		}
		page = operationHTTPList(t, router, "state=active&limit=2&cursor="+url.QueryEscape(*page.NextCursor), token)
	}
	if len(seen) != 55 || !slices.Equal(pausingIDs, []string{strconv.Itoa(newer.ID), strconv.Itoa(older.ID)}) {
		t.Fatalf("discovery: %d operations, pausing IDs=%v", len(seen), pausingIDs)
	}
	for _, changed := range []string{"state=all", "task_type=translation", "status=pausing"} {
		w := jobQueryRequest(router, "/api/v1/operations?"+changed+"&cursor="+url.QueryEscape(firstCursor), token)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("cursor accepted changed filter %q: %d %s", changed, w.Code, w.Body.String())
		}
	}
	jobs := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs?limit=1", token))
	if jobs.NextCursor == nil {
		t.Fatal("expected Job continuation")
	}
	w := jobQueryRequest(router, "/api/v1/operations?cursor="+url.QueryEscape(*jobs.NextCursor), token)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("operation accepted Job cursor: %d %s", w.Code, w.Body.String())
	}
	assertOperationHTTPSummary(t, router, "", token, OperationCounts{Pending: 51, Pausing: 2}, OperationCounts{Running: 1}, OperationCounts{Running: 1})
}

func assertOperationHTTPSummary(t *testing.T, router http.Handler, query, token string, translation, sync, storage OperationCounts) OperationsSummaryResponse {
	t.Helper()
	w := jobQueryRequest(router, "/api/v1/operations/summary?"+query, token)
	got := jobQueryDecode[OperationsSummaryResponse](t, w)
	assertP4ResponseSchema(t, "/operations/summary", w)
	wantTotal := OperationCounts{
		Pending:      translation.Pending + sync.Pending + storage.Pending,
		Running:      translation.Running + sync.Running + storage.Running,
		Pausing:      translation.Pausing + sync.Pausing + storage.Pausing,
		Paused:       translation.Paused + sync.Paused + storage.Paused,
		RecentFailed: translation.RecentFailed + sync.RecentFailed + storage.RecentFailed,
		WaitingRetry: translation.WaitingRetry + sync.WaitingRetry + storage.WaitingRetry,
		NeedsAction:  translation.NeedsAction + sync.NeedsAction + storage.NeedsAction,
	}
	if got.Total != wantTotal || got.ByType.Translation != translation || got.ByType.GlossarySync != sync || got.ByType.Storage != storage {
		t.Fatalf("summary %q = %+v; want total=%+v translation=%+v sync=%+v storage=%+v", query, got, wantTotal, translation, sync, storage)
	}
	var raw struct {
		Total  map[string]json.RawMessage            `json:"total"`
		ByType map[string]map[string]json.RawMessage `json:"by_type"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string]OperationCounts{"total": wantTotal, "translation": translation, "glossary_sync": sync, "storage": storage} {
		fields := raw.Total
		if kind != "total" {
			fields = raw.ByType[kind]
		}
		if value, exists := fields["pausing"]; !exists || string(value) != strconv.Itoa(want.Pausing) {
			t.Fatalf("%s.pausing must be present as integer %d: %s", kind, want.Pausing, w.Body.String())
		}
	}
	return got
}

func TestOperationsHTTPPausingSummaryFiltersAndBuckets(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	p := jobQueryProject(t, c, u.ID)
	otherProject := jobQueryProject(t, c, u.ID)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-30 * 24 * time.Hour)
	for _, status := range []string{"pending", "running", "pausing", "paused"} {
		jobQuerySeed(t, c, p.ID, status, "manual", old)
	}
	jobQuerySeed(t, c, p.ID, "pausing", "manual", old)
	jobQuerySeed(t, c, otherProject.ID, "pausing", "file_update", old)
	sync := operationAPISeed(t, c, p, u, old)
	for _, status := range []string{"pending", "failed"} {
		c.SyncTask.Create().SetProjectID(p.ID).SetActorUserID(u.ID).SetEntryID(sync.EntryID).
			SetOldTarget("old").SetNewTarget("new").SetTotalSegments(0).SetSegmentIds("[]").SetResourceIds("[]").
			SetStatus(status).SetUpdatedAt(now.Add(-time.Hour)).SaveX(ctx)
	}
	for _, status := range []storagetask.Status{storagetask.StatusPending, storagetask.StatusRunning, storagetask.StatusWaitingRetry, storagetask.StatusNeedsAction, storagetask.StatusFailed} {
		operationHTTPStorage(t, c, p, u, status, now.Add(-time.Hour))
	}
	for _, at := range []time.Time{now.Add(-time.Hour), old, now.Add(time.Hour)} {
		jobQuerySeed(t, c, p.ID, "failed", "manual", at)
	}
	router := s.newRouter()
	token := authTestToken(t, u, now.Add(time.Hour), []byte(authTestSecret))
	translation := OperationCounts{Pending: 1, Running: 1, Pausing: 3, Paused: 1, RecentFailed: 1}
	syncCounts := OperationCounts{Pending: 1, Running: 1, RecentFailed: 1}
	storage := OperationCounts{Pending: 1, Running: 1, RecentFailed: 1, WaitingRetry: 1, NeedsAction: 1}
	summary := assertOperationHTTPSummary(t, router, "", token, translation, syncCounts, storage)
	if summary.AsOf.Sub(summary.RecentFailedSince) != 7*24*time.Hour {
		t.Fatalf("recent failure window changed: %+v", summary)
	}
	failed := operationHTTPList(t, router, "status=failed&updated_from="+url.QueryEscape(summary.RecentFailedSince.Format(time.RFC3339Nano))+"&updated_before="+url.QueryEscape(summary.AsOf.Format(time.RFC3339Nano)), token)
	if len(failed.Items) != summary.Total.RecentFailed {
		t.Fatalf("recent failures=%+v summary=%+v", failed.Items, summary)
	}
	assertOperationHTTPSummary(t, router, "task_type=translation", token, translation, OperationCounts{}, OperationCounts{})
	assertOperationHTTPSummary(t, router, "task_type=glossary_sync", token, OperationCounts{}, syncCounts, OperationCounts{})
	assertOperationHTTPSummary(t, router, "task_type=storage", token, OperationCounts{}, OperationCounts{}, storage)
	primary := translation
	primary.Pausing = 2
	assertOperationHTTPSummary(t, router, "project_id="+strconv.Itoa(p.ID), token, primary, syncCounts, storage)
	assertOperationHTTPSummary(t, router, "task_type=translation&trigger_type=manual", token, primary, OperationCounts{}, OperationCounts{})
	assertOperationHTTPSummary(t, router, "task_type=translation&trigger_type=file_update&project_id="+strconv.Itoa(otherProject.ID), token, OperationCounts{Pausing: 1}, OperationCounts{}, OperationCounts{})
	assertOperationHTTPSummary(t, router, "task_type=translation&trigger_type=file_update&project_id="+strconv.Itoa(p.ID), token, OperationCounts{}, OperationCounts{}, OperationCounts{})
}

func TestOperationsHTTPPausingPermissionsAndRevocation(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	ctx := context.Background()
	other := c.User.Create().SetUsername("operation-other").SetEmail("operation-other@test.com").SetPasswordHash("unused").SaveX(ctx)
	foreign := jobQueryProject(t, c, other.ID)
	org := c.Organization.Create().SetName("operation-org").SetSlug("operation-org").SaveX(ctx)
	member := c.OrgMembership.Create().SetUserID(u.ID).SetOrganizationID(org.ID).SetRole("member").SaveX(ctx)
	shared := c.Project.Create().SetName("shared").SetOwnerOrgID(org.ID).SaveX(ctx)
	at := time.Now().UTC().Add(-time.Hour)
	for _, project := range []*ent.Project{shared, foreign} {
		for range 2 {
			jobQuerySeed(t, c, project.ID, "pausing", "manual", at)
		}
		operationAPISeed(t, c, project, other, at)
		operationHTTPStorage(t, c, project, other, storagetask.StatusRunning, at)
	}
	router := s.newRouter()
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	visible := operationHTTPList(t, router, "status=pausing", token)
	if len(visible.Items) != 2 {
		t.Fatalf("member's pausing operations=%+v", visible.Items)
	}
	assertOperationHTTPSummary(t, router, "", token, OperationCounts{Pausing: 2}, OperationCounts{Running: 1}, OperationCounts{Running: 1})
	for _, projectID := range []int{foreign.ID, foreign.ID + 1000} {
		query := "project_id=" + strconv.Itoa(projectID)
		page := operationHTTPList(t, router, query+"&status=pausing", token)
		if page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
			t.Fatalf("inaccessible project must return empty items: %+v", page)
		}
		assertOperationHTTPSummary(t, router, query, token, OperationCounts{}, OperationCounts{}, OperationCounts{})
	}
	first := operationHTTPList(t, router, "limit=1", token)
	if first.NextCursor == nil {
		t.Fatal("expected continuation before revocation")
	}
	c.OrgMembership.DeleteOne(member).ExecX(ctx)
	page := operationHTTPList(t, router, "cursor="+url.QueryEscape(*first.NextCursor), token)
	if page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("revoked permission must apply to continuation: %+v", page)
	}
	assertOperationHTTPSummary(t, router, "", token, OperationCounts{}, OperationCounts{}, OperationCounts{})
	u = c.User.UpdateOne(u).SetRole("admin").SaveX(ctx)
	token = authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	page = operationHTTPList(t, router, "status=pausing", token)
	if len(page.Items) != 0 {
		t.Fatalf("system administrator gained project access: %+v", page)
	}
	assertOperationHTTPSummary(t, router, "", token, OperationCounts{}, OperationCounts{}, OperationCounts{})
}

func TestOperationsHTTPPausingValidation(t *testing.T) {
	s, _, u := jobQueryTestServer(t)
	router := s.newRouter()
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, query := range []string{
		"state=active&status=pausing", "state=all&status=pausing", "state=terminal&status=pausing",
		"state=pausing", "status=unknown", "status=pausing&status=pausing", "status=pausing&status=paused",
		"status=pausing&trigger_type=manual", "task_type=glossary_sync&status=pausing&trigger_type=manual",
		"task_type=storage&status=pausing&trigger_type=manual",
	} {
		t.Run(query, func(t *testing.T) {
			w := jobQueryRequest(router, "/api/v1/operations?"+query, token)
			if w.Code != http.StatusBadRequest || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Fatalf("invalid query response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestOperationsHTTPPausingCountsMoveOnlyWithPersistedState(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	p := jobQueryProject(t, c, u.ID)
	j := jobQuerySeed(t, c, p.ID, "pausing", "manual", time.Now().UTC().Add(-30*24*time.Hour))
	router := s.newRouter()
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	before := assertOperationHTTPSummary(t, router, "", token, OperationCounts{Pausing: 1}, OperationCounts{}, OperationCounts{})
	if before.Total.Pending+before.Total.Running+before.Total.Pausing+before.Total.Paused+before.Total.WaitingRetry+before.Total.NeedsAction != 1 {
		t.Fatal("single pausing Job must count as exactly one active operation")
	}
	c.Job.UpdateOne(j).SetStatus("paused").ExecX(context.Background())
	assertOperationHTTPSummary(t, router, "", token, OperationCounts{Paused: 1}, OperationCounts{}, OperationCounts{})
}
