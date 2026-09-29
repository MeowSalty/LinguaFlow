package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

type sharedTestFixture struct {
	client                                   *ent.Client
	owner, admin, member, outsider, sysadmin *ent.User
	org, otherOrg                            *ent.Organization
	memberRow, adminRow                      *ent.OrgMembership
}

func newSharedTestFixture(t *testing.T) sharedTestFixture {
	t.Helper()
	ctx := context.Background()
	f := sharedTestFixture{client: testClient(t)}
	createUser := func(name string) *ent.User {
		row, err := f.client.User.Create().SetUsername(name).SetEmail(name + "@test.com").SetPasswordHash("hash").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	f.owner, f.admin, f.member = createUser("owner"), createUser("admin"), createUser("member")
	f.outsider, f.sysadmin = createUser("outsider"), createUser("sysadmin")
	var err error
	f.sysadmin, err = f.client.User.UpdateOne(f.sysadmin).SetRole("admin").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.org, err = f.client.Organization.Create().SetName("shared organization").SetSlug("shared").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.otherOrg, err = f.client.Organization.Create().SetName("other organization").SetSlug("other").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	add := func(org *ent.Organization, user *ent.User, role string) *ent.OrgMembership {
		row, err := f.client.OrgMembership.Create().SetOrganization(org).SetUser(user).SetRole(role).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	add(f.org, f.owner, OrgRoleOwner)
	add(f.otherOrg, f.owner, OrgRoleOwner)
	f.adminRow = add(f.org, f.admin, OrgRoleAdmin)
	f.memberRow = add(f.org, f.member, OrgRoleMember)
	return f
}

func sharedCorrectRounds() []schema.ExecutionRoundConfig {
	return []schema.ExecutionRoundConfig{{
		Mode: "correct", Correct: &schema.CorrectRoundConfig{Concurrency: 1, Rules: []schema.CorrectRuleConfig{{Name: correct.AllRuleNames()[0], Enabled: true}}},
	}}
}

type sharedCRUDTestAdapter struct {
	name                string
	notFound            error
	builtin             bool
	create              func(int, *int) (int, error)
	get, update, delete func(int, int) error
	listOrg             func(int, int) ([]int, error)
	listUser            func(int) ([]int, error)
}

func sharedTestAdapters(client *ent.Client) []sharedCRUDTestAdapter {
	ctx := context.Background()
	users := NewUserService(client, nil)
	translations := NewTranslationPromptTemplateService(client)
	bootstraps := NewBootstrapPromptTemplateService(client)
	prunes := NewPrunePromptTemplateService(client)
	profiles := NewExecutionProfileService(client, users)
	plans := NewExecutionPlanService(client, users, profiles)
	return []sharedCRUDTestAdapter{
		{
			name: "translation_prompt_template", notFound: ErrTranslationPromptTemplateNotFound, builtin: true,
			create: func(actorID int, orgID *int) (int, error) {
				row, err := translations.Create(ctx, actorID, CreateTranslationPromptTemplateInput{Name: "shared", OrgID: orgID, SystemPromptContent: "private prompt body"})
				if err != nil {
					return 0, err
				}
				return row.ID, nil
			},
			get: func(actorID, id int) error { _, err := translations.GetByID(ctx, actorID, id); return err },
			update: func(actorID, id int) error {
				name := "updated"
				_, err := translations.Update(ctx, actorID, id, UpdateTranslationPromptTemplateInput{Name: &name})
				return err
			},
			delete: func(actorID, id int) error { return translations.Delete(ctx, actorID, id) },
			listOrg: func(actorID, orgID int) ([]int, error) {
				rows, err := translations.ListByOrg(ctx, actorID, orgID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
			listUser: func(actorID int) ([]int, error) {
				rows, err := translations.ListByUser(ctx, actorID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "bootstrap_prompt_template", notFound: ErrBootstrapPromptTemplateNotFound, builtin: true,
			create: func(actorID int, orgID *int) (int, error) {
				row, err := bootstraps.Create(ctx, actorID, CreateBootstrapPromptTemplateInput{Name: "shared", OrgID: orgID, Content: "private prompt body"})
				if err != nil {
					return 0, err
				}
				return row.ID, nil
			},
			get: func(actorID, id int) error { _, err := bootstraps.GetByID(ctx, actorID, id); return err },
			update: func(actorID, id int) error {
				name := "updated"
				_, err := bootstraps.Update(ctx, actorID, id, UpdateBootstrapPromptTemplateInput{Name: &name})
				return err
			},
			delete: func(actorID, id int) error { return bootstraps.Delete(ctx, actorID, id) },
			listOrg: func(actorID, orgID int) ([]int, error) {
				rows, err := bootstraps.ListByOrg(ctx, actorID, orgID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
			listUser: func(actorID int) ([]int, error) {
				rows, err := bootstraps.ListByUser(ctx, actorID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "prune_prompt_template", notFound: ErrPrunePromptTemplateNotFound, builtin: true,
			create: func(actorID int, orgID *int) (int, error) {
				row, err := prunes.Create(ctx, actorID, CreatePrunePromptTemplateInput{Name: "shared", OrgID: orgID, Content: "private prompt body"})
				if err != nil {
					return 0, err
				}
				return row.ID, nil
			},
			get: func(actorID, id int) error { _, err := prunes.GetByID(ctx, actorID, id); return err },
			update: func(actorID, id int) error {
				name := "updated"
				_, err := prunes.Update(ctx, actorID, id, UpdatePrunePromptTemplateInput{Name: &name})
				return err
			},
			delete: func(actorID, id int) error { return prunes.Delete(ctx, actorID, id) },
			listOrg: func(actorID, orgID int) ([]int, error) {
				rows, err := prunes.ListByOrg(ctx, actorID, orgID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
			listUser: func(actorID int) ([]int, error) {
				rows, err := prunes.ListByUser(ctx, actorID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "execution_profile", notFound: ErrExecutionProfileNotFound, builtin: true,
			create: func(actorID int, orgID *int) (int, error) {
				row, err := profiles.Create(ctx, actorID, CreateExecutionProfileInput{Name: "shared", OrgID: orgID})
				if err != nil {
					return 0, err
				}
				return row.ID, nil
			},
			get: func(actorID, id int) error { _, err := profiles.GetByID(ctx, actorID, id); return err },
			update: func(actorID, id int) error {
				name := "updated"
				_, err := profiles.Update(ctx, actorID, id, UpdateExecutionProfileInput{Name: &name})
				return err
			},
			delete: func(actorID, id int) error { return profiles.Delete(ctx, actorID, id) },
			listOrg: func(actorID, orgID int) ([]int, error) {
				rows, err := profiles.ListByOrg(ctx, actorID, orgID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
			listUser: func(actorID int) ([]int, error) {
				rows, err := profiles.ListByUser(ctx, actorID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
		{
			name: "execution_plan", notFound: ErrExecutionPlanNotFound, builtin: false,
			create: func(actorID int, orgID *int) (int, error) {
				row, err := plans.Create(ctx, actorID, CreateExecutionPlanTemplateInput{Name: "shared", OrgID: orgID, ProfileID: -1, Rounds: sharedCorrectRounds()})
				if err != nil {
					return 0, err
				}
				return row.ID, nil
			},
			get: func(actorID, id int) error { _, err := plans.GetByID(ctx, actorID, id); return err },
			update: func(actorID, id int) error {
				name := "updated"
				_, err := plans.Update(ctx, actorID, id, UpdateExecutionPlanTemplateInput{Name: &name})
				return err
			},
			delete: func(actorID, id int) error { return plans.Delete(ctx, actorID, id) },
			listOrg: func(actorID, orgID int) ([]int, error) {
				rows, err := plans.ListByOrg(ctx, actorID, orgID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
			listUser: func(actorID int) ([]int, error) {
				rows, err := plans.ListByUser(ctx, actorID)
				ids := make([]int, 0, len(rows))
				for _, row := range rows {
					ids = append(ids, row.ID)
				}
				return ids, err
			},
		},
	}
}

func TestSharedObjectsAuthorizationAndLists(t *testing.T) {
	for i := range sharedTestAdapters(nil) {
		t.Run(sharedTestAdapters(nil)[i].name, func(t *testing.T) {
			ctx := context.Background()
			f := newSharedTestFixture(t)
			a := sharedTestAdapters(f.client)[i]
			id, err := a.create(f.owner.ID, &f.org.ID)
			if err != nil {
				t.Fatal(err)
			}
			adminID, err := a.create(f.admin.ID, &f.org.ID)
			if err != nil {
				t.Fatal(err)
			}
			personalID, err := a.create(f.owner.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			otherOrgID, err := a.create(f.owner.ID, &f.otherOrg.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, actor := range []*ent.User{f.owner, f.admin, f.member} {
				if err := a.get(actor.ID, id); err != nil {
					t.Errorf("member read: %v", err)
				}
			}
			for _, actor := range []*ent.User{f.outsider, f.sysadmin} {
				if err := a.get(actor.ID, id); !errors.Is(err, a.notFound) {
					t.Errorf("other read: %v", err)
				}
				if err := a.update(actor.ID, id); !errors.Is(err, a.notFound) {
					t.Errorf("other update: %v", err)
				}
				if err := a.delete(actor.ID, id); !errors.Is(err, a.notFound) {
					t.Errorf("other delete: %v", err)
				}
				if _, err := a.listOrg(actor.ID, f.org.ID); !errors.Is(err, ErrForbidden) {
					t.Errorf("other org list: %v", err)
				}
				if _, err := a.create(actor.ID, &f.org.ID); !errors.Is(err, ErrForbidden) {
					t.Errorf("other org create: %v", err)
				}
			}
			if _, err := a.create(f.member.ID, &f.org.ID); !errors.Is(err, ErrForbidden) {
				t.Errorf("member create: %v", err)
			}
			if err := a.update(f.member.ID, id); !errors.Is(err, ErrForbidden) {
				t.Errorf("member update: %v", err)
			}
			if err := a.delete(f.member.ID, id); !errors.Is(err, ErrForbidden) {
				t.Errorf("member delete: %v", err)
			}
			if err := a.get(f.admin.ID, personalID); !errors.Is(err, a.notFound) {
				t.Errorf("private read: %v", err)
			}
			if err := a.update(f.admin.ID, personalID); !errors.Is(err, a.notFound) {
				t.Errorf("private update: %v", err)
			}
			if err := a.delete(f.admin.ID, personalID); !errors.Is(err, a.notFound) {
				t.Errorf("private delete: %v", err)
			}
			if err := a.update(f.owner.ID, personalID); err != nil {
				t.Fatal(err)
			}
			ids, err := a.listOrg(f.member.ID, f.org.ID)
			if err != nil || !slices.Equal(ids, []int{id, adminID}) {
				t.Fatalf("org list %v: %v", ids, err)
			}
			ids, err = a.listUser(f.owner.ID)
			if err != nil || !slices.Contains(ids, personalID) {
				t.Fatalf("personal list %v: %v", ids, err)
			}
			if a.builtin {
				if !slices.Contains(ids, -1) || slices.Contains(ids, id) || slices.Contains(ids, otherOrgID) {
					t.Errorf("personal builtin contract: %v", ids)
				}
				if err := a.get(f.member.ID, -1); err != nil {
					t.Errorf("builtin read: %v", err)
				}
				if err := a.update(f.owner.ID, -1); !errors.Is(err, a.notFound) {
					t.Errorf("builtin update: %v", err)
				}
				if err := a.delete(f.owner.ID, -1); !errors.Is(err, a.notFound) {
					t.Errorf("builtin delete: %v", err)
				}
			} else if !slices.Contains(ids, id) || !slices.Contains(ids, otherOrgID) {
				t.Errorf("plan all membership contract: %v", ids)
			}
			missing, invalid := 99999, 0
			if _, err := a.listOrg(f.owner.ID, missing); !errors.Is(err, ErrOrganizationNotFound) {
				t.Errorf("missing org: %v", err)
			}
			if _, err := a.create(f.owner.ID, &invalid); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("invalid org: %v", err)
			}
			if err := a.update(f.admin.ID, id); err != nil {
				t.Fatal(err)
			}
			if err := a.delete(f.admin.ID, id); err != nil {
				t.Fatal(err)
			}
			if err := a.get(f.member.ID, id); !errors.Is(err, a.notFound) {
				t.Errorf("deleted read: %v", err)
			}
			if err := f.client.OrgMembership.DeleteOne(f.memberRow).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.get(f.member.ID, adminID); !errors.Is(err, a.notFound) {
				t.Errorf("revoked read: %v", err)
			}
			if _, err := a.listOrg(f.member.ID, f.org.ID); !errors.Is(err, ErrForbidden) {
				t.Errorf("revoked list: %v", err)
			}
			if err := f.client.OrgMembership.UpdateOne(f.adminRow).SetRole(OrgRoleMember).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.update(f.admin.ID, adminID); !errors.Is(err, ErrForbidden) {
				t.Errorf("downgraded write: %v", err)
			}
			logs, err := f.client.ActivityLog.Query().Where(activitylog.ResourceTypeEQ(a.name)).All(ctx)
			if err != nil || len(logs) != 7 {
				t.Fatalf("audit count %d, want 7: %v", len(logs), err)
			}
			raw, _ := json.Marshal(logs)
			if strings.Contains(string(raw), "private prompt body") {
				t.Fatal("audit leaked template content")
			}
		})
	}
}

func TestSharedObjectsAuditFailureRollsBack(t *testing.T) {
	for i := range sharedTestAdapters(nil) {
		t.Run(sharedTestAdapters(nil)[i].name, func(t *testing.T) {
			ctx := context.Background()
			f := newSharedTestFixture(t)
			a := sharedTestAdapters(f.client)[i]
			orgID, err := a.create(f.owner.ID, &f.org.ID)
			if err != nil {
				t.Fatal(err)
			}
			ownID, err := a.create(f.owner.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			auditErr := errors.New("audit failed")
			f.client.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, auditErr })
			})
			for _, targetOrg := range []*int{nil, &f.org.ID} {
				if _, err := a.create(f.owner.ID, targetOrg); !errors.Is(err, auditErr) {
					t.Fatalf("create error: %v", err)
				}
			}
			for _, id := range []int{orgID, ownID} {
				if err := a.update(f.owner.ID, id); !errors.Is(err, auditErr) {
					t.Fatalf("update error: %v", err)
				}
				if err := a.delete(f.owner.ID, id); !errors.Is(err, auditErr) {
					t.Fatalf("delete error: %v", err)
				}
				if err := a.get(f.owner.ID, id); err != nil {
					t.Fatalf("rolled back delete missing: %v", err)
				}
			}
			ids, err := a.listOrg(f.owner.ID, f.org.ID)
			if err != nil || !slices.Equal(ids, []int{orgID}) {
				t.Fatalf("create rollback: %v %v", ids, err)
			}
			count, err := f.client.ActivityLog.Query().Count(ctx)
			if err != nil || count != 2 {
				t.Fatalf("unexpected audit records: %d %v", count, err)
			}
		})
	}
}
