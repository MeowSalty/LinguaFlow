package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
)

func historyFixture(t *testing.T) (*ent.Client, *UserService, *ProjectService, *AuditService, *ent.User, *ent.User, *ent.Organization, *ent.Project) {
	t.Helper()
	client := testClient(t)
	ctx := context.Background()
	owner := createTestUser(t, client, "history-owner")
	member := createTestUser(t, client, "history-member")
	org := client.Organization.Create().SetName("history").SetSlug("history").SaveX(ctx)
	client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(owner.ID).SetRole(OrgRoleOwner).SaveX(ctx)
	client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(member.ID).SetRole(OrgRoleMember).SaveX(ctx)
	p := client.Project.Create().SetName("org-project").SetOwnerOrgID(org.ID).SaveX(ctx)
	users := NewUserService(client, nil)
	projects := NewProjectService(client, users)
	return client, users, projects, NewAuditService(client, users, projects), owner, member, org, p
}

func TestHistoryAuthorizationCurrentMembershipAndOwnerPrecedence(t *testing.T) {
	client, _, projects, audit, owner, member, org, p := historyFixture(t)
	ctx := context.Background()
	alien := createTestUser(t, client, "history-alien")
	dual := client.Project.Create().SetName("dual").SetOwnerUserID(alien.ID).SetOwnerOrgID(org.ID).SaveX(ctx)
	for _, projectRow := range []*ent.Project{p, dual} {
		if err := audit.Record(ctx, AuditEvent{ActorUserID: member.ID, ProjectID: &projectRow.ID,
			Action: "job.create", ResourceType: "job", ResourceID: 1}); err != nil {
			t.Fatal(err)
		}
		client.UsageRecord.Create().SetVisibilityScope(usagerecord.VisibilityScopeProject).
			SetProjectID(projectRow.ID).SetOrganizationID(org.ID).SetUserID(member.ID).SetAPICalls(10).SaveX(ctx)
	}
	if err := audit.Record(ctx, AuditEvent{ActorUserID: member.ID, VisibilityScope: "personal", Action: "quick_translate", ResourceType: "quick_translate"}); err != nil {
		t.Fatal(err)
	}
	client.UsageRecord.Create().SetVisibilityScope(usagerecord.VisibilityScopePersonal).SetUserID(member.ID).SetAPICalls(2).SetInputTokens(7).SaveX(ctx)
	// Legacy, unknown and conflicting personal rows cannot use their actor/org to bypass scope.
	for _, scope := range []activitylog.VisibilityScope{activitylog.VisibilityScopeLegacy, activitylog.VisibilityScopeUnknown, activitylog.VisibilityScopePersonal} {
		client.ActivityLog.Create().SetAction("admin.user.update").SetResourceType("user").SetActorID(member.ID).
			SetOrganizationID(org.ID).SetVisibilityScope(scope).SaveX(ctx)
	}
	page, err := audit.ListActivity(ctx, member.ID, 0, 50)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("activities=%v err=%v", page, err)
	}
	page, err = audit.ListActivity(ctx, member.ID, 0, 50, org.ID)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("org activities=%v err=%v", page, err)
	}
	listing, err := projects.ListOrgProjects(ctx, owner.ID, org.ID)
	if err != nil || len(listing) != 1 || listing[0].ID != p.ID {
		t.Fatalf("org projects=%v err=%v", listing, err)
	}
	if _, err := projects.CreateOrgProject(ctx, member.ID, org.ID, CreateProjectInput{Name: "denied"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create=%v", err)
	}
	stats := NewStatsService(client, projects)
	summary, err := stats.Summary(ctx, member.ID)
	if err != nil || summary.APICalls != 12 || summary.UsageRecords != 2 || summary.InputTokens != 7 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	memberships, err := client.OrgMembership.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, membership := range memberships {
		u, err := membership.QueryUser().Only(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if u.ID == member.ID {
			if err := client.OrgMembership.DeleteOne(membership).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err = audit.ListActivity(ctx, member.ID, 0, 50)
	if err != nil || len(page.Items) != 1 || page.Items[0].Action != "quick_translate" {
		t.Fatalf("revoked activities=%v err=%v", page, err)
	}
	summary, err = stats.Summary(ctx, member.ID)
	if err != nil || summary.APICalls != 2 {
		t.Fatalf("revoked summary=%+v err=%v", summary, err)
	}
	if _, err := audit.ListActivity(ctx, member.ID, 0, 50, org.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked org=%v", err)
	}
	summary, err = stats.Summary(ctx, createTestUser(t, client, "history-empty").ID)
	if err != nil || *summary != (UsageStats{}) {
		t.Fatalf("empty summary=%+v err=%v", summary, err)
	}
}

func TestHistoryMigrationPreservesUnknownAndTimestamps(t *testing.T) {
	client, _, _, audit, _, member, org, p := historyFixture(t)
	ctx := context.Background()
	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	j := client.Job.Create().SetProjectID(p.ID).SetExecutionPlanID(1).SaveX(ctx)
	recoverable := client.ActivityLog.Create().SetAction("job.create").SetResourceType("job").SetResourceID(j.ID).SetActorID(member.ID).SetUpdatedAt(stamp).SaveX(ctx)
	unresolved := client.ActivityLog.Create().SetAction("job.create").SetResourceType("job").SetResourceID(j.ID + 1).SetActorID(member.ID).SetOrganizationID(org.ID).SaveX(ctx)
	admin := client.ActivityLog.Create().SetAction("admin.user.update").SetResourceType("user").SetActorID(member.ID).SetProjectID(p.ID).SaveX(ctx)
	personal := client.ActivityLog.Create().SetAction("quick_translate").SetResourceType("quick_translate").SetActorID(member.ID).SaveX(ctx)
	linked := client.UsageRecord.Create().SetProjectID(p.ID).SetUserID(member.ID).SetUpdatedAt(stamp).SaveX(ctx)
	orphan := client.UsageRecord.Create().SetOrganizationID(org.ID).SetUserID(member.ID).SaveX(ctx)
	for i := 0; i < 2; i++ {
		if err := MigrateHistoryVisibility(ctx, client); err != nil {
			t.Fatal(err)
		}
	}
	recovered := client.ActivityLog.GetX(ctx, recoverable.ID)
	if recovered.VisibilityScope != activitylog.VisibilityScopeProject || !recovered.UpdatedAt.Equal(stamp) {
		t.Fatalf("recovered=%+v", recovered)
	}
	if got := client.ActivityLog.GetX(ctx, unresolved.ID); got.VisibilityScope != activitylog.VisibilityScopeUnknown {
		t.Fatalf("orphan=%+v", got)
	}
	if got := client.ActivityLog.GetX(ctx, admin.ID); got.VisibilityScope != activitylog.VisibilityScopeUnknown {
		t.Fatalf("admin=%+v", got)
	}
	if got := client.ActivityLog.GetX(ctx, personal.ID); got.VisibilityScope != activitylog.VisibilityScopePersonal {
		t.Fatalf("personal=%+v", got)
	}
	if got := client.UsageRecord.GetX(ctx, linked.ID); got.VisibilityScope != usagerecord.VisibilityScopeProject || !got.UpdatedAt.Equal(stamp) {
		t.Fatalf("linked usage=%+v", got)
	}
	if got := client.UsageRecord.GetX(ctx, orphan.ID); got.VisibilityScope != usagerecord.VisibilityScopeUnknown {
		t.Fatalf("orphan usage=%+v", got)
	}
	client.Job.Create().SetProjectID(p.ID).SetExecutionPlanID(1).SaveX(ctx)
	if err := MigrateHistoryVisibility(ctx, client); err != nil {
		t.Fatal(err)
	}
	if got := client.ActivityLog.GetX(ctx, unresolved.ID); got.VisibilityScope != activitylog.VisibilityScopeUnknown {
		t.Fatal("unknown was reclaimed")
	}
	page, err := audit.ListActivity(ctx, member.ID, 0, 50)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestHistoryProjectDeletionKeepsOrganizationAndHidesPersonalHistory(t *testing.T) {
	client, _, projects, audit, owner, member, org, p := historyFixture(t)
	ctx := context.Background()
	personal := createTestProject(t, client, "personal-history", owner.ID)
	dual := client.Project.Create().SetName("dual-history").SetOwnerUserID(owner.ID).SetOwnerOrgID(org.ID).SaveX(ctx)
	for _, row := range []*ent.Project{p, personal, dual} {
		if err := audit.Record(ctx, AuditEvent{ActorUserID: owner.ID, ProjectID: &row.ID, Action: "qa.recheck", ResourceType: "project", ResourceID: row.ID}); err != nil {
			t.Fatal(err)
		}
		usage := client.UsageRecord.Create().SetVisibilityScope(usagerecord.VisibilityScopeProject).SetProjectID(row.ID).SetUserID(owner.ID).SetAPICalls(3)
		if orgID := EffectiveProjectOrgID(row); orgID != nil {
			usage.SetOrganizationID(*orgID)
		}
		usage.SaveX(ctx)
	}
	for _, row := range []*ent.Project{p, personal, dual} {
		if _, err := projects.DeleteProject(ctx, owner.ID, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	page, err := audit.ListActivity(ctx, member.ID, 0, 50, org.ID)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("deleted org history=%+v err=%v", page, err)
	}
	ownerPage, err := audit.ListActivity(ctx, owner.ID, 0, 50)
	if err != nil || len(ownerPage.Items) != 2 {
		t.Fatalf("deleted personal leaked=%+v err=%v", ownerPage, err)
	}
	stats, err := NewStatsService(client, projects).Summary(ctx, owner.ID)
	if err != nil || stats.APICalls != 3 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestHistoryAuditFailureRollsBackProjectMutation(t *testing.T) {
	client, _, projects, _, owner, _, org, p := historyFixture(t)
	ctx := context.Background()
	sentinel := errors.New("audit unavailable")
	client.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) { return nil, sentinel })
	})
	if _, err := projects.CreateOrgProject(ctx, owner.ID, org.ID, CreateProjectInput{Name: "rolled-back"}); !errors.Is(err, sentinel) {
		t.Fatalf("create=%v", err)
	}
	if count := client.Project.Query().CountX(ctx); count != 1 {
		t.Fatalf("project creation leaked: %d", count)
	}
	if _, err := projects.UpdateProject(ctx, owner.ID, p.ID, UpdateProjectInput{Name: "changed"}); !errors.Is(err, sentinel) {
		t.Fatalf("update=%v", err)
	}
	if current := client.Project.GetX(ctx, p.ID); current.Name != p.Name {
		t.Fatalf("update leaked=%s", current.Name)
	}
	if _, err := projects.DeleteProject(ctx, owner.ID, p.ID); !errors.Is(err, sentinel) {
		t.Fatalf("delete=%v", err)
	}
	if !client.Project.Query().ExistX(ctx) {
		t.Fatal("delete leaked")
	}
}

func TestHistoryMetadataAllowlist(t *testing.T) {
	got := SanitizeActivityMetadata("segment.search_replace", map[string]any{"find": "secret", "replace_with": "private", "applied_count": 3, "operation_id": "op"})
	want := map[string]any{"applied_count": 3, "operation_id": "op"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata=%#v", got)
	}
	got = SanitizeActivityMetadata("organization.member.role", map[string]any{"old_role": "member", "new_role": "owner", "token": "secret"})
	if len(got) != 2 {
		t.Fatalf("roles=%#v", got)
	}
}
