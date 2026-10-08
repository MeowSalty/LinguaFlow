package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

type sharedReferenceSet struct{ profile, translation, bootstrap, backend int }

func seedSharedReferenceSet(t *testing.T, f sharedTestFixture, ownerID int, orgID *int) sharedReferenceSet {
	t.Helper()
	ctx := context.Background()
	scope := ScopeUser
	if orgID != nil {
		scope = ScopeOrg
	}
	profile := f.client.ExecutionProfile.Create().SetName("profile").SetScope(scope).SetConfig(schema.DefaultProfileConfig())
	translation := f.client.TranslationPromptTemplate.Create().SetName("translation").SetSystemPromptContent("secret").SetScope(scope)
	bootstrap := f.client.BootstrapPromptTemplate.Create().SetName("bootstrap").SetContent("secret").SetScope(scope)
	backend := f.client.Backend.Create().SetName("backend").SetBackendType("openai").SetScope(scope).
		SetOptions(map[string]any{"api_key": "secret", "model": "fake"})
	if orgID == nil {
		profile.SetOwnerUserID(ownerID)
		translation.SetOwnerUserID(ownerID)
		bootstrap.SetOwnerUserID(ownerID)
		backend.SetOwnerUserID(ownerID)
	} else {
		profile.SetOwnerOrgID(*orgID)
		translation.SetOwnerOrgID(*orgID)
		bootstrap.SetOwnerOrgID(*orgID)
		backend.SetOwnerOrgID(*orgID)
	}
	p, err := profile.Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := translation.Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bt, err := bootstrap.Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	be, err := backend.Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return sharedReferenceSet{p.ID, tr.ID, bt.ID, bindExecutionTestBackend(t, f.client, be).ID}
}

func sharedReferencePlan(orgID *int, refs sharedReferenceSet) CreateExecutionPlanTemplateInput {
	translate := validTranslateRound(refs.backend)
	translate.Translate.PromptTemplateID = refs.translation
	return CreateExecutionPlanTemplateInput{
		Name: "reference plan", OrgID: orgID, ProfileID: refs.profile,
		RubyRetry: schema.ExecutionPlanRubyRetryConfig{Enabled: true, BackendID: refs.backend, MaxAttempts: 1},
		Rounds: []schema.ExecutionRoundConfig{
			translate,
			{Mode: "extract", BackendID: refs.backend, Extract: &schema.ExtractRoundConfig{BootstrapTemplateID: refs.bootstrap, BatchSize: 10, Concurrency: 1}},
		},
	}
}

func TestSharedExecutionPlanReferenceBoundaries(t *testing.T) {
	ctx := context.Background()
	f := newSharedTestFixture(t)
	users := NewUserService(f.client, nil)
	profiles := NewExecutionProfileService(f.client, users)
	plans := NewExecutionPlanService(f.client, users, profiles)
	orgRefs := seedSharedReferenceSet(t, f, f.owner.ID, &f.org.ID)
	privateRefs := seedSharedReferenceSet(t, f, f.owner.ID, nil)
	crossRefs := seedSharedReferenceSet(t, f, f.owner.ID, &f.otherOrg.ID)
	foreignRefs := seedSharedReferenceSet(t, f, f.outsider.ID, nil)
	setRef := []struct {
		name string
		set  func(*CreateExecutionPlanTemplateInput, sharedReferenceSet)
	}{
		{"profile", func(in *CreateExecutionPlanTemplateInput, refs sharedReferenceSet) { in.ProfileID = refs.profile }},
		{"translation", func(in *CreateExecutionPlanTemplateInput, refs sharedReferenceSet) {
			in.Rounds[0].Translate.PromptTemplateID = refs.translation
		}},
		{"bootstrap", func(in *CreateExecutionPlanTemplateInput, refs sharedReferenceSet) {
			in.Rounds[1].Extract.BootstrapTemplateID = refs.bootstrap
		}},
		{"translate backend", func(in *CreateExecutionPlanTemplateInput, refs sharedReferenceSet) {
			in.Rounds[0].BackendID = refs.backend
		}},
		{"extract backend", func(in *CreateExecutionPlanTemplateInput, refs sharedReferenceSet) {
			in.Rounds[1].BackendID = refs.backend
		}},
		{"ruby retry backend", func(in *CreateExecutionPlanTemplateInput, refs sharedReferenceSet) {
			in.RubyRetry.BackendID = refs.backend
		}},
	}
	for _, source := range []struct {
		name string
		refs sharedReferenceSet
	}{
		{"private", privateRefs}, {"other organization", crossRefs}, {"other person", foreignRefs},
		{"missing", sharedReferenceSet{99999, 99999, 99999, 99999}},
	} {
		for _, field := range setRef {
			t.Run(source.name+"/"+field.name, func(t *testing.T) {
				input := sharedReferencePlan(&f.org.ID, orgRefs)
				field.set(&input, source.refs)
				if _, err := plans.Create(ctx, f.owner.ID, input); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
					t.Fatalf("create accepted invalid reference: %v", err)
				}
			})
		}
	}
	count, err := f.client.ExecutionPlanTemplate.Query().Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("failed creates persisted plans: %d %v", count, err)
	}
	count, err = f.client.ActivityLog.Query().Count(ctx)
	if err != nil || count != 0 {
		t.Fatalf("failed creates persisted audits: %d %v", count, err)
	}
	input := sharedReferencePlan(&f.org.ID, orgRefs)
	good, err := plans.Create(ctx, f.owner.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range setRef {
		t.Run("update/"+field.name, func(t *testing.T) {
			bad := sharedReferencePlan(&f.org.ID, orgRefs)
			field.set(&bad, privateRefs)
			_, err := plans.Update(ctx, f.owner.ID, good.ID, UpdateExecutionPlanTemplateInput{
				ProfileID: &bad.ProfileID, RubyRetry: &bad.RubyRetry, Rounds: bad.Rounds,
			})
			if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
				t.Fatalf("invalid update: %v", err)
			}
		})
	}
	// An unchanged reference must still be checked when editing a legacy invalid plan.
	if err := f.client.ExecutionPlanTemplate.UpdateOneID(good.ID).SetProfileID(privateRefs.profile).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	name := "rename only"
	if _, err := plans.Update(ctx, f.owner.ID, good.ID, UpdateExecutionPlanTemplateInput{Name: &name}); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
		t.Fatalf("unchanged private dependency bypass: %v", err)
	}
	if err := f.client.ExecutionPlanTemplate.UpdateOneID(good.ID).SetProfileID(orgRefs.profile).Exec(ctx); err != nil {
		t.Fatal(err)
	}

	// Builtins remain usable by organization plans; a zero retry backend reuses translate.
	builtin := sharedReferencePlan(&f.org.ID, orgRefs)
	builtin.ProfileID = -1
	builtin.Rounds[0].Translate.PromptTemplateID = -1
	builtin.Rounds[1].Extract.BootstrapTemplateID = -1
	builtin.RubyRetry.BackendID = 0
	if _, err := plans.Create(ctx, f.admin.ID, builtin); err != nil {
		t.Fatalf("system dependencies: %v", err)
	}
	if _, err := plans.Create(ctx, f.owner.ID, CreateExecutionPlanTemplateInput{
		Name: "local correction", OrgID: &f.org.ID, ProfileID: -1, Rounds: sharedCorrectRounds(),
	}); err != nil {
		t.Fatalf("correct without backend: %v", err)
	}

	// Personal plans retain accessible organization references, including another organization.
	for _, refs := range []sharedReferenceSet{orgRefs, privateRefs, crossRefs} {
		if _, err := plans.Create(ctx, f.owner.ID, sharedReferencePlan(nil, refs)); err != nil {
			t.Fatalf("personal accessible references: %v", err)
		}
	}
	if _, err := plans.Create(ctx, f.owner.ID, sharedReferencePlan(nil, foreignRefs)); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
		t.Fatalf("personal foreign references: %v", err)
	}
	// Referenced deletion returns a stable conflict without a referencing plan's name.
	rename := "PRIVATE_PLAN_NAME"
	if _, err := plans.Update(ctx, f.owner.ID, good.ID, UpdateExecutionPlanTemplateInput{Name: &rename}); err != nil {
		t.Fatal(err)
	}
	for _, deletion := range []struct {
		remove func() error
		want   error
	}{
		{func() error {
			return NewTranslationPromptTemplateService(f.client).Delete(ctx, f.admin.ID, orgRefs.translation)
		}, ErrTranslationPromptTemplateInUse},
		{func() error {
			return NewBootstrapPromptTemplateService(f.client).Delete(ctx, f.admin.ID, orgRefs.bootstrap)
		}, ErrBootstrapPromptTemplateInUse},
		{func() error { return profiles.Delete(ctx, f.admin.ID, orgRefs.profile) }, ErrExecutionProfileInUse},
	} {
		err := deletion.remove()
		if !errors.Is(err, deletion.want) || strings.Contains(err.Error(), rename) {
			t.Fatalf("unsafe reference conflict: %v", err)
		}
	}
}

func TestSharedOwnershipConflictsAreNotReadable(t *testing.T) {
	ctx := context.Background()
	f := newSharedTestFixture(t)
	row, err := f.client.TranslationPromptTemplate.Create().SetName("ambiguous").
		SetScope(ScopeOrg).SetOwnerUserID(f.owner.ID).SetOwnerOrgID(f.org.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewTranslationPromptTemplateService(f.client)
	if _, err := svc.GetByID(ctx, f.owner.ID, row.ID); !errors.Is(err, ErrTranslationPromptTemplateNotFound) {
		t.Fatalf("ambiguous read: %v", err)
	}
	rows, err := svc.ListByOrg(ctx, f.owner.ID, f.org.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("ambiguous organization list: %v %v", rows, err)
	}
}
