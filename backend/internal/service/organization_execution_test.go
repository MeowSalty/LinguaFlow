package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

// Exercise a real organization workflow across membership, shared configuration,
// project jobs and synchronous translation, without contacting an AI provider.
func TestOrganizationExecutionWorkflowAndRevocation(t *testing.T) {
	quick, client, ownerID, _ := newQuickFixture(t)
	ctx := context.Background()
	users := NewUserService(client, nil)
	org, err := users.CreateOrganization(ctx, ownerID, CreateOrganizationInput{Name: "Team", Slug: "team"})
	if err != nil {
		t.Fatal(err)
	}
	admin := createTestUser(t, client, "team-admin")
	member := createTestUser(t, client, "team-member")
	adminRole := OrgRoleAdmin
	if _, err := users.AddMember(ctx, ownerID, org.ID, AddOrgMemberInput{Username: admin.Username, Role: &adminRole}); err != nil {
		t.Fatal(err)
	}
	if _, err := users.AddMember(ctx, ownerID, org.ID, AddOrgMemberInput{Username: member.Username}); err != nil {
		t.Fatal(err)
	}
	backend := client.Backend.Create().SetName("team-backend").SetScope(ScopeOrg).SetOwnerOrgID(org.ID).
		SetBackendType("openai").SetOptions(map[string]any{"api_key": "frozen-team-secret"}).SaveX(ctx)
	backend = bindExecutionTestBackend(t, client, backend)
	profile, err := quick.jobs.profiles.Create(ctx, admin.ID, CreateExecutionProfileInput{Name: "team-profile", OrgID: &org.ID})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := quick.jobs.translationPromptTemplates.Create(ctx, admin.ID, CreateTranslationPromptTemplateInput{
		Name: "team-prompt", OrgID: &org.ID, SystemPromptContent: "Translate the provided text.",
	})
	if err != nil {
		t.Fatal(err)
	}
	round := validTranslateRound(backend.ID)
	round.Translate.PromptTemplateID = prompt.ID
	plan, err := quick.executionPlans.Create(ctx, admin.ID, CreateExecutionPlanTemplateInput{
		Name: "team-plan", OrgID: &org.ID, ProfileID: profile.ID, Rounds: []schema.ExecutionRoundConfig{round},
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := quick.projects.CreateOrgProject(ctx, admin.ID, org.ID, CreateProjectInput{Name: "team-project"})
	if err != nil {
		t.Fatal(err)
	}
	resource := createTestResource(t, client, project.ID, "team.txt")
	createTestSegment(t, client, resource.ID, 0, "Hello", nil)
	input := CreateJobInput{ExecutionPlanID: plan.ID, ResourceIDs: []int{resource.ID}}
	if _, err := quick.jobs.CreateManualJob(ctx, member.ID, project.ID, input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member create job=%v", err)
	}
	job, err := quick.jobs.CreateManualJob(ctx, admin.ID, project.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := quick.jobs.GetJob(ctx, member.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	page, err := quick.jobs.ListAccessibleJobs(ctx, member.ID, AccessibleJobListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != job.ID {
		t.Fatalf("shared jobs=%+v err=%v", page, err)
	}
	if _, err := quick.jobs.CancelJob(ctx, member.ID, job.ID); err != nil {
		t.Fatalf("existing member control contract: %v", err)
	}
	// Project-bound quick translation retains the existing member permission.
	request := QuickTranslateInput{ActorUserID: member.ID, SourceText: "Hello", ExecutionPlanID: plan.ID, ProjectID: &project.ID}
	if _, err := quick.Translate(ctx, request); err != nil {
		t.Fatalf("member project quick translate: %v", err)
	}
	request.ProjectID = nil
	if _, err := quick.Translate(ctx, request); !errors.Is(err, ErrBackendNotFound) {
		t.Fatalf("member unscoped backend access=%v", err)
	}
	// The job's durable configuration does not depend on the creator remaining a member.
	if err := users.RemoveMember(ctx, ownerID, org.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := quick.jobs.GetJob(ctx, admin.ID, job.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("creator after removal=%v", err)
	}
	snapshot, err := quick.jobs.GetExecutionSnapshot(ctx, job.ID)
	if err != nil {
		t.Fatalf("worker snapshot lost=%+v err=%v", snapshot, err)
	}
	bound := snapshot.Rounds[0].Backend
	if _, ok := bound.Options["api_key"]; ok {
		t.Fatal("worker snapshot contains a plaintext credential")
	}
	secret, err := quick.jobs.backends.Credentials().Resolve(ctx, bound.Credential, bound.Type, bound.Options["base_url"].(string))
	if err != nil || secret != "frozen-team-secret" {
		t.Fatalf("worker credential resolution after creator leaves: %v", err)
	}
	if _, err := quick.jobs.LoadJobExecution(ctx, job.ID); err != nil {
		t.Fatalf("worker execution after creator leaves=%v", err)
	}
	if _, err := quick.jobs.GetJob(ctx, member.ID, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := users.RemoveMember(ctx, ownerID, org.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	request.ProjectID = &project.ID
	if _, err := quick.Translate(ctx, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member quick translate=%v", err)
	}
	page, err = quick.jobs.ListAccessibleJobs(ctx, member.ID, AccessibleJobListOptions{State: "all"})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("removed member jobs=%+v err=%v", page, err)
	}
}

func TestOrganizationExecutionRejectsPrivateSnapshotDependencies(t *testing.T) {
	quick, client, ownerID, _ := newQuickFixture(t)
	ctx := context.Background()
	users := NewUserService(client, nil)
	org, err := users.CreateOrganization(ctx, ownerID, CreateOrganizationInput{Name: "Snapshots", Slug: "snapshots"})
	if err != nil {
		t.Fatal(err)
	}
	project := client.Project.Create().SetName("org-project").SetOwnerOrgID(org.ID).SaveX(ctx)
	backend := client.Backend.Create().SetName("org-backend").SetScope(ScopeOrg).SetOwnerOrgID(org.ID).SetBackendType("openai").SaveX(ctx)
	backend = bindExecutionTestBackend(t, client, backend)
	privateBackend := seedUserBackend(t, client, ownerID)
	privateProfile := client.ExecutionProfile.Create().SetName("private-profile").SetScope(ScopeUser).SetOwnerUserID(ownerID).SetConfig(schema.DefaultProfileConfig()).SaveX(ctx)
	privatePrompt := client.TranslationPromptTemplate.Create().SetName("private-prompt").SetScope(ScopeUser).SetOwnerUserID(ownerID).SetSystemPromptContent("private text").SaveX(ctx)
	privateBootstrap := client.BootstrapPromptTemplate.Create().SetName("private-bootstrap").SetScope(ScopeUser).SetOwnerUserID(ownerID).SetContent("private text").SaveX(ctx)
	for _, tc := range []struct {
		name   string
		mutate func(*ent.ExecutionPlanTemplate)
	}{
		{"plan", func(p *ent.ExecutionPlanTemplate) { p.Scope = ScopeUser; p.OwnerOrgID = nil; p.OwnerUserID = &ownerID }},
		{"profile", func(p *ent.ExecutionPlanTemplate) { p.ProfileID = privateProfile.ID }},
		{"prompt", func(p *ent.ExecutionPlanTemplate) { p.Rounds[0].Translate.PromptTemplateID = privatePrompt.ID }},
		{"bootstrap", func(p *ent.ExecutionPlanTemplate) {
			p.Rounds = append(p.Rounds, schema.ExecutionRoundConfig{Mode: "extract", BackendID: backend.ID, Extract: &schema.ExtractRoundConfig{BootstrapTemplateID: privateBootstrap.ID, BatchSize: 10, Concurrency: 1}})
		}},
		{"backend", func(p *ent.ExecutionPlanTemplate) { p.Rounds[0].BackendID = privateBackend }},
		{"ruby_backend", func(p *ent.ExecutionPlanTemplate) {
			p.RubyRetry = schema.ExecutionPlanRubyRetryConfig{Enabled: true, BackendID: privateBackend}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantErr := ErrExecutionPlanConfigInvalid
			if tc.name == "plan" {
				wantErr = ErrForbidden
			}
			plan := &ent.ExecutionPlanTemplate{Name: tc.name, Scope: ScopeOrg, OwnerOrgID: &org.ID, ProfileID: -1, Rounds: []schema.ExecutionRoundConfig{validTranslateRound(backend.ID)}}
			tc.mutate(plan)
			// Seed a legacy malformed plan directly, bypassing the new creation guard.
			create := client.ExecutionPlanTemplate.Create().SetName(plan.Name).SetScope(plan.Scope).SetProfileID(plan.ProfileID).SetRounds(plan.Rounds).SetRubyRetry(plan.RubyRetry)
			if plan.OwnerOrgID != nil {
				create.SetOwnerOrgID(*plan.OwnerOrgID)
			}
			if plan.OwnerUserID != nil {
				create.SetOwnerUserID(*plan.OwnerUserID)
			}
			row := create.SaveX(ctx)
			_, release, err := quick.jobs.prepareExecutionSnapshot(ctx, ownerID, project, row.ID, "")
			if release != nil {
				t.Cleanup(release)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("job/preview should reject private dependency with scope error, got %v", err)
			}
			_, release, err = quick.jobs.prepareExecutionSnapshotForActor(ctx, ownerID, row.ID, "", "en", "zh", false, project)
			if release != nil {
				t.Cleanup(release)
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("quick translation should reject private dependency with scope error, got %v", err)
			}
		})
	}
}
