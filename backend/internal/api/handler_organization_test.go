package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func organizationAPIServer(t *testing.T) (*Server, *ent.Client, *ent.User, string) {
	t.Helper()
	s, client, owner := authTestServer(t)
	s.userService = service.NewUserService(client, nil)
	token := authTestToken(t, owner, time.Now().Add(time.Hour), []byte(authTestSecret))
	return s, client, owner, token
}

func organizationAPIResponse(t *testing.T, body []byte) organizationResponse {
	t.Helper()
	var response organizationResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRouter_OrganizationContract(t *testing.T) {
	s, client, owner, token := organizationAPIServer(t)
	router := s.newRouter()
	w := accountRequest(router, http.MethodPost, "/orgs", token, `{"name":" Team ","slug":" TEAM ","display_name":"Team Name","description":"Original"}`)
	accountAssertStatus(t, w, http.StatusCreated)
	org := organizationAPIResponse(t, w.Body.Bytes())
	if org.Name != "Team" || org.Slug != "team" || org.CurrentUserRole != service.OrgRoleOwner {
		t.Fatalf("created=%+v", org)
	}
	path := fmt.Sprintf("/orgs/%d", org.ID)
	w = accountRequest(router, http.MethodGet, path, token, "")
	accountAssertStatus(t, w, http.StatusOK)
	if detail := organizationAPIResponse(t, w.Body.Bytes()); detail.CurrentUserRole != service.OrgRoleOwner {
		t.Fatalf("detail=%+v", detail)
	}
	w = accountRequest(router, http.MethodGet, "/orgs", token, "")
	accountAssertStatus(t, w, http.StatusOK)
	var list struct {
		Items []organizationResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].CurrentUserRole != service.OrgRoleOwner {
		t.Fatalf("list=%+v", list)
	}
	w = accountRequest(router, http.MethodPut, path, token, `{"name":"Team","slug":"team"}`)
	accountAssertStatus(t, w, http.StatusOK)
	if updated := organizationAPIResponse(t, w.Body.Bytes()); updated.DisplayName != "Team Name" || updated.Description != "Original" {
		t.Fatalf("omitted fields not preserved: %+v", updated)
	}
	w = accountRequest(router, http.MethodPut, path, token, `{"name":"Team","slug":"team","display_name":"","description":""}`)
	accountAssertStatus(t, w, http.StatusOK)
	if updated := organizationAPIResponse(t, w.Body.Bytes()); updated.DisplayName != "" || updated.Description != "" {
		t.Fatalf("empty fields not cleared: %+v", updated)
	}
	for _, conflict := range []struct{ body, urn string }{
		{`{"name":"Team","slug":"other"}`, "urn:linguaflow:organization-name-exists"},
		{`{"name":"Other","slug":"TEAM"}`, "urn:linguaflow:organization-slug-exists"},
	} {
		w = accountRequest(router, http.MethodPost, "/orgs", token, conflict.body)
		accountAssertProblem(t, w, http.StatusConflict, conflict.urn)
	}
	w = accountRequest(router, http.MethodDelete, fmt.Sprintf("%s/members/%d", path, owner.ID), token, "")
	accountAssertProblem(t, w, http.StatusConflict, "urn:linguaflow:owner-required")
	if count := client.Organization.Query().CountX(context.Background()); count != 1 {
		t.Fatalf("conflicts left %d organizations", count)
	}
}

func TestRouter_OrganizationRejectsInvalidRequests(t *testing.T) {
	s, _, _, token := organizationAPIServer(t)
	router := s.newRouter()
	w := accountRequest(router, http.MethodPost, "/orgs", token, `{"name":"Team","slug":"team"}`)
	accountAssertStatus(t, w, http.StatusCreated)
	org := organizationAPIResponse(t, w.Body.Bytes())
	path := fmt.Sprintf("/orgs/%d", org.ID)
	for _, body := range []string{"", `null`, `[]`, `{}`, `{"name":"Team"}`, `{"slug":"team"}`, `{"name":"","slug":"team"}`, `{"name":"Team","slug":" "}`, `{"name":"Team","slug":"team","description":null}`, `{"name":"Team","slug":"team","display_name":null}`, `{"name":"Team","name":"Other","slug":"team"}`, `{"name":"Team","slug":"team","current_user_role":"owner"}`, `{"name":"Team","slug":"team","unexpected":"x"}`, `{"name":3,"slug":"team"}`, `{"name":"Team","slug":"team"} {}`} {
		for _, method := range []string{http.MethodPost, http.MethodPut} {
			target := path
			if method == http.MethodPost {
				target = "/orgs"
			}
			w = accountRequest(router, method, target, token, body)
			accountAssertProblem(t, w, http.StatusBadRequest, "")
		}
	}
	for _, id := range []string{"0", "-1", "no-id", "999999999999999999999999"} {
		w = accountRequest(router, http.MethodGet, "/orgs/"+id, token, "")
		accountAssertProblem(t, w, http.StatusBadRequest, "")
		w = accountRequest(router, http.MethodDelete, path+"/members/"+id, token, "")
		accountAssertProblem(t, w, http.StatusBadRequest, "")
	}
	w = accountRequest(router, http.MethodGet, "/orgs", "", "")
	accountAssertProblem(t, w, http.StatusUnauthorized, "")
}

func TestRouter_OrganizationMemberRolesAndExit(t *testing.T) {
	ctx := context.Background()
	s, client, owner, ownerToken := organizationAPIServer(t)
	router := s.newRouter()
	w := accountRequest(router, http.MethodPost, "/orgs", ownerToken, `{"name":"Team","slug":"team"}`)
	accountAssertStatus(t, w, http.StatusCreated)
	org := organizationAPIResponse(t, w.Body.Bytes())
	path := fmt.Sprintf("/orgs/%d", org.ID)
	member, err := client.User.Create().SetUsername("member").SetEmail("member@example.com").SetPasswordHash("unused").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	memberToken := authTestToken(t, member, time.Now().Add(time.Hour), []byte(authTestSecret))
	admin, err := client.User.Create().SetUsername("org-admin").SetEmail("admin@example.com").SetPasswordHash("unused").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	adminToken := authTestToken(t, admin, time.Now().Add(time.Hour), []byte(authTestSecret))
	w = accountRequest(router, http.MethodPost, path+"/members", ownerToken, `{"username":"org-admin","role":"admin"}`)
	accountAssertStatus(t, w, http.StatusCreated)
	for _, role := range []string{"owner", "admin"} {
		w = accountRequest(router, http.MethodPost, path+"/members", adminToken, fmt.Sprintf(`{"username":"member","role":%q}`, role))
		accountAssertProblem(t, w, http.StatusForbidden, "")
	}
	w = accountRequest(router, http.MethodPost, path+"/members", adminToken, `{"username":"member"}`)
	accountAssertStatus(t, w, http.StatusCreated)
	var membership orgMembershipResponse
	if err := json.Unmarshal(w.Body.Bytes(), &membership); err != nil {
		t.Fatal(err)
	}
	if membership.Role != service.OrgRoleMember || membership.User.ID != member.ID {
		t.Fatalf("membership=%+v", membership)
	}
	w = accountRequest(router, http.MethodPost, path+"/members", ownerToken, `{"username":"member"}`)
	accountAssertProblem(t, w, http.StatusConflict, "urn:linguaflow:membership-exists")
	memberPath := fmt.Sprintf("%s/members/%d", path, member.ID)
	for _, body := range []string{`{}`, `{"role":""}`, `{"role":null}`, `{"role":"unknown"}`, `{"role":"OWNER"}`, `{"role":"owner","username":"member"}`, `{"role":"member","role":"owner"}`} {
		w = accountRequest(router, http.MethodPut, memberPath, ownerToken, body)
		accountAssertProblem(t, w, http.StatusBadRequest, "")
	}
	for _, body := range []string{`{}`, `{"username":"unknown-account"}`, `{"username":"member","role":""}`, `{"username":"member","role":null}`, `{"username":"member","role":"unknown"}`, `{"username":"member","org_id":"1"}`} {
		w = accountRequest(router, http.MethodPost, path+"/members", ownerToken, body)
		accountAssertProblem(t, w, http.StatusBadRequest, "")
	}
	w = accountRequest(router, http.MethodPut, memberPath, adminToken, `{"role":"owner"}`)
	accountAssertProblem(t, w, http.StatusForbidden, "")
	w = accountRequest(router, http.MethodDelete, fmt.Sprintf("%s/members/%d", path, owner.ID), adminToken, "")
	accountAssertProblem(t, w, http.StatusForbidden, "")
	w = accountRequest(router, http.MethodGet, path, memberToken, "")
	accountAssertStatus(t, w, http.StatusOK)
	if detail := organizationAPIResponse(t, w.Body.Bytes()); detail.CurrentUserRole != service.OrgRoleMember {
		t.Fatalf("member role=%+v", detail)
	}
	w = accountRequest(router, http.MethodGet, path+"/members", memberToken, "")
	accountAssertStatus(t, w, http.StatusOK)
	w = accountRequest(router, http.MethodPut, path, memberToken, `{"name":"Team","slug":"team"}`)
	accountAssertProblem(t, w, http.StatusForbidden, "")
	w = accountRequest(router, http.MethodDelete, memberPath, memberToken, "")
	accountAssertStatus(t, w, http.StatusNoContent)
	w = accountRequest(router, http.MethodDelete, memberPath, memberToken, "")
	accountAssertProblem(t, w, http.StatusForbidden, "")
	w = accountRequest(router, http.MethodGet, path, memberToken, "")
	accountAssertProblem(t, w, http.StatusForbidden, "")
	w = accountRequest(router, http.MethodDelete, memberPath, ownerToken, "")
	accountAssertProblem(t, w, http.StatusNotFound, "")
}
