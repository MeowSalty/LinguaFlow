package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func operationAPISeed(t *testing.T, c *ent.Client, p *ent.Project, u *ent.User, at time.Time) *ent.SyncTask {
	t.Helper()
	ctx := context.Background()
	entry := c.GlossaryEntry.Create().SetProjectID(p.ID).SetSource("term").SetSourceKey("term").SetTarget("private-new-target").SaveX(ctx)
	return c.SyncTask.Create().SetProjectID(p.ID).SetActorUserID(u.ID).SetEntryID(entry.ID).SetOldTarget("private-old-target").SetNewTarget("private-new-target").SetStatus("running").SetSegmentIds("[321]").SetResourceIds("[654]").SetTotalSegments(12).SetProcessedSegments(4).SetError("private-error").SetUpdatedAt(at).SaveX(ctx)
}
func TestOperationsHTTPProjectionAndLegacyCompatibility(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	p := jobQueryProject(t, c, u.ID)
	at := time.Now().UTC()
	j := jobQuerySeed(t, c, p.ID, "running", "manual", at)
	sync := operationAPISeed(t, c, p, u, at)
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	first := jobQueryRequest(router, "/api/v1/operations?limit=1", token)
	assertP4ResponseSchema(t, "/operations", first)
	page := jobQueryDecode[OperationListResponse](t, first)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("page=%+v", page)
	}
	translation, err := page.Items[0].AsTranslationOperation()
	if err != nil || translation.TaskId != fmt.Sprint(j.ID) || translation.TaskType != "translation" {
		t.Fatalf("translation=%+v %v", translation, err)
	}
	if translation.StartedAt != nil || translation.Progress.QueueSize != nil || len(translation.SupportedActions) != 5 {
		t.Fatalf("translation fields=%+v", translation)
	}
	second := jobQueryRequest(router, "/api/v1/operations?limit=1&cursor="+url.QueryEscape(*page.NextCursor), token)
	assertP4ResponseSchema(t, "/operations", second)
	page = jobQueryDecode[OperationListResponse](t, second)
	item, err := page.Items[0].AsGlossarySyncOperation()
	if err != nil || item.TaskId != fmt.Sprint(sync.ID) || item.Progress.ProcessedSegments != 4 || len(item.SupportedActions) != 2 {
		t.Fatalf("sync=%+v %v", item, err)
	}
	for _, secret := range []string{"private-", "segment_ids", "resource_ids", "trigger_type", "execution_config"} {
		if strings.Contains(second.Body.String(), secret) {
			t.Fatalf("leaked %s: %s", secret, second.Body.String())
		}
	}
	old := jobQueryDecode[JobSummaryListResponse](t, jobQueryRequest(router, "/api/v1/jobs", token))
	if len(old.Items) != 1 || old.Items[0].Id != j.ID {
		t.Fatal(old)
	}
	summaryResponse := jobQueryRequest(router, "/api/v1/operations/summary", token)
	assertP4ResponseSchema(t, "/operations/summary", summaryResponse)
	summary := jobQueryDecode[OperationsSummaryResponse](t, summaryResponse)
	if summary.Total.Running != 2 || summary.ByType.Translation.Running != 1 || summary.ByType.GlossarySync.Running != 1 || summary.ByType.GlossarySync.Paused != 0 {
		t.Fatal(summary)
	}
}

func assertP4ResponseSchema(t *testing.T, path string, response *httptest.ResponseRecorder) {
	t.Helper()
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	schema := spec.Paths.Find(path).Get.Responses.Status(200).Value.Content["application/json"].Schema.Value
	if err := schema.VisitJSON(value); err != nil {
		t.Fatalf("%s response violates OpenAPI: %v\n%s", path, err, response.Body.String())
	}
}
func TestOperationsHTTPValidationAndAuthentication(t *testing.T) {
	s, _, u := jobQueryTestServer(t)
	router := s.newRouter()
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	for _, path := range []string{"/api/v1/operations", "/api/v1/operations/summary"} {
		if got := jobQueryRequest(router, path, ""); got.Code != http.StatusUnauthorized {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	invalid := map[string][]string{
		"/api/v1/operations":         {"unknown=1", "task_type=sync", "task_type=", "task_type=translation&task_type=glossary_sync", "trigger_type=manual", "task_type=glossary_sync&trigger_type=manual", "project_id=0", "project_id=-1", "state=active&status=running", "state=unknown", "status=", "limit=0", "limit=101", "limit=1&limit=2", "cursor=123", "updated_from=2026-09-29", "updated_from=bad", "updated_from=2026-09-29T00:00:00Z&updated_before=2026-09-28T00:00:00Z"},
		"/api/v1/operations/summary": {"state=active", "status=running", "limit=10", "cursor=abc", "task_type=sync", "trigger_type=manual", "project_id=0", "task_type=translation&task_type=translation", "updated_from=2026-09-29T00:00:00Z"},
	}
	for path, queries := range invalid {
		for _, query := range queries {
			w := jobQueryRequest(router, path+"?"+query, token)
			if w.Code != 400 || !strings.Contains(w.Header().Get("Content-Type"), "problem+json") {
				t.Errorf("%s?%s: %d %s", path, query, w.Code, w.Body.String())
			}
		}
	}
}
