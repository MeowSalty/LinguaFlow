package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

func historyAPIServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	s, c, u := jobQueryTestServer(t)
	c.SystemSetting.Create().SetKey(service.SettingRegistrationEnabled).SetValue("true").SaveX(t.Context())
	c.SystemSetting.Create().SetKey(service.SettingTaskRetention).SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).SaveX(t.Context())
	s.settingsService = service.NewSettingsService(c)
	s.taskLifecycle = &tasklife.Coordinator{}
	s.eventBroker = event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig())).WithHistorian(event.NewEntEventStore(c, "sqlite"))
	s.taskHistory = service.NewTaskHistoryService(c, s.projectSvc, s.taskLifecycle, s.eventBroker)
	s.taskHistory.SetReady(true)
	s.taskHistory.SetMaintenance(s.storageMaintenance)
	s.jobSvc.SetLifecycle(s.taskLifecycle)
	return s, c, u
}

func historyAPIRequest(t *testing.T, s *Server, actor *ent.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if actor != nil {
		r.Header.Set("Authorization", "Bearer "+authTestToken(t, actor, time.Now().Add(time.Hour), []byte(authTestSecret)))
	}
	w := httptest.NewRecorder()
	s.newRouter().ServeHTTP(w, r)
	return w
}

func assertHistoryResponseSchema(t *testing.T, path, method string, response *httptest.ResponseRecorder) {
	t.Helper()
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	operation := spec.Paths.Find(path).Get
	if method == "POST" {
		operation = spec.Paths.Find(path).Post
	}
	if method == "PATCH" {
		operation = spec.Paths.Find(path).Patch
	}
	var value any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if err := operation.Responses.Status(200).Value.Content["application/json"].Schema.Value.VisitJSON(value); err != nil {
		t.Fatalf("%s %s violates schema: %v\n%s", method, path, err, response.Body.String())
	}
}

func TestTaskHistoryHTTPDeleteAndBatch(t *testing.T) {
	s, c, u := historyAPIServer(t)
	p := jobQueryProject(t, c, u.ID)
	completed := jobQuerySeed(t, c, p.ID, "completed", "manual", time.Now().UTC())
	pending := jobQuerySeed(t, c, p.ID, "pending", "manual", time.Now().UTC())
	busy := jobQuerySeed(t, c, p.ID, "cancelled", "manual", time.Now().UTC())
	guard, err := s.taskLifecycle.Lock(t.Context(), "translation", busy.ID)
	if err != nil {
		t.Fatal(err)
	}
	release := guard.Claim()
	guard.Release()
	defer release()
	for _, check := range []struct {
		id, code int
		reason   string
	}{{pending.ID, 409, "task_not_terminal"}, {busy.ID, 409, "task_busy"}} {
		w := historyAPIRequest(t, s, u, "DELETE", fmt.Sprintf("/jobs/%d", check.id), "")
		if w.Code != check.code || !strings.Contains(w.Body.String(), check.reason) {
			t.Fatalf("delete status: %d %s", w.Code, w.Body.String())
		}
	}
	w := historyAPIRequest(t, s, u, "POST", "/operations/batch-delete", fmt.Sprintf(`{"items":[{"kind":"translation","id":"%d","project_id":%d},{"kind":"storage","id":"1","project_id":%d}]}`, completed.ID, p.ID, p.ID))
	if w.Code != 400 || c.Job.Query().CountX(t.Context()) != 3 {
		t.Fatalf("invalid batch: %d %s", w.Code, w.Body.String())
	}
	ref := fmt.Sprintf(`{"kind":"translation","id":"%d","project_id":%d}`, completed.ID, p.ID)
	body := fmt.Sprintf(`{"items":[%s,%s,{"kind":"translation","id":"%d","project_id":%d},{"kind":"translation","id":"%d","project_id":%d}]}`, ref, ref, pending.ID, p.ID, busy.ID, p.ID)
	w = historyAPIRequest(t, s, u, "POST", "/operations/batch-delete", body)
	if w.Code != 200 {
		t.Fatalf("batch: %d %s", w.Code, w.Body.String())
	}
	assertHistoryResponseSchema(t, "/operations/batch-delete", "POST", w)
	var result struct {
		Items []service.HistoryDeleteResult `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 || result.Items[0].Status != "deleted" || result.Items[1].Status != "not_terminal" || result.Items[2].Status != "busy" {
		t.Fatalf("results=%+v", result.Items)
	}
	if w := historyAPIRequest(t, s, u, "DELETE", fmt.Sprintf("/jobs/%d", completed.ID), ""); w.Code != 404 {
		t.Fatalf("repeated delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := c.Project.Get(t.Context(), p.ID); err != nil {
		t.Fatal("project content was removed", err)
	}
}

func TestTaskHistoryHTTPProjectionAndAuthorization(t *testing.T) {
	s, c, u := historyAPIServer(t)
	p := jobQueryProject(t, c, u.ID)
	job := jobQuerySeed(t, c, p.ID, "completed", "manual", time.Now().UTC())
	finished := time.Now().UTC().Add(-time.Hour)
	c.Job.UpdateOne(job).SetFinishedAt(finished).SetRetentionAnchorAt(finished).ExecX(t.Context())
	for _, path := range []string{fmt.Sprintf("/jobs/%d", job.ID), "/jobs?state=terminal", "/operations?state=terminal"} {
		w := historyAPIRequest(t, s, u, "GET", path, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"can_delete":true`) || strings.Contains(w.Body.String(), `"finished_at":null`) {
			t.Fatalf("projection %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	other := c.User.Create().SetUsername("unrelated-admin").SetEmail("admin@test.invalid").SetPasswordHash("unused").SetRole(service.SystemRoleAdmin).SaveX(t.Context())
	w := historyAPIRequest(t, s, other, "DELETE", fmt.Sprintf("/jobs/%d", job.ID), "")
	if w.Code != 403 {
		t.Fatalf("admin crossed project scope: %d %s", w.Code, w.Body.String())
	}
	task := operationAPISeed(t, c, p, u, time.Now().UTC())
	c.SyncTask.UpdateOne(task).SetStatus("completed").ExecX(t.Context())
	w = historyAPIRequest(t, s, u, "DELETE", fmt.Sprintf("/projects/%d/sync-tasks/%d", p.ID, task.ID), "")
	if w.Code != 204 {
		t.Fatalf("sync delete: %d %s", w.Code, w.Body.String())
	}
	w = historyAPIRequest(t, s, u, "DELETE", fmt.Sprintf("/jobs/%d", job.ID), "")
	if w.Code != 204 {
		t.Fatalf("job delete: %d %s", w.Code, w.Body.String())
	}
}

func TestTaskHistoryHTTPMaintenancePreservesAuthorization(t *testing.T) {
	s, c, u := historyAPIServer(t)
	p := jobQueryProject(t, c, u.ID)
	job := jobQuerySeed(t, c, p.ID, "completed", "manual", time.Now().UTC())
	other := c.User.Create().SetUsername("history-other").SetEmail("history-other@test.invalid").SetPasswordHash("unused").SaveX(t.Context())
	otherProject := jobQueryProject(t, c, other.ID)
	otherJob := jobQuerySeed(t, c, otherProject.ID, "completed", "manual", time.Now().UTC())
	s.storageRuntime = &storageRuntime{maintenance: true}
	body := fmt.Sprintf(`{"items":[{"kind":"translation","id":"%d","project_id":%d},{"kind":"translation","id":"%d","project_id":%d},{"kind":"translation","id":"%d","project_id":%d}]}`,
		job.ID, p.ID, otherJob.ID, otherProject.ID, otherJob.ID, p.ID)
	if w := historyAPIRequest(t, s, nil, "POST", "/operations/batch-delete", body); w.Code != 401 {
		t.Fatalf("maintenance hid authentication: %d %s", w.Code, w.Body.String())
	}
	w := historyAPIRequest(t, s, u, "POST", "/operations/batch-delete", body)
	if w.Code != 200 {
		t.Fatalf("maintenance batch: %d %s", w.Code, w.Body.String())
	}
	assertHistoryResponseSchema(t, "/operations/batch-delete", "POST", w)
	var response struct {
		Items []service.HistoryDeleteResult `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 3 || response.Items[0].Status != "blocked" || response.Items[1].Status != "forbidden" || response.Items[2].Status != "not_found" {
		t.Fatalf("maintenance bypassed item authorization: %+v", response.Items)
	}
	if c.Job.Query().CountX(t.Context()) != 2 {
		t.Fatal("maintenance batch deleted task history")
	}
	w = historyAPIRequest(t, s, u, "GET", fmt.Sprintf("/jobs/%d", job.ID), "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"can_delete":false`) {
		t.Fatalf("maintenance deletion projection: %d %s", w.Code, w.Body.String())
	}
}

func TestTaskRetentionAdminContractAndMaintenance(t *testing.T) {
	s, c, u := historyAPIServer(t)
	assertStatusPolicy := func(response *httptest.ResponseRecorder, want service.TaskRetentionPolicy) {
		t.Helper()
		var status struct {
			TaskRetention  *service.TaskRetentionPolicy `json:"task_retention"`
			PolicyRevision int64                        `json:"policy_revision"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
			t.Fatal(err)
		}
		if status.TaskRetention == nil || *status.TaskRetention != want || status.PolicyRevision != want.Revision {
			t.Fatalf("status policy = %+v revision=%d; want %+v", status.TaskRetention, status.PolicyRevision, want)
		}
	}
	if w := historyAPIRequest(t, s, u, "POST", "/admin/task-retention/preview", `{"retention_days":30}`); w.Code != 403 {
		t.Fatalf("preview auth=%d", w.Code)
	}
	u = c.User.UpdateOne(u).SetRole(service.SystemRoleAdmin).SaveX(t.Context())
	s.storageRuntime = &storageRuntime{maintenance: true}
	w := historyAPIRequest(t, s, u, "POST", "/admin/task-retention/preview", `{"retention_days":30}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"policy_revision":1`) {
		t.Fatalf("maintenance preview: %d %s", w.Code, w.Body.String())
	}
	assertHistoryResponseSchema(t, "/admin/task-retention/preview", "POST", w)
	w = historyAPIRequest(t, s, u, "GET", "/admin/task-retention/status", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"last_scan":null`) || !strings.Contains(w.Body.String(), `"state":"disabled"`) {
		t.Fatalf("initial status: %d %s", w.Code, w.Body.String())
	}
	assertHistoryResponseSchema(t, "/admin/task-retention/status", "GET", w)
	assertStatusPolicy(w, service.TaskRetentionPolicy{Enabled: false, RetentionDays: 30, Revision: 1})
	valid := `{"settings":{"task_retention":{"enabled":true,"retention_days":7,"expected_revision":1}}}`
	for _, body := range []string{
		`{"settings":{"registration_enabled":null,"task_retention":{"enabled":true,"retention_days":7,"expected_revision":1}}}`,
		`{"settings":{"registration_enabled":false,"task_retention":null}}`,
		`{"settings":{"task_retention":{"enabled":true,"retention_days":7,"expected_revision":1,"revision":2}}}`,
		`{"settings":{"task_retention":{"enabled":true,"retention_days":"7","expected_revision":1}}}`,
		`{"settings":{"task_retention":{"enabled":true,"retention_days":0,"expected_revision":1}}}`,
		`{"settings":{"task_retention":{"enabled":true,"retention_days":7}}}`,
		valid + ` {}`,
	} {
		if w := historyAPIRequest(t, s, u, "PATCH", "/admin/settings", body); w.Code != 400 {
			t.Fatalf("invalid patch=%s response=%d %s", body, w.Code, w.Body.String())
		}
	}
	w = historyAPIRequest(t, s, u, "PATCH", "/admin/settings", valid)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("maintenance patch: %d %s", w.Code, w.Body.String())
	}
	assertHistoryResponseSchema(t, "/admin/settings", "PATCH", w)
	w = historyAPIRequest(t, s, u, "PATCH", "/admin/settings", valid)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "settings_conflict") {
		t.Fatalf("stale patch: %d %s", w.Code, w.Body.String())
	}
	w = historyAPIRequest(t, s, u, "GET", "/admin/task-retention/status", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"blocked"`) {
		t.Fatalf("blocked status: %d %s", w.Code, w.Body.String())
	}
	assertHistoryResponseSchema(t, "/admin/task-retention/status", "GET", w)
	assertStatusPolicy(w, service.TaskRetentionPolicy{Enabled: true, RetentionDays: 7, Revision: 2})
}

func TestJobHistoryReadFailureReturnsUnavailable(t *testing.T) {
	s, c, u := jobEventsTestServer(t)
	job, _ := seedJobEvents(t, c, u)
	c.SSEEvent.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(context.Context, ent.Query) (ent.Value, error) {
			return nil, errors.New("injected event read failure")
		})
	}))
	beforeSeq := 6
	for _, before := range []*int{nil, &beforeSeq} {
		w := jobEventsRequest2(s, job.ID, 0, 10, u, before)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "history_unavailable") {
			t.Fatalf("history failure: %d %s", w.Code, w.Body.String())
		}
	}
	w := jobEventsRequest(s, job.ID, 1, 10, u)
	if w.Code != 503 {
		t.Fatalf("forward failure: %d %s", w.Code, w.Body.String())
	}
}
