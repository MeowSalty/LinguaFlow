package service

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
)

func TestManualJobFreezesInlineTermExtractionPerRound(t *testing.T) {
	ctx := context.Background()
	fixture, client, userID, _ := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	projectID := seedProject(t, client, userID)
	client.Project.UpdateOneID(projectID).SetGlossaryEnabled(false).ExecX(ctx)
	resource := createTestResource(t, client, projectID, "inline-extraction.txt")
	createTestSegment(t, client, resource.ID, 0, "Nebula API", nil)
	extraction := execution.DefaultInlineTermExtraction()
	extraction.Enabled, extraction.MaxTermsPer1000Words = true, 9
	rounds := []schema.ExecutionRoundConfig{validTranslateRound(backendID), validTranslateRound(backendID)}
	rounds[0].Translate.InlineTermExtraction = &extraction
	plan, err := fixture.executionPlans.Create(ctx, userID, CreateExecutionPlanTemplateInput{
		Name: "per-round extraction", ProfileID: -1, Rounds: rounds,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := fixture.jobs.CreateManualJob(ctx, userID, projectID, CreateJobInput{
		ExecutionPlanID: plan.ID, ResourceIDs: []int{resource.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan.Rounds[0].Translate.InlineTermExtraction.Enabled = false
	plan.Rounds[1].Translate.InlineTermExtraction = &extraction
	client.ExecutionPlanTemplate.UpdateOneID(plan.ID).SetRounds(plan.Rounds).ExecX(ctx)
	snapshot, err := GetSnapshot(client.Job.GetX(ctx, created.ID))
	if err != nil {
		t.Fatal(err)
	}
	first := snapshot.Rounds[0].Translate.InlineTermExtraction
	if first == nil || !first.Enabled || first.MaxTermsPer1000Words != 9 || snapshot.Rounds[1].Translate.InlineTermExtraction != nil {
		t.Fatal("saved job inherited a later plan edit or lost per-round extraction settings")
	}
	if snapshot.GlossaryEnabled || client.Project.GetX(ctx, projectID).GlossaryEnabled {
		t.Fatal("round extraction overrode the project's glossary master switch")
	}
}

func TestQuickInlineTermExtractionUsesIsolatedGlossary(t *testing.T) {
	ctx := context.Background()
	fixture, client, userID, runner := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	projectID := seedProject(t, client, userID)
	client.Project.UpdateOneID(projectID).SetGlossaryEnabled(true).ExecX(ctx)
	seedGlossaryEntry(t, client, projectID, "API", "接口")
	planID := seedTranslatePlan(t, client, userID, backendID)
	plan := client.ExecutionPlanTemplate.GetX(ctx, planID)
	extraction := execution.DefaultInlineTermExtraction()
	extraction.Enabled = true
	plan.Rounds[0].Translate.InlineTermExtraction = &extraction
	client.ExecutionPlanTemplate.UpdateOneID(planID).SetRounds(plan.Rounds).ExecX(ctx)
	_, err := fixture.Translate(ctx, QuickTranslateInput{
		ActorUserID: userID, ProjectID: &projectID, ExecutionPlanID: planID, SourceText: "Nebula API",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !runner.captured.Snapshot.GlossaryEnabled || !runner.captured.Snapshot.Rounds[0].Translate.InlineTermExtraction.Enabled {
		t.Fatal("quick translation lost the round's extraction settings")
	}
	if _, ok := runner.captured.Glossary.(*preview.OverlayGlossary); !ok {
		t.Fatal("quick extraction should use an isolated glossary overlay")
	}
	if _, err := runner.captured.Glossary.Add(ctx, glossary.Entry{Source: "Nebula", Target: "星云"}); err != nil {
		t.Fatal(err)
	}
	entries, err := runner.captured.Glossary.Lookup(ctx, "Nebula API", "en", "zh")
	if err != nil || len(entries) != 2 {
		t.Fatalf("overlay did not combine existing and extracted terms: %+v, %v", entries, err)
	}
	if client.GlossaryEntry.Query().CountX(ctx) != 1 || !client.Project.GetX(ctx, projectID).GlossaryEnabled {
		t.Fatal("quick extraction persisted terms or changed project settings")
	}
}

func TestQuickInlineTermExtractionRespectsProjectMasterSwitch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		project     bool
		wantEnabled bool
	}{
		{name: "project_off", project: true},
		{name: "no_project", wantEnabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fixture, client, userID, runner := newQuickFixture(t)
			backendID := seedUserBackend(t, client, userID)
			planID := seedTranslatePlan(t, client, userID, backendID)
			plan := client.ExecutionPlanTemplate.GetX(ctx, planID)
			extraction := execution.DefaultInlineTermExtraction()
			extraction.Enabled = true
			plan.Rounds[0].Translate.InlineTermExtraction = &extraction
			plan.Rounds = append(plan.Rounds, schema.ExecutionRoundConfig{
				Mode: "extract", BackendID: backendID,
				Extract: &schema.ExtractRoundConfig{BootstrapTemplateID: -1, BatchSize: 10, Concurrency: 1},
			})
			client.ExecutionPlanTemplate.UpdateOneID(planID).SetRounds(plan.Rounds).ExecX(ctx)
			input := QuickTranslateInput{ActorUserID: userID, ExecutionPlanID: planID, SourceText: "Nebula API"}
			if tc.project {
				projectID := seedProject(t, client, userID)
				client.Project.UpdateOneID(projectID).SetGlossaryEnabled(false).ExecX(ctx)
				input.ProjectID = &projectID
				// Even explicitly supplied temporary entries cannot override the project switch.
				input.Glossary = []QuickGlossaryEntryInput{{Source: "API", Target: "接口"}}
			}
			if _, err := fixture.Translate(ctx, input); err != nil {
				t.Fatal(err)
			}
			if runner.captured.Snapshot.GlossaryEnabled != tc.wantEnabled || !runner.captured.Snapshot.Rounds[0].Translate.InlineTermExtraction.Enabled {
				t.Fatal("project policy was overridden or reusable round configuration was discarded")
			}
			_, isNop := runner.captured.Glossary.(glossary.Nop)
			if isNop == tc.wantEnabled {
				t.Fatal("runtime glossary does not match the effective glossary switch")
			}
		})
	}
}
