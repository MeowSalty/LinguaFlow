package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

func TestExecutionPlanExtractBatchLimits(t *testing.T) {
	for _, tc := range []struct {
		name         string
		batch, words int
		want         string
	}{
		{"send_all", 0, 0, ""},
		{"segments", 10, 0, ""},
		{"words", 0, 100, ""},
		{"negative_segments", -1, 100, "batch_size"},
		{"negative_words", 10, -1, "max_words_per_batch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
				Mode: "extract", BackendID: 1,
				Extract: &schema.ExtractRoundConfig{BootstrapTemplateID: -1, BatchSize: tc.batch, MaxWordsPerBatch: tc.words, Concurrency: 1},
			}})
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrExecutionPlanConfigInvalid) || !strings.Contains(err.Error(), "rounds[0].extract."+tc.want) {
				t.Fatalf("error = %v, want config sentinel and field context", err)
			}
		})
	}
}

func TestCreateManualJobPreservesExtractSendAllSnapshot(t *testing.T) {
	ctx := context.Background()
	fixture, client, userID, _ := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	projectID := seedProject(t, client, userID)
	resource := createTestResource(t, client, projectID, "extract-send-all.txt")
	createTestSegment(t, client, resource.ID, 0, "Gemini API and OAuth2 authentication", nil)
	const originalTemplate = "saved custom extract prompt"
	prompt, err := fixture.jobs.bootstrapPromptTemplates.Create(ctx, userID, CreateBootstrapPromptTemplateInput{
		Name: "send-all prompt", Content: originalTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := fixture.executionPlans.Create(ctx, userID, CreateExecutionPlanTemplateInput{
		Name: "send-all plan", ProfileID: -1,
		Rounds: []schema.ExecutionRoundConfig{{
			Mode: "extract", BackendID: backendID,
			Extract: &schema.ExtractRoundConfig{BootstrapTemplateID: prompt.ID, BatchSize: 0, MaxWordsPerBatch: 0, Concurrency: 1},
		}},
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
	// 即使源资产随后发生变化，恢复时也必须使用保存下来的配置。
	client.BootstrapPromptTemplate.UpdateOneID(prompt.ID).SetContent("later prompt").ExecX(ctx)
	plan.Rounds[0].Extract.BatchSize = 20
	client.ExecutionPlanTemplate.UpdateOneID(plan.ID).SetRounds(plan.Rounds).ExecX(ctx)
	snapshot, err := GetSnapshot(client.Job.GetX(ctx, created.ID))
	if err != nil {
		t.Fatal(err)
	}
	extract := snapshot.Rounds[0].Extract
	if extract.BatchSize != 0 || extract.MaxWordsPerBatch != 0 || extract.TemplateContent != originalTemplate || !snapshot.GlossaryEnabled {
		t.Fatal("created job did not preserve its send-all configuration and template")
	}
	if !snapshot.Rounds[0].Backend.Credential.Valid() || client.CredentialJobReference.Query().CountX(ctx) != 1 {
		t.Fatal("created send-all job did not retain its historical credential")
	}
}
