package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRouterHistoryScopePaginationAndRevocation(t *testing.T) {
	s, client, u := jobQueryTestServer(t)
	s.auditSvc = service.NewAuditService(client, s.userService, s.projectSvc)
	s.statsSvc = service.NewStatsService(client, s.projectSvc)
	ctx := context.Background()
	org := client.Organization.Create().SetName("history-api").SetSlug("history-api").SaveX(ctx)
	membership := client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(u.ID).SetRole(service.OrgRoleMember).SaveX(ctx)
	p := client.Project.Create().SetName("history-api-project").SetOwnerOrgID(org.ID).SaveX(ctx)
	for i := 0; i < 3; i++ {
		client.ActivityLog.Create().SetVisibilityScope(activitylog.VisibilityScopeProject).SetProjectID(p.ID).
			SetActorID(u.ID).SetAction("segment.search_replace").SetResourceType("resource").
			SetMetadata(map[string]any{"find": "must-not-leak", "replace_with": "private body", "applied_count": i}).SaveX(ctx)
	}
	client.UsageRecord.Create().SetVisibilityScope(usagerecord.VisibilityScopeProject).SetProjectID(p.ID).SetUserID(u.ID).SetAPICalls(9).SaveX(ctx)
	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	path := fmt.Sprintf("/api/v1/activity?org_id=%d&limit=2", org.ID)
	w := jobQueryRequest(router, path, token)
	first := jobQueryDecode[activityListResponse](t, w)
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("page=%+v", first)
	}
	if strings.Contains(w.Body.String(), "must-not-leak") || strings.Contains(w.Body.String(), "private body") {
		t.Fatal("legacy metadata was not sanitized")
	}
	for _, row := range first.Items {
		if row.OrganizationID == nil || *row.OrganizationID != org.ID || row.ProjectID == nil || *row.ProjectID != p.ID {
			t.Fatalf("context=%+v", row)
		}
	}
	next := jobQueryDecode[activityListResponse](t, jobQueryRequest(router, path+"&cursor="+first.NextCursor, token))
	if len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatalf("next=%+v", next)
	}
	usage := jobQueryDecode[usageStatsResponse](t, jobQueryRequest(router, "/api/v1/stats/summary", token))
	if usage.APICalls != 9 {
		t.Fatalf("usage=%+v", usage)
	}
	for _, query := range []string{"org_id=", "org_id=0", "org_id=-1", "org_id=abc", "org_id=1&org_id=2", "org_id=1&unknown=1", "limit=1&limit=2"} {
		w := jobQueryRequest(router, "/api/v1/activity?"+query, token)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("query %q status=%d body=%s", query, w.Code, w.Body.String())
		}
	}
	if w := jobQueryRequest(router, "/api/v1/activity?org_id=99999", token); w.Code != http.StatusNotFound {
		t.Fatalf("missing org=%d", w.Code)
	}
	if err := client.OrgMembership.DeleteOne(membership).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if w := jobQueryRequest(router, path, token); w.Code != http.StatusForbidden {
		t.Fatalf("revoked org=%d body=%s", w.Code, w.Body.String())
	}
	w = jobQueryRequest(router, "/api/v1/activity", token)
	var remaining activityListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &remaining); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(remaining.Items) != 0 {
		t.Fatalf("revoked data=%+v code=%d", remaining, w.Code)
	}
	usage = jobQueryDecode[usageStatsResponse](t, jobQueryRequest(router, "/api/v1/stats/summary", token))
	if usage.APICalls != 0 {
		t.Fatalf("revoked usage=%+v", usage)
	}
}
