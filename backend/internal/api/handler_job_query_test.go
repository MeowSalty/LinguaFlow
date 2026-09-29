package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func jobQueryTestServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	s, client, user := authTestServer(t)
	s.userService = service.NewUserService(client, nil)
	s.projectSvc = service.NewProjectService(client, s.userService)
	s.jobSvc = service.NewJobService(client, s.projectSvc, nil, nil, nil, nil, nil, nil, nil)
	return s, client, user
}

func jobQueryProject(t *testing.T, client *ent.Client, ownerID int) *ent.Project {
	t.Helper()
	p, err := client.Project.Create().SetName("project-" + strconv.Itoa(ownerID)).SetOwnerUserID(ownerID).Save(context.Background())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

func jobQuerySeed(t *testing.T, client *ent.Client, projectID int, status, trigger string, updatedAt time.Time) *ent.Job {
	t.Helper()
	j, err := client.Job.Create().SetProjectID(projectID).SetExecutionPlanID(1).
		SetStatus(status).SetTriggerType(trigger).SetUpdatedAt(updatedAt).
		SetExecutionConfig(map[string]any{"private": "must not appear in task summary"}).
		SetResourceCount(4).SetCompletedResources(2).SetFailedResources(1).
		SetProgressTotal(200).SetProgressCompleted(75).Save(context.Background())
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return j
}

func jobQueryRequest(router http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func jobQueryDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	var result T
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func TestRouter_JobQueries_Authentication(t *testing.T) {
	s, client, user := jobQueryTestServer(t)
	router := s.newRouter()
	paths := []string{"/api/v1/jobs", "/api/v1/jobs/summary"}
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, path := range paths {
		w := jobQueryRequest(router, path, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token = %d, want 401: %s", path, w.Code, w.Body.String())
		}
		w = jobQueryRequest(router, path, token)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s with valid token = %d, want 200: %s", path, w.Code, w.Body.String())
		}
	}
	if err := client.User.UpdateOne(user).SetActive(false).Exec(context.Background()); err != nil {
		t.Fatalf("disable user: %v", err)
	}
	for _, path := range paths {
		w := jobQueryRequest(router, path, token)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with disabled user = %d, want 401: %s", path, w.Code, w.Body.String())
		}
	}
}

func TestRouter_JobQueries_InvalidParameters(t *testing.T) {
	s, _, user := jobQueryTestServer(t)
	router := s.newRouter()
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	cases := map[string][]string{
		"/api/v1/jobs": {
			"state=unknown", "status=unknown", "trigger_type=unknown", "state=active&status=running",
			"state=all&status=failed", "limit=0", "limit=-1", "limit=101", "limit=abc", "limit=1.5",
			"project_id=0", "project_id=-1", "project_id=abc", "project_id=999999999999999999999999",
			"updated_from=2026-09-01", "updated_before=invalid",
			"updated_from=2026-09-02T00:00:00Z&updated_before=2026-09-01T00:00:00Z",
			"updated_from=2026-09-01T00:00:00Z&updated_before=2026-09-01T00:00:00Z",
			"cursor=123", "cursor=not-a-valid-cursor", "cursor=MTAw",
			"state=", "status=", "trigger_type=", "project_id=", "limit=", "cursor=", "updated_from=", "updated_before=",
			"state=active&state=active", "status=running&status=paused", "limit=1&limit=2", "project_id=1&project_id=2",
			"updated_from=2026-09-01T00:00:00Z&updated_from=2026-09-02T00:00:00Z", "unknown=true",
		},
		"/api/v1/jobs/summary": {
			"project_id=0", "project_id=abc", "project_id=", "trigger_type=invalid", "trigger_type=",
			"project_id=1&project_id=1", "trigger_type=manual&trigger_type=file_update",
			"state=all", "status=failed", "limit=10", "cursor=123", "updated_from=2026-09-01T00:00:00Z",
			"updated_before=2026-09-02T00:00:00Z", "unknown=true",
		},
	}
	for path, queries := range cases {
		for _, query := range queries {
			t.Run(path+"?"+query, func(t *testing.T) {
				w := jobQueryRequest(router, path+"?"+query, token)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400, body: %s", w.Code, w.Body.String())
				}
				if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/problem+json") {
					t.Errorf("Content-Type = %q, want application/problem+json", contentType)
				}
				var problem problemDetails
				if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
					t.Fatalf("decode problem: %v", err)
				}
				if problem.Status != http.StatusBadRequest || problem.Type != "urn:linguaflow:invalid-query-parameter" || problem.Title != "invalid_query_parameter" || problem.Detail == "" {
					t.Errorf("unexpected parameter problem: %+v", problem)
				}
			})
		}
	}
}

func TestRouter_ListAccessibleJobs_FiltersAndLightweightResponse(t *testing.T) {
	s, client, user := jobQueryTestServer(t)
	p := jobQueryProject(t, client, user.ID)
	base := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	statuses := []string{"pending", "running", "paused", "completed", "failed", "cancelled"}
	triggers := []string{"manual", "file_update", "glossary_change", "web_edit", "manual", "manual"}
	seeded := make([]*ent.Job, len(statuses))
	for i, status := range statuses {
		seeded[i] = jobQuerySeed(t, client, p.ID, status, triggers[i], base.Add(time.Duration(i)*time.Second))
	}
	started := base.Add(-time.Hour)
	if err := client.Job.UpdateOne(seeded[1]).SetStartedAt(started).SetUpdatedAt(seeded[1].UpdatedAt).Exec(context.Background()); err != nil {
		t.Fatalf("set started time: %v", err)
	}
	router := s.newRouter()
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"", []string{"paused", "running", "pending"}},
		{"state=active", []string{"paused", "running", "pending"}},
		{"state=terminal", []string{"cancelled", "failed", "completed"}},
		{"state=all", []string{"cancelled", "failed", "completed", "paused", "running", "pending"}},
		{"status=failed", []string{"failed"}},
		{"state=all&trigger_type=web_edit", []string{"completed"}},
		{"trigger_type=file_update", []string{"running"}},
		{"trigger_type=glossary_change", []string{"paused"}},
		{"state=all&trigger_type=manual&project_id=" + strconv.Itoa(p.ID), []string{"cancelled", "failed", "pending"}},
		{"state=all&updated_from=" + url.QueryEscape(base.Add(time.Second).Format(time.RFC3339Nano)) + "&updated_before=" + url.QueryEscape(base.Add(3*time.Second).Format(time.RFC3339Nano)), []string{"paused", "running"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			response := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs?"+tc.query, token))
			got := make([]string, len(response.Items))
			for i, item := range response.Items {
				got[i] = string(item.Status)
				if item.ProjectId != p.ID || item.ProjectName != p.Name {
					t.Errorf("project identity not mapped: %+v", item)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("statuses = %v, want %v", got, tc.want)
			}
		})
	}
	w := jobQueryRequest(router, "/api/v1/jobs?status=pending", token)
	response := jobQueryDecode[JobSummaryListResponse](t, w)
	if len(response.Items) != 1 {
		t.Fatalf("pending items = %d, want 1", len(response.Items))
	}
	item := response.Items[0]
	if item.Id != seeded[0].ID || item.StartedAt != nil || item.CreatedAt.IsZero() || !item.UpdatedAt.Equal(base) {
		t.Errorf("summary metadata not mapped: %+v", item)
	}
	progress := item.Progress
	if progress.TotalResources != 4 || progress.CompletedResources != 2 || progress.FailedResources != 1 || progress.ProgressTotal != 200 || progress.ProgressCompleted != 75 {
		t.Errorf("progress counters not mapped: %+v", progress)
	}
	raw := jobQueryDecode[struct {
		Items []map[string]json.RawMessage `json:"items"`
	}](t, w)
	if string(raw.Items[0]["started_at"]) != "null" {
		t.Errorf("started_at = %s, want explicit null", raw.Items[0]["started_at"])
	}
	for _, field := range []string{"execution_config", "execution_plan_id", "created_by", "job_resources", "resources", "rounds", "events"} {
		if _, ok := raw.Items[0][field]; ok {
			t.Errorf("lightweight summary unexpectedly includes %q", field)
		}
	}
	var rawProgress map[string]json.RawMessage
	if err := json.Unmarshal(raw.Items[0]["progress"], &rawProgress); err != nil {
		t.Fatalf("decode progress: %v", err)
	}
	for _, field := range []string{"queue_position", "queue_size"} {
		if string(rawProgress[field]) != "null" {
			t.Errorf("%s = %s, want explicit null", field, rawProgress[field])
		}
	}
	running := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs?status=running", token))
	if len(running.Items) != 1 || running.Items[0].StartedAt == nil || !running.Items[0].StartedAt.Equal(started) {
		t.Errorf("started_at not preserved: %+v", running.Items)
	}
	// Existing project list and job detail must remain usable beside the static /jobs/summary route.
	legacyList := jobQueryDecode[struct {
		Items []jobResponse `json:"items"`
	}](t, jobQueryRequest(router, "/api/v1/projects/"+strconv.Itoa(p.ID)+"/jobs?status=failed", token))
	if len(legacyList.Items) != 1 || legacyList.Items[0].ID != seeded[4].ID {
		t.Errorf("legacy project list changed: %+v", legacyList.Items)
	}
	detail := jobQueryDecode[jobResponse](t, jobQueryRequest(router, "/api/v1/jobs/"+strconv.Itoa(seeded[0].ID), token))
	if detail.ID != seeded[0].ID || detail.ExecutionConfig["private"] == nil {
		t.Errorf("legacy job detail changed: %+v", detail)
	}
}

func TestRouter_ListAccessibleJobs_Pagination(t *testing.T) {
	s, client, user := jobQueryTestServer(t)
	p := jobQueryProject(t, client, user.ID)
	base := time.Date(2026, time.September, 1, 12, 0, 0, 100, time.UTC)
	old := jobQuerySeed(t, client, p.ID, "pending", "manual", base)
	middle := jobQuerySeed(t, client, p.ID, "running", "manual", base.Add(100*time.Nanosecond))
	newest := jobQuerySeed(t, client, p.ID, "paused", "manual", base.Add(100*time.Nanosecond))
	router := s.newRouter()
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	path := "/api/v1/jobs?limit=1"
	for i, wantID := range []int{newest.ID, middle.ID, old.ID} {
		response := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, path, token))
		if len(response.Items) != 1 || response.Items[0].Id != wantID {
			t.Fatalf("page %d = %+v, want job %d", i+1, response, wantID)
		}
		if i == 2 {
			if response.NextCursor != nil {
				t.Errorf("last page cursor = %q, want omitted", *response.NextCursor)
			}
			break
		}
		if response.NextCursor == nil || *response.NextCursor == "" {
			t.Fatal("missing next page cursor")
		}
		if _, err := strconv.Atoi(*response.NextCursor); err == nil {
			t.Errorf("cursor must be opaque, got numeric %q", *response.NextCursor)
		}
		path = "/api/v1/jobs?limit=1&cursor=" + url.QueryEscape(*response.NextCursor)
	}
}

func TestRouter_JobQueries_AccessScopeAndLocalMode(t *testing.T) {
	for _, local := range []bool{false, true} {
		name := "server"
		if local {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			s, client, user := jobQueryTestServer(t)
			own := jobQueryProject(t, client, user.ID)
			other, err := client.User.Create().SetUsername("other").SetEmail("other@test.com").SetPasswordHash("unused").Save(context.Background())
			if err != nil {
				t.Fatalf("create other user: %v", err)
			}
			foreign := jobQueryProject(t, client, other.ID)
			visible := jobQuerySeed(t, client, own.ID, "pending", "manual", time.Now().UTC())
			jobQuerySeed(t, client, foreign.ID, "pending", "manual", time.Now().UTC())
			token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
			if local {
				s.mode = config.ModeLocal
				s.localUser = user
				token = ""
			}
			router := s.newRouter()
			response := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs", token))
			if len(response.Items) != 1 || response.Items[0].Id != visible.ID {
				t.Fatalf("accessible list = %+v, want own job %d", response.Items, visible.ID)
			}
			summary := jobQueryDecode[JobsSummaryResponse](t, jobQueryRequest(router, "/api/v1/jobs/summary", token))
			if summary.Pending != 1 {
				t.Errorf("pending = %d, want 1", summary.Pending)
			}
			for _, projectID := range []int{foreign.ID, foreign.ID + 1000} {
				query := "?project_id=" + strconv.Itoa(projectID)
				response := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs"+query, token))
				if response.Items == nil || len(response.Items) != 0 || response.NextCursor != nil {
					t.Errorf("inaccessible project list must be items: [] without cursor: %+v", response)
				}
				summary := jobQueryDecode[JobsSummaryResponse](t, jobQueryRequest(router, "/api/v1/jobs/summary"+query, token))
				if summary.Pending != 0 || summary.Running != 0 || summary.Paused != 0 || summary.RecentFailed != 0 {
					t.Errorf("inaccessible project summary = %+v, want zero counts", summary)
				}
			}
		})
	}
}

func TestRouter_GetJobsSummary_WindowAndUnpaginatedCounts(t *testing.T) {
	s, client, user := jobQueryTestServer(t)
	p := jobQueryProject(t, client, user.ID)
	now := time.Now().UTC()
	for range 51 {
		jobQuerySeed(t, client, p.ID, "pending", "manual", now.Add(-30*24*time.Hour))
	}
	jobQuerySeed(t, client, p.ID, "running", "file_update", now.Add(-30*24*time.Hour))
	jobQuerySeed(t, client, p.ID, "paused", "manual", now.Add(-30*24*time.Hour))
	recentFailure := jobQuerySeed(t, client, p.ID, "failed", "manual", now.Add(-6*24*time.Hour))
	jobQuerySeed(t, client, p.ID, "failed", "manual", now.Add(-8*24*time.Hour))
	jobQuerySeed(t, client, p.ID, "failed", "manual", now.Add(24*time.Hour))
	jobQuerySeed(t, client, p.ID, "completed", "manual", now.Add(-time.Hour))
	router := s.newRouter()
	token := authTestToken(t, user, time.Now().Add(time.Hour), []byte(authTestSecret))
	list := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs", token))
	if len(list.Items) != 50 || list.NextCursor == nil {
		t.Fatalf("default list = %d items, cursor %v; want 50 with continuation", len(list.Items), list.NextCursor)
	}
	before := time.Now()
	summary := jobQueryDecode[JobsSummaryResponse](t, jobQueryRequest(router, "/api/v1/jobs/summary", token))
	after := time.Now()
	if summary.Pending != 51 || summary.Running != 1 || summary.Paused != 1 || summary.RecentFailed != 1 {
		t.Errorf("summary = %+v, want pending=51 running=1 paused=1 recent_failed=1", summary)
	}
	if summary.AsOf.Before(before) || summary.AsOf.After(after) || summary.AsOf.Sub(summary.RecentFailedSince) != 7*24*time.Hour {
		t.Errorf("summary window must be seven days ending at request time: %+v", summary)
	}
	query := url.Values{
		"status":         {"failed"},
		"updated_from":   {summary.RecentFailedSince.Format(time.RFC3339Nano)},
		"updated_before": {summary.AsOf.Format(time.RFC3339Nano)},
	}
	failed := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs?"+query.Encode(), token))
	if len(failed.Items) != 1 || len(failed.Items) != summary.RecentFailed || failed.Items[0].Id != recentFailure.ID {
		t.Errorf("failure list does not match summary: %+v", failed)
	}
	filtered := jobQueryDecode[JobsSummaryResponse](t, jobQueryRequest(router, "/api/v1/jobs/summary?project_id="+strconv.Itoa(p.ID)+"&trigger_type=file_update", token))
	if filtered.Pending != 0 || filtered.Running != 1 || filtered.Paused != 0 || filtered.RecentFailed != 0 {
		t.Errorf("project and trigger summary = %+v", filtered)
	}
	if err := client.Job.UpdateOne(recentFailure).SetStatus("pending").Exec(context.Background()); err != nil {
		t.Fatalf("change failed job to pending: %v", err)
	}
	retried := jobQueryDecode[JobsSummaryResponse](t, jobQueryRequest(router, "/api/v1/jobs/summary", token))
	if retried.Pending != 52 || retried.RecentFailed != 0 {
		t.Errorf("retried job must leave failure count: %+v", retried)
	}
}
