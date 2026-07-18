package service

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestDefaultProjectConfigMergesProjectLangs(t *testing.T) {
	projectRow := &ent.Project{
		SourceLang: "en",
		TargetLang: "zh",
	}

	got := defaultProjectConfig(projectRow)
	if got["source_lang"] != "en" {
		t.Fatalf("source_lang = %v, want en", got["source_lang"])
	}
	if got["target_lang"] != "zh" {
		t.Fatalf("target_lang = %v, want zh", got["target_lang"])
	}
}

func TestDefaultProjectConfigNilProject(t *testing.T) {
	got := defaultProjectConfig(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty map for nil project, got %v", got)
	}
}

func TestJobGlossaryEnabled(t *testing.T) {
	tests := []struct {
		name           string
		projectEnabled bool
		rounds         []JobRoundSnapshot
		want           bool
	}{
		{name: "project enabled", projectEnabled: true, want: true},
		{name: "extract round", rounds: []JobRoundSnapshot{{Mode: "extract"}}, want: true},
		{name: "translate only", rounds: []JobRoundSnapshot{{Mode: "translate"}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jobGlossaryEnabled(tt.projectEnabled, tt.rounds); got != tt.want {
				t.Fatalf("jobGlossaryEnabled() = %v, want %v", got, tt.want)
			}
		})
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
