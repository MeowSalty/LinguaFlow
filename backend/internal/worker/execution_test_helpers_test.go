package worker

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

type fixtureCredentials struct{}

func (fixtureCredentials) Resolve(context.Context, credential.Binding, string, string) (string, error) {
	return "fixture-secret", nil
}
func (fixtureCredentials) Check(context.Context, credential.Binding, int, string, string) error {
	return nil
}

func configureFakeFactory(factory *EngineFactory) {
	factory.SetCredentials(fixtureCredentials{}, fixtureCredentials{})
	factory.build = func(cfg backend.Config) (backend.Backend, error) {
		currentFake.name = cfg.Name
		return currentFake, nil
	}
}

// All tests enter the factory with the same complete contract as production.
// Defaults belong in this fixture resolver, never in the runtime factory.
func completeWorkerSnapshot(t *testing.T, snapshot *service.JobExecutionSnapshot) *service.JobExecutionSnapshot {
	t.Helper()
	snapshot.RubyTemplates = execution.RubyTemplates{JSON: prompt.RubyAlignmentJSONTemplate, Text: prompt.RubyAlignmentTextTemplate}
	for i := range snapshot.Rounds {
		r := &snapshot.Rounds[i]
		if r.Mode != "correct" {
			if r.Backend.Type == "" {
				r.Backend.Type = "openai"
			}
			if !r.Backend.Credential.Valid() {
				r.Backend.Credential = credential.Binding{ID: 1, Version: 1}
			}
			if r.Backend.Options == nil {
				r.Backend.Options = map[string]any{}
			}
			if _, ok := r.Backend.Options["model"]; !ok {
				r.Backend.Options["model"] = "fixture"
			}
		}
		switch r.Mode {
		case "translate":
			v := r.Translate
			if v.BatchSize == 0 {
				v.BatchSize = 10
			}
			if v.Concurrency == 0 {
				v.Concurrency = 1
			}
			if v.FallbackShrink == 0 {
				v.FallbackShrink = 1
			}
			if v.SegmentFilter == nil {
				v.SegmentFilter = &service.SegmentFilterSnapshot{StatusFilter: "pending_only"}
			}
		case "revise":
			v := r.Revise
			if v.TemplateContent == "" {
				v.TemplateContent = templates.EmbeddedReviseTemplate()
			}
			if v.BatchSize == 0 {
				v.BatchSize = 10
			}
			if v.Concurrency == 0 {
				v.Concurrency = 1
			}
		case "semantic_qa":
			v := r.SemanticQA
			if v.TemplateContent == "" {
				v.TemplateContent = templates.EmbeddedSemanticQATemplate()
			}
			if v.BatchSize == 0 {
				v.BatchSize = 10
			}
			if v.Concurrency == 0 {
				v.Concurrency = 1
			}
		case "adjudicate":
			v := r.Adjudicate
			if v.TemplateContent == "" {
				v.TemplateContent = templates.EmbeddedAdjudicationTemplate()
			}
			if v.BatchSize == 0 {
				v.BatchSize = 10
			}
			if v.Concurrency == 0 {
				v.Concurrency = 1
			}
		}
	}
	resolved, err := execution.Resolve(*snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
