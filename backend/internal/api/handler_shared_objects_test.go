package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func sharedHTTPServer(t *testing.T) (*Server, *ent.Client, *ent.User) {
	t.Helper()
	s, client, actor := authTestServer(t)
	s.userService = service.NewUserService(client, nil)
	s.translationPromptTemplateSvc = service.NewTranslationPromptTemplateService(client)
	s.bootstrapPromptTemplateSvc = service.NewBootstrapPromptTemplateService(client)
	s.prunePromptTemplateSvc = service.NewPrunePromptTemplateService(client)
	s.executionProfileSvc = service.NewExecutionProfileService(client, s.userService)
	s.executionPlanSvc = service.NewExecutionPlanService(client, s.userService, s.executionProfileSvc)
	s.executionPlanHandler = NewHandlerExecutionPlan(s.executionPlanSvc, s)
	return s, client, actor
}

func sharedHTTPRequest(router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestRouter_SharedObjectsOrganizationContract(t *testing.T) {
	cases := []struct{ path, extra string }{
		{"translation-prompt-templates", `,"system_prompt_content":"private body"`},
		{"bootstrap-prompt-templates", `,"content":"private body"`},
		{"prune-prompt-templates", `,"content":"private body"`},
		{"execution-profiles", ""},
		{"execution-plan-templates", `,"profile_id":-1,"rounds":[{"mode":"correct","concurrency":1,"correct":{"rules":[{"name":"punctuation_missing_wrap","enabled":true}]}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			ctx := context.Background()
			s, client, owner := sharedHTTPServer(t)
			createUser := func(name string) *ent.User {
				user, err := client.User.Create().SetUsername(name).SetEmail(name + "@test.com").SetPasswordHash("hash").Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return user
			}
			admin, member, outsider := createUser("admin"), createUser("member"), createUser("outsider")
			org, err := client.Organization.Create().SetName("team").SetSlug("team").Save(ctx)
			if err != nil {
				t.Fatal(err)
			}
			add := func(actor *ent.User, role string) *ent.OrgMembership {
				row, err := client.OrgMembership.Create().SetOrganization(org).SetUser(actor).SetRole(role).Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
				return row
			}
			add(owner, "owner")
			adminMembership := add(admin, "admin")
			memberMembership := add(member, "member")
			token := func(actor *ent.User) string {
				return authTestToken(t, actor, time.Now().Add(time.Hour), []byte(authTestSecret))
			}
			ownerToken, adminToken, memberToken, outsiderToken := token(owner), token(admin), token(member), token(outsider)
			router := s.newRouter()
			path := "/api/v1/" + tc.path
			body := fmt.Sprintf(`{"name":"shared","org_id":%d%s}`, org.ID, tc.extra)
			request := func(method, target, rawToken, rawBody string, status int) *httptest.ResponseRecorder {
				t.Helper()
				w := sharedHTTPRequest(router, method, target, rawToken, rawBody)
				if w.Code != status {
					t.Fatalf("%s %s = %d want %d: %s", method, target, w.Code, status, w.Body.String())
				}
				return w
			}
			request(http.MethodGet, path, "", "", 401)
			request(http.MethodPost, path, memberToken, body, 403)
			request(http.MethodPost, path, outsiderToken, body, 403)
			w := request(http.MethodPost, path, adminToken, body, 201)
			var created struct {
				ID          int    `json:"id"`
				Scope       string `json:"scope"`
				OwnerOrgID  *int   `json:"owner_org_id"`
				OwnerUserID *int   `json:"owner_user_id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			if created.Scope != "org" || created.OwnerOrgID == nil || *created.OwnerOrgID != org.ID || created.OwnerUserID != nil {
				t.Fatalf("incorrect organization ownership: %s", w.Body.String())
			}
			detail := path + "/" + strconv.Itoa(created.ID)
			request(http.MethodGet, detail, memberToken, "", 200)
			request(http.MethodGet, detail, outsiderToken, "", 404)
			request(http.MethodPut, detail, outsiderToken, `{"name":"updated"}`, 404)
			request(http.MethodDelete, detail, outsiderToken, "", 404)
			request(http.MethodPut, detail, memberToken, `{"name":"updated"}`, 403)
			request(http.MethodDelete, detail, memberToken, "", 403)
			w = request(http.MethodGet, path+"?org_id="+strconv.Itoa(org.ID), memberToken, "", 200)
			var listing struct {
				Items []struct {
					ID    int    `json:"id"`
					Scope string `json:"scope"`
				} `json:"items"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
				t.Fatal(err)
			}
			if len(listing.Items) != 1 || listing.Items[0].ID != created.ID || listing.Items[0].Scope != "org" {
				t.Fatalf("organization filter: %s", w.Body.String())
			}
			request(http.MethodGet, path+"?org_id="+strconv.Itoa(org.ID), outsiderToken, "", 403)
			request(http.MethodGet, path+"?org_id=999999", ownerToken, "", 404)
			for _, query := range []string{"?org_id=0", "?org_id=-1", "?org_id=", "?org_id=1&org_id=2", "?unknown=1"} {
				request(http.MethodGet, path+query, ownerToken, "", 400)
			}
			for _, field := range []string{
				`"org_id":null`, `"org_id":0`, `"org_id":-1`, `"org_id":"1"`,
				`"org_id":1,"org_id":1`, `"owner_user_id":1`, `"owner_org_id":1`, `"scope":"org"`,
				`"ORG_ID":null`, fmt.Sprintf(`"org_id":%d,"ORG_ID":null`, org.ID),
			} {
				request(http.MethodPost, path, ownerToken, `{"name":"invalid",`+field+tc.extra+`}`, 400)
			}
			request(http.MethodPost, path, ownerToken, `{"name":"first","name":"second"`+tc.extra+`}`, 400)
			request(http.MethodPut, detail, ownerToken, `{"org_id":2}`, 400)
			request(http.MethodPut, detail, ownerToken, `{"owner_user_id":1}`, 400)
			request(http.MethodPut, detail, ownerToken, `{"name":"ok"} {}`, 400)
			request(http.MethodPut, detail, adminToken, `{"name":"updated"}`, 200)
			if err := client.OrgMembership.UpdateOne(adminMembership).SetRole("member").Exec(ctx); err != nil {
				t.Fatal(err)
			}
			request(http.MethodPut, detail, adminToken, `{"name":"denied"}`, 403)
			if err := client.OrgMembership.DeleteOne(memberMembership).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			request(http.MethodGet, detail, memberToken, "", 404)
			request(http.MethodGet, path+"?org_id="+strconv.Itoa(org.ID), memberToken, "", 403)
			request(http.MethodDelete, detail, ownerToken, "", 204)
			request(http.MethodDelete, detail, ownerToken, "", 404)
			personalBody := `{"name":"personal"` + tc.extra + `}`
			w = request(http.MethodPost, path, ownerToken, personalBody, 201)
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			// Decode into a fresh value because omitted ownership fields are meaningful.
			var personal map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &personal); err != nil {
				t.Fatal(err)
			}
			if personal["scope"] != "user" || personal["owner_user_id"] != float64(owner.ID) || personal["owner_org_id"] != nil {
				t.Fatalf("personal creation compatibility: %s", w.Body.String())
			}
		})
	}
}

func TestSharedJSONRejectsNestedDuplicateFields(t *testing.T) {
	s, _, _ := sharedHTTPServer(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"config":{"qa":{"enabled":true,"enabled":false}}}`))
	w := httptest.NewRecorder()
	var dst map[string]any
	if s.decodeSharedJSON(w, req, &dst) || w.Code != 400 {
		t.Fatalf("nested duplicate accepted: %d", w.Code)
	}
}
