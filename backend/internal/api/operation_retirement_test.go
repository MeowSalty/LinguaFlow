package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
)

// TestOperationsHTTPProjectsStorageRetirementParent 固定父退出任务在任务中心的
// 线路格式:不输出 project_id/project_name,can_delete 恒为 false,动作仅 view/resume/cancel。
func TestOperationsHTTPProjectsStorageRetirementParent(t *testing.T) {
	s, c, u := jobQueryTestServer(t)
	ctx := context.Background()
	conn := c.StorageConnection.Create().SetName("retiring-connection").SetDriver("s3").SetOwnerKind("user").SetOwnerID(u.ID).SetAuthSource("stored").SaveX(ctx)
	space := c.StorageSpace.Create().SetConnectionID(conn.ID).SetName("retiring-space").SetIdentity("http-retirement-space").SetMarkerNonce("http-retirement-nonce").SetOwnerKind("user").SetOwnerID(u.ID).SaveX(ctx)
	row := c.StorageRetirement.Create().
		SetTargetKind(storageretirement.TargetKindSpace).SetTargetID(space.ID).
		SetTargetIdentity("http-retirement-identity").
		SetOwnerKind(storageretirement.OwnerKindUser).SetOwnerID(u.ID).SetActorID(u.ID).
		SetConfirmedScope(json.RawMessage(`{"target_kind":"space"}`)).
		SetIdempotencyKey("http-retirement-key").SetRequestHash("http-retirement-request").
		SetStatus(storageretirement.StatusNeedsAction).SetPhase(storageretirement.PhaseFinalizing).
		SetErrorCode("storage_retirement_dependencies_remaining").
		SetNextCheckAt(time.Now().UTC().Add(time.Hour)).SaveX(ctx)
	// 他人的个人退出不得出现在当前账号的列表里。
	other := c.User.Create().SetUsername("http-retirement-other").SetEmail("http-retirement-other@test.com").SetPasswordHash("$2a$10$dummy").SaveX(ctx)
	c.StorageRetirement.Create().
		SetTargetKind(storageretirement.TargetKindSpace).SetTargetID(space.ID).
		SetTargetIdentity("http-retirement-other-identity").
		SetOwnerKind(storageretirement.OwnerKindUser).SetOwnerID(other.ID).SetActorID(other.ID).
		SetConfirmedScope(json.RawMessage(`{"target_kind":"space"}`)).
		SetIdempotencyKey("http-retirement-other-key").SetRequestHash("http-retirement-other-request").
		SetStatus(storageretirement.StatusPending).SetPhase(storageretirement.PhaseDraining).SaveX(ctx)

	token := authTestToken(t, u, time.Now().Add(time.Hour), []byte(authTestSecret))
	router := s.newRouter()
	w := jobQueryRequest(router, "/api/v1/operations?task_type=storage_retirement", token)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%s", w.Body.String())
	}
	item := page.Items[0]
	if item["task_type"] != "storage_retirement" || item["task_id"] != fmt.Sprint(row.ID) {
		t.Fatalf("identity=%v", item)
	}
	if _, ok := item["project_id"]; ok {
		t.Fatalf("project_id must not be emitted: %v", item)
	}
	if _, ok := item["project_name"]; ok {
		t.Fatalf("project_name must not be emitted: %v", item)
	}
	if item["scope"] != "user" || item["owner_id"] != float64(u.ID) {
		t.Fatalf("ownership=%v", item)
	}
	if item["target_kind"] != "space" || item["target_id"] != float64(space.ID) || item["name"] != "retiring-space" {
		t.Fatalf("target=%v", item)
	}
	if item["status"] != "needs_action" || item["phase"] != "finalizing" || item["error_code"] != "storage_retirement_dependencies_remaining" {
		t.Fatalf("state=%v", item)
	}
	if item["can_delete"] != false {
		t.Fatalf("can_delete=%v", item["can_delete"])
	}
	if item["started_at"] != nil || item["finished_at"] != nil {
		t.Fatalf("timestamps=%v", item)
	}
	if _, ok := item["next_check_at"].(string); !ok {
		t.Fatalf("next_check_at=%v", item["next_check_at"])
	}
	actions, ok := item["supported_actions"].([]any)
	if !ok || len(actions) != 3 || actions[0] != "view" || actions[1] != "resume" || actions[2] != "cancel" {
		t.Fatalf("supported_actions=%v", item["supported_actions"])
	}
	if strings.Contains(w.Body.String(), "http-retirement-other") {
		t.Fatalf("foreign retirement leaked: %s", w.Body.String())
	}

	summary := jobQueryRequest(router, "/api/v1/operations/summary", token)
	if summary.Code != 200 {
		t.Fatalf("summary status=%d body=%s", summary.Code, summary.Body.String())
	}
	var counts struct {
		Total struct {
			NeedsAction int `json:"needs_action"`
		} `json:"total"`
		ByType struct {
			StorageRetirement struct {
				NeedsAction int `json:"needs_action"`
			} `json:"storage_retirement"`
		} `json:"by_type"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	if counts.ByType.StorageRetirement.NeedsAction != 1 || counts.Total.NeedsAction != 1 {
		t.Fatalf("summary=%s", summary.Body.String())
	}
}
