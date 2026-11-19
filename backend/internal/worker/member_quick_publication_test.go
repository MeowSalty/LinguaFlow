package worker

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// These calls use the real service, engine, MemoryRoundStore and HTTP provider.
// The project database is an independent oracle for the nonpersist dictionary.
func TestQuickTranslateInlineGlossaryPublishesAtomically(t *testing.T) {
	for _, mode := range []string{"no_project", "project_overlay"} {
		t.Run(mode, func(t *testing.T) {
			f, http := newHandoffHTTPFixture(t, "translate", 1, 1, 1)
			http.open()
			http.transform = func(content any) any {
				result := content.(map[string]any)
				for id := range result["translations"].(map[string]string) {
					result["translations"].(map[string]string)[id] = "候选"
				}
				result["glossary"] = []map[string]string{{"source": "SDK", "target": "候选"}, {"source": "Nebula", "target": "星云"}}
				return result
			}
			users := service.NewUserService(f.client, nil)
			projects := service.NewProjectService(f.client, users)
			backends := f.initializeServices(t)
			profiles := service.NewExecutionProfileService(f.client, users)
			plans := service.NewExecutionPlanService(f.client, users, profiles)
			jobs := service.NewJobService(f.client, projects, plans, backends, service.NewTranslationPromptTemplateService(f.client), service.NewBootstrapPromptTemplateService(f.client), profiles, nil, nil)
			inline := execution.DefaultInlineTermExtraction()
			inline.Enabled, inline.MaxTermsPer1000Words = true, 1000
			plan := f.client.ExecutionPlanTemplate.Create().SetName("member quick").SetScope("user").SetOwnerUserID(f.ownerID).SetProfileID(-1).SetRubyRetry(schema.ExecutionPlanRubyRetryConfig{}).SetRounds([]schema.ExecutionRoundConfig{{Mode: "translate", BackendID: f.snapshot.Rounds[0].Backend.ID, Translate: &schema.TranslateRoundConfig{PromptTemplateID: -1, BatchSize: 1, MaxWordsPerBatch: 500, Concurrency: 1, FallbackShrink: 1, InlineTermExtraction: &inline}}}).SaveX(t.Context())
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			f.factory.rootBudget = root
			runner := &QuickTranslateRunner{logger: f.logger, client: f.client, factory: f.factory}
			quick := service.NewQuickTranslateService(f.logger, f.client, projects, jobs, backends, plans, service.NewAuditService(f.client, users, projects), runner, 1, time.Minute)
			input := service.QuickTranslateInput{ActorUserID: f.ownerID, SourceText: "SDK Nebula", SourceLang: "en", TargetLang: "zh", ExecutionPlanID: plan.ID}
			project := f.client.Job.QueryProject(f.client.Job.GetX(t.Context(), f.jobID)).OnlyX(t.Context())
			f.client.Project.UpdateOneID(project.ID).SetGlossaryEnabled(true).ExecX(t.Context())
			if mode == "project_overlay" {
				input.ProjectID = &project.ID
				f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("sdk").SetSource("SDK").SetTarget("规范").SaveX(t.Context())
			} else {
				input.Glossary = []service.QuickGlossaryEntryInput{{Source: "SDK", Target: "规范", Mandatory: true}}
			}
			before := f.client.GlossaryEntry.Query().CountX(t.Context())
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			out, err := quick.Translate(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			if out == nil || out.Status != "success" || out.TargetText != "规范" || len(out.RoundSummary) != 1 {
				t.Fatalf("real quick inline response did not publish correction: %+v", out)
			}
			if got := f.client.GlossaryEntry.Query().CountX(ctx); got != before {
				t.Fatalf("temporary dictionary was persisted: %d -> %d", before, got)
			}
			if mode == "project_overlay" && f.client.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(project.ID)).OnlyX(ctx).Target != "规范" {
				t.Fatal("project dictionary was rewritten")
			}
			http.mu.Lock()
			calls := http.calls
			http.mu.Unlock()
			if calls != 1 || f.client.WorkRequest.Query().CountX(ctx) != 0 {
				t.Fatalf("quick used durable job response or repeated HTTP: %d", calls)
			}
			out.ReleaseExecutionInputs()
			if got := root.Snapshot(f.ownerID); got.Instance != (resourcebudget.Vector{}) || got.ActiveIdentities != 0 {
				t.Fatalf("quick last reference retained owners: %+v", got)
			}
		})
	}
}

func TestPreviewInlineGlossaryUsesNonpersistPublication(t *testing.T) {
	f, http := newHandoffHTTPFixture(t, "translate", 1, 1, 1)
	http.open()
	http.transform = func(content any) any {
		result := content.(map[string]any)
		for id := range result["translations"].(map[string]string) {
			result["translations"].(map[string]string)[id] = "候选"
		}
		result["glossary"] = []map[string]string{{"source": "SDK", "target": "候选"}}
		return result
	}
	project := f.client.Job.QueryProject(f.client.Job.GetX(t.Context(), f.jobID)).OnlyX(t.Context())
	project.GlossaryEnabled = true
	f.client.Project.UpdateOneID(project.ID).SetGlossaryEnabled(true).ExecX(t.Context())
	f.client.GlossaryEntry.Create().SetProjectID(project.ID).SetSourceKey("sdk").SetSource("SDK").SetTarget("规范").SaveX(t.Context())
	inline := execution.DefaultInlineTermExtraction()
	inline.Enabled = true
	f.snapshot.GlossaryEnabled = true
	f.snapshot.Rounds[0].Translate.InlineTermExtraction = &inline
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	rows := f.client.Segment.Query().AllX(t.Context())
	resource := f.client.Resource.GetX(t.Context(), f.resourceIDs[0])
	rows[0].SourceText = "SDK"
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	f.factory.rootBudget = root
	runner := &PreviewRunner{logger: f.logger, client: f.client, factory: f.factory}
	ctx := service.WithExecutionActor(t.Context(), f.ownerID)
	defer service.ReleaseExecutionActor(ctx)
	ctx = service.WithExecutionInputBudget(ctx, root, service.ExecutionScope(ctx), service.DefaultExecutionInputBytes)
	out, err := runner.RunPreview(ctx, f.snapshot, project, resource, rows, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || out.Status != "success" || out.TargetText != "规范" {
		t.Fatalf("preview did not publish nonpersist correction: %+v", out)
	}
	if f.client.GlossaryEntry.Query().CountX(ctx) != 1 || f.client.GlossaryEntry.Query().OnlyX(ctx).Target != "规范" {
		t.Fatal("preview wrote dictionary")
	}
	http.mu.Lock()
	calls := http.calls
	http.mu.Unlock()
	if calls != 1 {
		t.Fatalf("preview HTTP=%d", calls)
	}
	out.ReleaseExecutionInputs()
	if got := root.Snapshot(f.ownerID); got.Instance != (resourcebudget.Vector{}) || got.ActiveIdentities != 0 {
		t.Fatalf("preview retained owners: %+v", got)
	}
}
