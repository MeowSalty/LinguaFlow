package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// 可选字段无值时必须整体省略而不是输出 null，否则违反规范的不可空声明
// （前端按 next_cursor === undefined 判断是否还有下一页）。
func TestStorageDiagnosticsResponseOmitsAbsentOptionals(t *testing.T) {
	in := &service.StorageDiagnostics{
		Disks:  []service.StorageDiskDiagnostic{{Roles: []string{"work"}, State: "available"}},
		Spaces: []service.StorageSpaceDiagnostics{{ID: 1}},
	}
	payload, err := json.Marshal(toStorageDiagnosticsResponse(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"next_cursor", "oldest_intent_at", "latest_backup"} {
		if _, ok := doc[key]; ok {
			t.Fatalf("可选字段无值时应整体省略，%q 仍出现在响应中: %s", key, payload)
		}
	}
	space := doc["spaces"].([]any)[0].(map[string]any)
	if _, ok := space["last_checked_at"]; ok {
		t.Fatalf("未校验的空间不应输出 last_checked_at: %s", payload)
	}
}

func TestStorageDiagnosticsResponseKeepsPresentOptionals(t *testing.T) {
	observed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cursor := 9
	in := &service.StorageDiagnostics{
		Disks:          []service.StorageDiskDiagnostic{{Roles: []string{"work"}, State: "low", ObservedAt: observed}},
		Spaces:         []service.StorageSpaceDiagnostics{{ID: 1, LastCheckedAt: &observed}},
		NextCursor:     &cursor,
		OldestIntentAt: &observed,
		LatestBackup:   &service.StorageBackupDiagnostic{ID: 3, Status: "complete", CreatedAt: observed},
	}
	payload, err := json.Marshal(toStorageDiagnosticsResponse(in))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["next_cursor"] != float64(cursor) {
		t.Fatalf("next_cursor = %v, want %d", doc["next_cursor"], cursor)
	}
	if doc["oldest_intent_at"] == nil {
		t.Fatalf("oldest_intent_at 不应缺失: %s", payload)
	}
	backup := doc["latest_backup"].(map[string]any)
	if backup["status"] != "complete" || backup["id"] != float64(3) {
		t.Fatalf("latest_backup = %v", backup)
	}
	space := doc["spaces"].([]any)[0].(map[string]any)
	if space["last_checked_at"] == nil {
		t.Fatalf("last_checked_at 不应缺失: %s", payload)
	}
}
