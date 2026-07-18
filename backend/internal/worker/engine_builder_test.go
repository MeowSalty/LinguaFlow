package worker

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestBuildEngineConfigExtractOnlyKeepsGlossaryEnabled(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		GlossaryEnabled: true,
		Rounds: []service.JobRoundSnapshot{
			{
				Mode:    "extract",
				Extract: &service.JobExtractRoundSnapshot{},
			},
		},
	}

	cfg := buildEngineConfig(snapshot)
	if !cfg.Glossary.Enabled {
		t.Fatal("extract-only snapshot lost glossary enabled state")
	}
}

func TestBuildEngineConfigTranslateKeepsGlossarySettings(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		GlossaryEnabled: true,
		Rounds: []service.JobRoundSnapshot{
			{
				Mode: "translate",
				Translate: &service.JobTranslateRoundSnapshot{
					Strategy: service.StrategySnapshot{},
				},
			},
		},
	}

	cfg := buildEngineConfig(snapshot)
	if !cfg.Glossary.Enabled {
		t.Fatal("translate snapshot lost glossary enabled state")
	}
}
