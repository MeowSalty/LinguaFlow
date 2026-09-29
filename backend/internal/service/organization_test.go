package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func organizationRole(role string) *string { return &role }

type organizationFixture struct {
	client *ent.Client
	svc    *UserService
	org    *OrganizationView
	owner  *ent.User
	admin  *ent.User
	member *ent.User
	other  *ent.User
}

func newOrganizationFixture(t *testing.T) organizationFixture {
	t.Helper()
	client := testClient(t)
	svc := NewUserService(client, nil)
	owner := createTestUser(t, client, "owner")
	admin := createTestUser(t, client, "admin")
	member := createTestUser(t, client, "member")
	other := createTestUser(t, client, "other")
	org, err := svc.CreateOrganization(context.Background(), owner.ID, CreateOrganizationInput{Name: "Team", Slug: "team", DisplayName: "Same display", Description: "Description"})
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct {
		user *ent.User
		role string
	}{{admin, OrgRoleAdmin}, {member, OrgRoleMember}} {
		if _, err := svc.AddMember(context.Background(), owner.ID, org.ID, AddOrgMemberInput{Username: account.user.Username, Role: organizationRole(account.role)}); err != nil {
			t.Fatal(err)
		}
	}
	return organizationFixture{client: client, svc: svc, org: org, owner: owner, admin: admin, member: member, other: other}
}

func TestOrganizationCreateUpdateContract(t *testing.T) {
	ctx := context.Background()
	f := newOrganizationFixture(t)
	if f.org.CurrentUserRole != OrgRoleOwner {
		t.Fatal("creator is not owner")
	}
	for _, account := range []struct {
		user *ent.User
		role string
	}{{f.owner, OrgRoleOwner}, {f.admin, OrgRoleAdmin}, {f.member, OrgRoleMember}} {
		list, err := f.svc.ListOrganizationsForUser(ctx, account.user.ID)
		if err != nil || len(list) != 1 || list[0].CurrentUserRole != account.role {
			t.Fatalf("list=%+v err=%v", list, err)
		}
		detail, err := f.svc.GetOrganization(ctx, account.user.ID, f.org.ID)
		if err != nil || detail.CurrentUserRole != account.role {
			t.Fatalf("detail=%+v err=%v", detail, err)
		}
	}
	updated, err := f.svc.UpdateOrganization(ctx, f.admin.ID, f.org.ID, UpdateOrganizationInput{Name: " Renamed ", Slug: " RENAMED "})
	if err != nil || updated.Name != "Renamed" || updated.Slug != "renamed" || updated.DisplayName != "Same display" || updated.Description != "Description" || updated.CurrentUserRole != OrgRoleAdmin {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	updated, err = f.svc.UpdateOrganization(ctx, f.owner.ID, f.org.ID, UpdateOrganizationInput{Name: "Renamed", Slug: "renamed", DisplayName: organizationRole(""), Description: organizationRole("")})
	if err != nil || updated.DisplayName != "" || updated.Description != "" {
		t.Fatalf("clear=%+v err=%v", updated, err)
	}
	if _, err := f.svc.UpdateOrganization(ctx, f.member.ID, f.org.ID, UpdateOrganizationInput{Name: "Other", Slug: "other"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member update: %v", err)
	}
	for _, input := range []UpdateOrganizationInput{{Slug: "valid"}, {Name: "valid"}, {Name: " ", Slug: "valid"}, {Name: "valid", Slug: " "}} {
		if _, err := f.svc.UpdateOrganization(ctx, f.owner.ID, f.org.ID, input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid update: %v", err)
		}
	}
	if _, err := f.svc.GetOrganization(ctx, f.other.ID, f.org.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("nonmember: %v", err)
	}
	if _, err := f.svc.GetOrganization(ctx, f.owner.ID, f.org.ID+100); !errors.Is(err, ErrOrganizationNotFound) {
		t.Fatalf("missing org: %v", err)
	}
	if _, err := f.svc.RequireMembership(ctx, f.owner.ID, f.org.ID, "unknown"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown required role: %v", err)
	}
}

func TestOrganizationUniqueConflictsAreAtomic(t *testing.T) {
	ctx := context.Background()
	f := newOrganizationFixture(t)
	second, err := f.svc.CreateOrganization(ctx, f.owner.ID, CreateOrganizationInput{Name: "Second", Slug: "second", DisplayName: "Same display"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, slug string
		want       error
	}{{"Team", "unique", ErrOrganizationNameExists}, {"Unique", " TEAM ", ErrOrganizationSlugExists}} {
		beforeOrgs := f.client.Organization.Query().CountX(ctx)
		beforeMembers := f.client.OrgMembership.Query().CountX(ctx)
		beforeAudits := f.client.ActivityLog.Query().CountX(ctx)
		if _, err := f.svc.CreateOrganization(ctx, f.owner.ID, CreateOrganizationInput{Name: tc.name, Slug: tc.slug}); !errors.Is(err, tc.want) {
			t.Fatalf("create conflict=%v want=%v", err, tc.want)
		}
		if f.client.Organization.Query().CountX(ctx) != beforeOrgs || f.client.OrgMembership.Query().CountX(ctx) != beforeMembers || f.client.ActivityLog.Query().CountX(ctx) != beforeAudits {
			t.Fatal("conflicting creation left data")
		}
		before := f.client.Organization.GetX(ctx, second.ID)
		if _, err := f.svc.UpdateOrganization(ctx, f.owner.ID, second.ID, UpdateOrganizationInput{Name: tc.name, Slug: tc.slug, Description: organizationRole("Must not persist")}); !errors.Is(err, tc.want) {
			t.Fatalf("update conflict=%v want=%v", err, tc.want)
		}
		after := f.client.Organization.GetX(ctx, second.ID)
		if before.Name != after.Name || before.Slug != after.Slug || before.Description != after.Description || !before.UpdatedAt.Equal(after.UpdatedAt) || f.client.ActivityLog.Query().CountX(ctx) != beforeAudits {
			t.Fatal("conflicting update changed data")
		}
	}
	if _, err := f.svc.UpdateOrganization(ctx, f.owner.ID, second.ID, UpdateOrganizationInput{Name: second.Name, Slug: second.Slug}); err != nil {
		t.Fatal(err)
	}
	before := f.client.Organization.Query().CountX(ctx)
	if _, err := f.svc.CreateOrganization(ctx, f.other.ID+100, CreateOrganizationInput{Name: "Orphan", Slug: "orphan"}); err == nil {
		t.Fatal("invalid creator accepted")
	}
	if f.client.Organization.Query().CountX(ctx) != before {
		t.Fatal("creation without owner was not rolled back")
	}
}

func TestOrganizationAddMemberRoleMatrix(t *testing.T) {
	for _, actorRole := range []string{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember} {
		for _, targetRole := range []string{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember} {
			t.Run(actorRole+"_adds_"+targetRole, func(t *testing.T) {
				f := newOrganizationFixture(t)
				actor := map[string]*ent.User{OrgRoleOwner: f.owner, OrgRoleAdmin: f.admin, OrgRoleMember: f.member}[actorRole]
				member, err := f.svc.AddMember(context.Background(), actor.ID, f.org.ID, AddOrgMemberInput{Username: f.other.Username, Role: organizationRole(targetRole)})
				allowed := actorRole == OrgRoleOwner || actorRole == OrgRoleAdmin && targetRole == OrgRoleMember
				if !allowed {
					if !errors.Is(err, ErrForbidden) {
						t.Fatalf("forbidden add=%v", err)
					}
					return
				}
				if err != nil || member.Role != targetRole || member.Edges.User.ID != f.other.ID {
					t.Fatalf("member=%+v err=%v", member, err)
				}
			})
		}
	}
}

func TestOrganizationMemberInputAndIdempotency(t *testing.T) {
	ctx := context.Background()
	f := newOrganizationFixture(t)
	for _, role := range []string{"", " ", "administrator", "OWNER", " owner "} {
		if _, err := f.svc.AddMember(ctx, f.owner.ID, f.org.ID, AddOrgMemberInput{Username: f.other.Username, Role: organizationRole(role)}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid add role %q=%v", role, err)
		}
		if _, err := f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.member.ID, UpdateOrgMemberRoleInput{Role: role}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid update role %q=%v", role, err)
		}
	}
	for _, username := range []string{"", " ", "does-not-exist", "oth"} {
		if _, err := f.svc.AddMember(ctx, f.owner.ID, f.org.ID, AddOrgMemberInput{Username: username}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("username %q=%v", username, err)
		}
	}
	member, err := f.svc.AddMember(ctx, f.admin.ID, f.org.ID, AddOrgMemberInput{Username: " OTHER "})
	if err != nil || member.Role != OrgRoleMember {
		t.Fatalf("default role: %v", err)
	}
	count := f.client.ActivityLog.Query().CountX(ctx)
	if _, err := f.svc.AddMember(ctx, f.admin.ID, f.org.ID, AddOrgMemberInput{Username: f.other.Username}); !errors.Is(err, ErrMembershipExists) {
		t.Fatalf("duplicate member: %v", err)
	}
	if f.client.ActivityLog.Query().CountX(ctx) != count {
		t.Fatal("duplicate add recorded audit")
	}
	if _, err := f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.other.ID, UpdateOrgMemberRoleInput{Role: OrgRoleMember}); err != nil {
		t.Fatal(err)
	}
	if f.client.ActivityLog.Query().CountX(ctx) != count {
		t.Fatal("identical role produced a change audit")
	}
	for _, actor := range []*ent.User{f.admin, f.member} {
		if _, err := f.svc.UpdateMemberRole(ctx, actor.ID, f.org.ID, f.other.ID, UpdateOrgMemberRoleInput{Role: OrgRoleAdmin}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("role escalation=%v", err)
		}
	}
	if _, err := f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.other.ID+100, UpdateOrgMemberRoleInput{Role: OrgRoleAdmin}); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("missing member=%v", err)
	}
	if _, err := f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.owner.ID, UpdateOrgMemberRoleInput{Role: OrgRoleAdmin}); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("last owner downgrade=%v", err)
	}
	if _, err := f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.owner.ID, UpdateOrgMemberRoleInput{Role: OrgRoleOwner}); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizationRemoveAndLeaveMatrix(t *testing.T) {
	for _, actorRole := range []string{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember} {
		for _, targetRole := range []string{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember} {
			t.Run(actorRole+"_removes_"+targetRole, func(t *testing.T) {
				f := newOrganizationFixture(t)
				actor := map[string]*ent.User{OrgRoleOwner: f.owner, OrgRoleAdmin: f.admin, OrgRoleMember: f.member}[actorRole]
				_, err := f.svc.AddMember(context.Background(), f.owner.ID, f.org.ID, AddOrgMemberInput{Username: f.other.Username, Role: organizationRole(targetRole)})
				if err != nil {
					t.Fatal(err)
				}
				err = f.svc.RemoveMember(context.Background(), actor.ID, f.org.ID, f.other.ID)
				allowed := actorRole == OrgRoleOwner || actorRole == OrgRoleAdmin && targetRole == OrgRoleMember
				if !allowed {
					if !errors.Is(err, ErrForbidden) {
						t.Fatalf("remove=%v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := f.svc.RemoveMember(context.Background(), actor.ID, f.org.ID, f.other.ID); !errors.Is(err, ErrMembershipNotFound) {
					t.Fatalf("duplicate removal=%v", err)
				}
			})
		}
	}
	for _, role := range []string{OrgRoleOwner, OrgRoleAdmin, OrgRoleMember} {
		t.Run(role+"_leaves", func(t *testing.T) {
			ctx := context.Background()
			f := newOrganizationFixture(t)
			actor := map[string]*ent.User{OrgRoleOwner: f.owner, OrgRoleAdmin: f.admin, OrgRoleMember: f.member}[role]
			if role == OrgRoleOwner {
				if err := f.svc.RemoveMember(ctx, actor.ID, f.org.ID, actor.ID); !errors.Is(err, ErrOwnerRequired) {
					t.Fatalf("last owner leave=%v", err)
				}
				if _, err := f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.admin.ID, UpdateOrgMemberRoleInput{Role: OrgRoleOwner}); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.svc.RemoveMember(ctx, actor.ID, f.org.ID, actor.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.svc.RemoveMember(ctx, actor.ID, f.org.ID, actor.ID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("repeated leave=%v", err)
			}
			if _, err := f.client.User.Get(ctx, actor.ID); err != nil {
				t.Fatal("leave removed account")
			}
			if _, err := f.client.Organization.Get(ctx, f.org.ID); err != nil {
				t.Fatal("leave removed organization")
			}
			audit := f.client.ActivityLog.Query().Where(activitylog.ActionEQ("organization.member.leave")).OnlyX(ctx)
			if audit.ResourceID == nil || *audit.ResourceID != actor.ID || audit.Metadata["old_role"] != role {
				t.Fatalf("leave audit=%+v", audit)
			}
		})
	}
}

func TestOrganizationAuditFailureRollsBackMutation(t *testing.T) {
	for _, operation := range []string{"create", "update", "add", "role", "remove", "leave"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			f := newOrganizationFixture(t)
			orgBefore := f.client.Organization.GetX(ctx, f.org.ID)
			membersBefore := f.client.OrgMembership.Query().CountX(ctx)
			auditsBefore := f.client.ActivityLog.Query().CountX(ctx)
			injected := errors.New("audit storage unavailable")
			f.client.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, injected })
			})
			var err error
			switch operation {
			case "create":
				_, err = f.svc.CreateOrganization(ctx, f.owner.ID, CreateOrganizationInput{Name: "Failed", Slug: "failed"})
			case "update":
				_, err = f.svc.UpdateOrganization(ctx, f.owner.ID, f.org.ID, UpdateOrganizationInput{Name: "Failed", Slug: "failed"})
			case "add":
				_, err = f.svc.AddMember(ctx, f.owner.ID, f.org.ID, AddOrgMemberInput{Username: f.other.Username})
			case "role":
				_, err = f.svc.UpdateMemberRole(ctx, f.owner.ID, f.org.ID, f.member.ID, UpdateOrgMemberRoleInput{Role: OrgRoleOwner})
			case "remove":
				err = f.svc.RemoveMember(ctx, f.owner.ID, f.org.ID, f.member.ID)
			case "leave":
				err = f.svc.RemoveMember(ctx, f.member.ID, f.org.ID, f.member.ID)
			}
			if !errors.Is(err, injected) {
				t.Fatalf("operation=%v", err)
			}
			orgAfter := f.client.Organization.GetX(ctx, f.org.ID)
			if orgAfter.Name != orgBefore.Name || orgAfter.Slug != orgBefore.Slug || !orgAfter.UpdatedAt.Equal(orgBefore.UpdatedAt) || f.client.Organization.Query().CountX(ctx) != 1 || f.client.OrgMembership.Query().CountX(ctx) != membersBefore || f.client.ActivityLog.Query().CountX(ctx) != auditsBefore {
				t.Fatal("audit failure left committed mutation")
			}
			member := f.client.OrgMembership.Query().Where(orgmembership.HasUserWith(user.IDEQ(f.member.ID))).OnlyX(ctx)
			if member.Role != OrgRoleMember {
				t.Fatal("audit failure committed role")
			}
		})
	}
}

func TestOrganizationUnknownStoredRoleCannotAuthorize(t *testing.T) {
	ctx := context.Background()
	f := newOrganizationFixture(t)
	f.client.OrgMembership.Update().Where(orgmembership.HasUserWith(user.IDEQ(f.member.ID))).SetRole("future-role").ExecX(ctx)
	if _, err := f.svc.RequireMembership(ctx, f.member.ID, f.org.ID, OrgRoleMember); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unknown role=%v", err)
	}
	list, err := f.svc.ListOrganizationsForUser(ctx, f.member.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("unknown role listed: %+v %v", list, err)
	}
}

func organizationOwnerCount(ctx context.Context, client *ent.Client, orgID int) (int, error) {
	return client.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(orgID)), orgmembership.RoleEQ(OrgRoleOwner)).Count(ctx)
}

func organizationTestUser(t *testing.T, client *ent.Client, prefix, suffix string) *ent.User {
	t.Helper()
	return createTestUser(t, client, fmt.Sprintf("%s-%s", prefix, suffix))
}
