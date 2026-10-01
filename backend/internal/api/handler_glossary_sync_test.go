package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func syncAPIFixture(t *testing.T) (*Server, *ent.Client, *ent.User, int, int, string) {
	t.Helper()
	ctx := context.Background()
	s, client, owner := authTestServer(t)
	s.userService = service.NewUserService(client, nil)
	s.projectSvc = service.NewProjectService(client, s.userService)
	s.glossarySvc = service.NewGlossaryService(client, s.projectSvc)
	s.glossarySyncSvc = service.NewGlossarySyncService(client, s.glossarySvc, s.projectSvc, nil, s.logger)
	project := client.Project.Create().SetName("Sync API").SetOwnerUserID(owner.ID).SetSourceLang("en").SetTargetLang("zh").SaveX(ctx)
	entry := client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("term").SetSource("term").SetTarget("旧").SaveX(ctx)
	res := client.Resource.Create().SetProjectID(project.ID).SetPath("sync.txt").SetFormat("txt").SetStoragePath("unused").SaveX(ctx)
	client.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("term").SetTargetText("旧").SetStatus(segment.StatusTranslated).SaveX(ctx)
	token := authTestToken(t, owner, time.Now().Add(time.Hour), []byte(authTestSecret))
	return s, client, owner, project.ID, entry.ID, token
}

func TestRouterGlossarySyncContract(t *testing.T) {
	ctx := context.Background()
	s, client, owner, projectID, entryID, token := syncAPIFixture(t)
	router := s.newRouter()
	executePath := fmt.Sprintf("/projects/%d/glossary/%d/sync-execute", projectID, entryID)
	w := accountRequest(router, http.MethodPost, executePath, token, `{"old_target":"旧","new_target":"旧新"}`)
	accountAssertStatus(t, w, http.StatusAccepted)
	var accepted GlossarySyncExecuteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	taskID, err := strconv.Atoi(accepted.TaskId)
	if err != nil || taskID <= 0 {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	statusPath := fmt.Sprintf("/projects/%d/sync-tasks/%d", projectID, taskID)
	accountAssertStatus(t, accountRequest(router, http.MethodGet, statusPath, token, ""), http.StatusOK)
	accountAssertProblem(t, accountRequest(router, http.MethodGet, statusPath, "", ""), http.StatusUnauthorized, "")
	for _, id := range []string{"0", "-1", "bad", "9999999999999999999999999999999999999999999"} {
		path := fmt.Sprintf("/projects/%d/sync-tasks/%s", projectID, id)
		accountAssertProblem(t, accountRequest(router, http.MethodGet, path, token, ""), http.StatusBadRequest, "")
		accountAssertProblem(t, accountRequest(router, http.MethodPost, path+"/cancel", token, ""), http.StatusBadRequest, "")
	}
	otherProject := client.Project.Create().SetName("Other owned").SetOwnerUserID(owner.ID).SetSourceLang("en").SetTargetLang("zh").SaveX(ctx)
	wrongPath := fmt.Sprintf("/projects/%d/sync-tasks/%d", otherProject.ID, taskID)
	accountAssertProblem(t, accountRequest(router, http.MethodGet, wrongPath, token, ""), http.StatusNotFound, "")
	accountAssertProblem(t, accountRequest(router, http.MethodPost, wrongPath+"/cancel", token, ""), http.StatusNotFound, "")
	for _, body := range []string{`{"old_target":"","new_target":"x"}`, `{"old_target":"旧","new_target":""}`, `{"old_target":"旧","new_target":"新","resource_ids":[0]}`} {
		accountAssertProblem(t, accountRequest(router, http.MethodPost, executePath, token, body), http.StatusBadRequest, "")
	}
	client.SyncTask.UpdateOneID(taskID).SetResult(`{"total_updated":0,"total_skipped":0,"resources":[]}`).ExecX(ctx)
	for i := 0; i < 2; i++ {
		accountAssertStatus(t, accountRequest(router, http.MethodPost, statusPath+"/cancel", token, ""), http.StatusOK)
	}
	w = accountRequest(router, http.MethodGet, statusPath, token, "")
	accountAssertStatus(t, w, http.StatusOK)
	var status GlossarySyncTaskStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != GlossarySyncTaskStatusResponseStatusCancelled || status.Result == nil {
		t.Fatalf("cancelled partial result=%+v", status)
	}
	for _, terminal := range []string{service.SyncTaskStatusCompleted, service.SyncTaskStatusFailed} {
		client.SyncTask.UpdateOneID(taskID).SetStatus(terminal).ExecX(ctx)
		accountAssertProblem(t, accountRequest(router, http.MethodPost, statusPath+"/cancel", token, ""), http.StatusConflict, "")
	}
}

func TestRouterGlossarySyncOrganizationPermissions(t *testing.T) {
	ctx := context.Background()
	s, client, owner, projectID, entryID, ownerToken := syncAPIFixture(t)
	org, err := s.userService.CreateOrganization(ctx, owner.ID, service.CreateOrganizationInput{Name: "Sync Team", Slug: "sync-team"})
	if err != nil {
		t.Fatal(err)
	}
	client.Project.UpdateOneID(projectID).ClearOwnerUserID().SetOwnerOrgID(org.ID).ExecX(ctx)
	member := client.User.Create().SetUsername("sync-member").SetEmail("sync-member@example.com").SetPasswordHash("unused").SaveX(ctx)
	admin := client.User.Create().SetUsername("sync-admin").SetEmail("sync-admin@example.com").SetPasswordHash("unused").SaveX(ctx)
	outsider := client.User.Create().SetUsername("sync-outsider").SetEmail("sync-outsider@example.com").SetPasswordHash("unused").SaveX(ctx)
	for _, account := range []struct {
		user *ent.User
		role string
	}{{member, "member"}, {admin, "admin"}} {
		role := account.role
		if _, err := s.userService.AddMember(ctx, owner.ID, org.ID, service.AddOrgMemberInput{Username: account.user.Username, Role: &role}); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[int]string{}
	for _, account := range []*ent.User{member, admin, outsider} {
		tokens[account.ID] = authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret))
	}
	router := s.newRouter()
	executePath := fmt.Sprintf("/projects/%d/glossary/%d/sync-execute", projectID, entryID)
	impactPath := fmt.Sprintf("/projects/%d/glossary/%d/sync-impact", projectID, entryID)
	w := accountRequest(router, http.MethodPost, executePath, tokens[admin.ID], `{"old_target":"旧","new_target":"新"}`)
	accountAssertStatus(t, w, http.StatusAccepted)
	var accepted GlossarySyncExecuteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	statusPath := fmt.Sprintf("/projects/%d/sync-tasks/%s", projectID, accepted.TaskId)
	for _, token := range []string{ownerToken, tokens[admin.ID], tokens[member.ID]} {
		accountAssertStatus(t, accountRequest(router, http.MethodGet, statusPath, token, ""), http.StatusOK)
		accountAssertStatus(t, accountRequest(router, http.MethodPost, impactPath, token, `{"old_target":"旧"}`), http.StatusOK)
	}
	for _, token := range []string{tokens[member.ID], tokens[outsider.ID]} {
		accountAssertProblem(t, accountRequest(router, http.MethodPost, executePath, token, `{"old_target":"旧","new_target":"新"}`), http.StatusForbidden, "")
		accountAssertProblem(t, accountRequest(router, http.MethodPost, statusPath+"/cancel", token, ""), http.StatusForbidden, "")
	}
	accountAssertProblem(t, accountRequest(router, http.MethodGet, statusPath, tokens[outsider.ID], ""), http.StatusForbidden, "")
	if err := s.userService.RemoveMember(ctx, owner.ID, org.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	accountAssertProblem(t, accountRequest(router, http.MethodGet, statusPath, tokens[admin.ID], ""), http.StatusForbidden, "")
	accountAssertStatus(t, accountRequest(router, http.MethodPost, statusPath+"/cancel", ownerToken, ""), http.StatusOK)
}
