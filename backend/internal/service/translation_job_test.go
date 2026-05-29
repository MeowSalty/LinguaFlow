package service

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestDefaultProjectTranslationConfigMergesProjectLangsAndDefaults(t *testing.T) {
	projectRow := &ent.Project{
		SourceLang: "en",
		TargetLang: "zh",
		DefaultTranslationConfig: map[string]any{
			"target_lang": "ja",
			"pipeline": map[string]any{
				"translate": map[string]any{
					"concurrency": 2,
				},
			},
		},
	}

	got := defaultProjectTranslationConfig(projectRow)
	if got["source_lang"] != "en" {
		t.Fatalf("source_lang = %v, want en", got["source_lang"])
	}
	if got["target_lang"] != "ja" {
		t.Fatalf("target_lang = %v, want ja", got["target_lang"])
	}

	pipeline, ok := got["pipeline"].(map[string]any)
	if !ok {
		t.Fatalf("pipeline has type %T, want map[string]any", got["pipeline"])
	}
	translate, ok := pipeline["translate"].(map[string]any)
	if !ok {
		t.Fatalf("pipeline.translate has type %T, want map[string]any", pipeline["translate"])
	}
	if translate["concurrency"] != 2 {
		t.Fatalf("pipeline.translate.concurrency = %v, want 2", translate["concurrency"])
	}
}

func TestTranslationConfigJobOverrideWinsOverProjectDefaults(t *testing.T) {
	projectConfig := defaultProjectTranslationConfig(&ent.Project{
		SourceLang: "en",
		TargetLang: "zh",
		DefaultTranslationConfig: map[string]any{
			"pipeline": map[string]any{
				"translate": map[string]any{
					"concurrency": 2,
					"batch_size":  8,
				},
			},
		},
	})

	merged := mergeConfigMaps(projectConfig, map[string]any{
		"source_lang": "fr",
		"pipeline": map[string]any{
			"translate": map[string]any{
				"batch_size": 16,
			},
		},
	})

	if merged["source_lang"] != "fr" {
		t.Fatalf("source_lang = %v, want fr", merged["source_lang"])
	}
	if merged["target_lang"] != "zh" {
		t.Fatalf("target_lang = %v, want zh", merged["target_lang"])
	}
	pipeline := merged["pipeline"].(map[string]any)
	translate := pipeline["translate"].(map[string]any)
	if translate["concurrency"] != 2 {
		t.Fatalf("pipeline.translate.concurrency = %v, want 2", translate["concurrency"])
	}
	if translate["batch_size"] != 16 {
		t.Fatalf("pipeline.translate.batch_size = %v, want 16", translate["batch_size"])
	}
}
