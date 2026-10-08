package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestRouterOrganizationJobSnapshotRedactionAndRevocation(t *testing.T) {
	s, client, owner := jobQueryTestServer(t)
	ctx := context.Background()
	member := client.User.Create().SetUsername("snapshot-reader").SetEmail("reader@test.com").SetPasswordHash("hash").SaveX(ctx)
	org, err := s.userService.CreateOrganization(ctx, owner.ID, service.CreateOrganizationInput{Name: "Snapshot Team", Slug: "snapshot-team"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.userService.AddMember(ctx, owner.ID, org.ID, service.AddOrgMemberInput{Username: member.Username}); err != nil {
		t.Fatal(err)
	}
	project := client.Project.Create().SetName("snapshot-project").SetOwnerOrgID(org.ID).SaveX(ctx)
	backendNode := func(secret string) map[string]any {
		return map[string]any{"backend": map[string]any{"options": map[string]any{"api_key": secret, "model": "test"}}}
	}
	config := map[string]any{
		"rounds":     []any{backendNode("secret-round")},
		"bootstrap":  backendNode("secret-bootstrap"),
		"ruby_retry": backendNode("secret-retry"),
	}
	job := client.Job.Create().SetProjectID(project.ID).SetCreatedByID(owner.ID).SetExecutionPlanID(1).SetExecutionConfig(config).SaveX(ctx)
	token := authTestToken(t, member, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	path := fmt.Sprintf("/api/v1/jobs/%d", job.ID)
	w := jobQueryRequest(router, path, token)
	if w.Code != http.StatusOK {
		t.Fatalf("detail=%d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"secret-round", "secret-bootstrap", "secret-retry"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("member response leaked %q", secret)
		}
	}
	if strings.Count(w.Body.String(), `"api_key":"***"`) != 3 {
		t.Fatalf("snapshot secrets were not masked: %s", w.Body.String())
	}
	persisted, err := json.Marshal(client.Job.GetX(ctx, job.ID).ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-round", "secret-bootstrap", "secret-retry"} {
		if !strings.Contains(string(persisted), secret) {
			t.Fatalf("response rendering mutated worker snapshot: %q", secret)
		}
	}
	if err := s.userService.RemoveMember(ctx, owner.ID, org.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if w := jobQueryRequest(router, path, token); w.Code != http.StatusForbidden {
		t.Fatalf("revoked detail=%d %s", w.Code, w.Body.String())
	}
}
