package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
)

// seedOperationRetirement 建立一条不携带 project_id 的父退出任务事实。
func seedOperationRetirement(t *testing.T, c *ent.Client, kind, scope string, ownerID, targetID int, status, phase string, at time.Time) *ent.StorageRetirement {
	t.Helper()
	row, err := c.StorageRetirement.Create().
		SetTargetKind(storageretirement.TargetKind(kind)).SetTargetID(targetID).
		SetTargetIdentity(fmt.Sprintf("identity-%s-%d-%s", kind, targetID, generateUniqueID())).
		SetOwnerKind(storageretirement.OwnerKind(scope)).SetOwnerID(ownerID).SetActorID(ownerID).
		SetConfirmedScope(json.RawMessage(`{"target_kind":"` + kind + `"}`)).
		SetIdempotencyKey(generateUniqueID()).SetRequestHash("retirement-operation-request").
		SetStatus(storageretirement.Status(status)).SetPhase(storageretirement.Phase(phase)).
		SetErrorCode("storage_retirement_dependencies_remaining").SetNextCheckAt(at.Add(time.Hour)).
		SetCreatedAt(at.Add(-time.Hour)).SetUpdatedAt(at).Save(context.Background())
	if err != nil {
		t.Fatalf("seed retirement: %v", err)
	}
	return row
}

func operationRetirementKeys(rows []OperationRow) []string {
	var keys []string
	for _, row := range rows {
		if row.TaskType == OperationStorageRetirement {
			keys = append(keys, fmt.Sprintf("storage_retirement:%d", row.ID()))
		}
	}
	return keys
}

func retirementKeys(ids ...int) []string {
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, fmt.Sprintf("storage_retirement:%d", id))
	}
	slices.Sort(keys)
	return keys
}

func assertRetirementScope(t *testing.T, svc *OperationQueryService, actor int, want ...int) []OperationRow {
	t.Helper()
	page, err := svc.List(context.Background(), actor, OperationListOptions{TaskType: OperationStorageRetirement, AccessibleJobListOptions: AccessibleJobListOptions{State: "all"}})
	if err != nil {
		t.Fatal(err)
	}
	got := operationRetirementKeys(page.Items)
	slices.Sort(got)
	if !slices.Equal(got, retirementKeys(want...)) {
		t.Fatalf("actor %d scope=%v want=%v", actor, got, retirementKeys(want...))
	}
	return page.Items
}

func TestOperationRetirementAuthorizationIsIndependentOfProjectAccess(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	owner := createTestUser(t, c, "retirement-owner")
	member := createTestUser(t, c, "retirement-member")
	admin := createTestUser(t, c, "retirement-admin")
	outsider := createTestUser(t, c, "retirement-outsider")
	c.User.UpdateOneID(admin.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	org := c.Organization.Create().SetName("retirement-org").SetSlug("retirement-org").SaveX(ctx)
	c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(owner.ID).SetRole(OrgRoleOwner).SaveX(ctx)
	c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(admin.ID).SetRole(OrgRoleAdmin).SaveX(ctx)
	c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(member.ID).SetRole(OrgRoleMember).SaveX(ctx)
	at := time.Now().UTC().Add(-time.Minute)

	personal := seedOperationRetirement(t, c, "space", ScopeUser, owner.ID, 11, "running", "draining", at)
	organizational := seedOperationRetirement(t, c, "connection", ScopeOrg, org.ID, 12, "needs_action", "waiting_cleanup", at)
	site := seedOperationRetirement(t, c, "connection", "site", 0, 13, "waiting_retry", "finalizing", at)
	otherPersonal := seedOperationRetirement(t, c, "space", ScopeUser, outsider.ID, 14, "pending", "draining", at)
	// 归属者不存在(例如账号已清理)时不向任何人泄露。
	seedOperationRetirement(t, c, "space", ScopeUser, 9999, 15, "pending", "draining", at)
	svc := NewOperationQueryService(c)

	// 个人退出仅本人可见;组织 owner 额外可见本组织退出,但不因此获得他人的个人退出。
	assertRetirementScope(t, svc, owner.ID, personal.ID, organizational.ID)
	// 组织 admin 可见组织退出,平台管理员可见站点退出;两者都不额外暴露个人退出。
	assertRetirementScope(t, svc, admin.ID, organizational.ID, site.ID)
	// 组织普通成员既不是 owner 也不是 admin。
	assertRetirementScope(t, svc, member.ID)
	assertRetirementScope(t, svc, outsider.ID, otherPersonal.ID)

	// 停用账号不保留任何退出可见性,与 requireOwner 的 active 前提一致。
	c.User.UpdateOneID(admin.ID).SetActive(false).ExecX(ctx)
	assertRetirementScope(t, svc, admin.ID)
	c.User.UpdateOneID(admin.ID).SetActive(true).ExecX(ctx)
}

func TestOperationRetirementListSummaryAndCursor(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "retirement-paging")
	p := createTestProject(t, c, "retirement-paging-project", actor.ID)
	conn := c.StorageConnection.Create().SetName("retirement-connection").SetDriver("s3").SetOwnerKind("user").SetOwnerID(actor.ID).SetAuthSource("stored").SaveX(ctx)
	space := c.StorageSpace.Create().SetConnectionID(conn.ID).SetName("retirement-space").SetIdentity("retirement-space-identity").SetMarkerNonce("retirement-space-nonce").SetOwnerKind("user").SetOwnerID(actor.ID).SaveX(ctx)
	at := time.Date(2026, 10, 14, 3, 4, 5, 123456789, time.UTC)
	now := at.Add(time.Hour)
	var retirements []int
	for i, status := range []string{"pending", "running", "waiting_retry", "needs_action", "completed", "failed", "cancelled"} {
		row := seedOperationRetirement(t, c, "space", ScopeUser, actor.ID, space.ID, status, "draining", at.Add(time.Duration(i)*time.Second))
		retirements = append(retirements, row.ID)
	}
	// 连接目标与仍需保持可读的 tombstone 名称。
	connectionTarget := seedOperationRetirement(t, c, "connection", ScopeUser, actor.ID, conn.ID, "pending", "draining", at.Add(8*time.Second))
	missingTarget := seedOperationRetirement(t, c, "space", ScopeUser, actor.ID, 99999, "pending", "draining", at.Add(9*time.Second))
	seedQueryJob(t, c, p.ID, "pending", "manual", at)
	seedOperationSync(t, c, p.ID, actor.ID, "pending", at)
	seedOperationStorage(t, c, p.ID, actor.ID, "pending", at)
	svc := NewOperationQueryService(c)

	// 9 条父退出任务(7 个空间状态 + 1 个连接目标 + 1 个已不存在的目标)加上三类旧投影。
	all, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all"}})
	if err != nil || len(all.Items) != 12 {
		t.Fatalf("mixed retirement list=%+v err=%v", all, err)
	}
	names := map[int]string{}
	for _, row := range all.Items {
		if row.Retirement == nil {
			continue
		}
		// 父任务不携带 project_id,也不借用项目名。
		if row.ProjectID() != 0 || row.ProjectName != "" {
			t.Fatalf("retirement leaked project scope: %+v", row)
		}
		names[row.Retirement.ID] = row.RetirementName
	}
	if len(names) != 9 {
		t.Fatalf("retirement rows=%d", len(names))
	}
	if names[connectionTarget.ID] != "retirement-connection" || names[retirements[0]] != "retirement-space" {
		t.Fatalf("target names=%v", names)
	}
	// 目标行已不存在时留空,不伪造名称也不回退到不透明身份标识。
	if names[missingTarget.ID] != "" {
		t.Fatalf("missing target name=%q", names[missingTarget.ID])
	}

	cursor, got := "", []string{}
	for {
		page, err := svc.List(ctx, actor.ID, OperationListOptions{AccessibleJobListOptions: AccessibleJobListOptions{State: "all", Limit: 1, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, operationKeys(page.Items)...)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if !slices.Equal(got, operationKeys(all.Items)) {
		t.Fatalf("retirement pagination=%v", got)
	}

	// 显式 project_id 排除没有项目归属的父任务,避免用项目权限推断连接管理权限。
	for _, opts := range []OperationListOptions{
		{TaskType: OperationStorageRetirement, AccessibleJobListOptions: AccessibleJobListOptions{State: "all", ProjectID: p.ID}},
		{AccessibleJobListOptions: AccessibleJobListOptions{State: "all", ProjectID: p.ID}},
	} {
		page, err := svc.List(ctx, actor.ID, opts)
		if err != nil || len(operationRetirementKeys(page.Items)) != 0 {
			t.Fatalf("project exclusion failed: %+v err=%v", page, err)
		}
	}

	summary, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := OperationCounts{Pending: 3, Running: 1, WaitingRetry: 1, NeedsAction: 1, RecentFailed: 1}
	if summary.ByType.StorageRetirement != want {
		t.Fatalf("by_type.storage_retirement=%+v", summary.ByType.StorageRetirement)
	}
	if summary.Total != (OperationCounts{Pending: 6, Running: 1, WaitingRetry: 1, NeedsAction: 1, RecentFailed: 1}) {
		t.Fatalf("total=%+v", summary.Total)
	}
	scoped, err := svc.summaryAt(ctx, actor.ID, OperationSummaryOptions{ProjectID: p.ID}, now)
	if err != nil || scoped.ByType.StorageRetirement != (OperationCounts{}) {
		t.Fatalf("project-scoped summary=%+v err=%v", scoped, err)
	}

	// 终态过滤与任务种类过滤共用同一游标契约。
	terminal, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: OperationStorageRetirement, AccessibleJobListOptions: AccessibleJobListOptions{State: "terminal"}})
	if err != nil || len(terminal.Items) != 3 {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
	for _, row := range terminal.Items {
		if row.Retirement == nil || (row.Retirement.Status != storageretirement.StatusCompleted && row.Retirement.Status != storageretirement.StatusFailed && row.Retirement.Status != storageretirement.StatusCancelled) {
			t.Fatalf("terminal row=%+v", row)
		}
	}
	if page, err := svc.List(ctx, actor.ID, OperationListOptions{TaskType: OperationStorageRetirement, AccessibleJobListOptions: AccessibleJobListOptions{Status: JobStatusPausing}}); err != nil || len(page.Items) != 0 {
		t.Fatalf("pausing must not match a retirement: %+v err=%v", page, err)
	}
}

func TestOperationRetirementRecentFailedWindow(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	actor := createTestUser(t, c, "retirement-recent-failed")
	now := time.Date(2026, 10, 22, 3, 4, 5, 123456789, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)

	for i, expected := range []int{0, 1, 2, 2, 2} {
		at := []time.Time{
			since.Add(-time.Nanosecond),
			since,
			now.Add(-time.Nanosecond),
			now,
			now.Add(time.Nanosecond),
		}[i]
		seedOperationRetirement(t, c, "space", ScopeUser, actor.ID, i+1, "failed", "draining", at)
		summary, err := NewOperationQueryService(c).summaryAt(ctx, actor.ID, OperationSummaryOptions{}, now)
		if err != nil {
			t.Fatal(err)
		}
		if summary.AsOf != now || summary.RecentFailedSince != since {
			t.Fatalf("failure window at boundary %d=[%s,%s)", i, summary.RecentFailedSince, summary.AsOf)
		}
		if summary.ByType.StorageRetirement.RecentFailed != expected || summary.Total.RecentFailed != expected {
			t.Fatalf("recent failed retirement counts at boundary %d: by_type=%d total=%d want=%d", i, summary.ByType.StorageRetirement.RecentFailed, summary.Total.RecentFailed, expected)
		}
	}
}
