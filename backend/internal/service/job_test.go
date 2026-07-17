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

func TestDeriveJobStatus(t *testing.T) {
	tests := []struct {
		name                                                  string
		total, pending, running, completed, failed, cancelled int
		want                                                  string
	}{
		{
			name:      "zero total returns pending",
			total:     0,
			pending:   0,
			running:   0,
			completed: 0,
			failed:    0,
			cancelled: 0,
			want:      JobStatusPending,
		},
		{
			name:      "all pending returns pending",
			total:     5,
			pending:   5,
			running:   0,
			completed: 0,
			failed:    0,
			cancelled: 0,
			want:      JobStatusPending,
		},
		{
			name:      "any running returns running",
			total:     5,
			pending:   2,
			running:   2,
			completed: 1,
			failed:    0,
			cancelled: 0,
			want:      JobStatusRunning,
		},
		{
			name:      "all completed returns completed (not awaiting_review)",
			total:     3,
			pending:   0,
			running:   0,
			completed: 3,
			failed:    0,
			cancelled: 0,
			want:      JobStatusCompleted,
		},
		{
			name:      "all cancelled returns cancelled",
			total:     3,
			pending:   0,
			running:   0,
			completed: 0,
			failed:    0,
			cancelled: 3,
			want:      JobStatusCancelled,
		},
		{
			name:      "mixed completed and failed returns failed",
			total:     3,
			pending:   0,
			running:   0,
			completed: 2,
			failed:    1,
			cancelled: 0,
			want:      JobStatusFailed,
		},
		{
			name:      "mixed with partial completion returns running",
			total:     5,
			pending:   2,
			running:   0,
			completed: 2,
			failed:    1,
			cancelled: 0,
			want:      JobStatusRunning,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveJobStatus(tt.total, tt.pending, tt.running, tt.completed, tt.failed, tt.cancelled)
			if got != tt.want {
				t.Errorf("deriveJobStatus(%d,%d,%d,%d,%d,%d) = %q, want %q",
					tt.total, tt.pending, tt.running, tt.completed, tt.failed, tt.cancelled, got, tt.want)
			}
		})
	}
}
