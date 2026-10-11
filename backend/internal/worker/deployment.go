package worker

import (
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/sysmem"
)

// PipelineRuntime translates validated deployment units into the admission
// budget and the process-wide RSS gate used by JobRunner.
func PipelineRuntime(cfg config.PipelineConfig, logger *slog.Logger) (PipelineConfig, *sysmem.Gate) {
	if logger == nil {
		logger = slog.Default()
	}
	limits := PipelineConfig{MaxInflightWeight: int64(cfg.MaxInflightWeightMB) * 1024 * 1024, MaxInflightResources: cfg.MaxInflightResources}
	limits.Candidates = pipeline.CandidateLimits{Segments: cfg.CandidateWindowSegments, Bytes: int64(cfg.CandidateWindowMB) << 20, ItemBytes: int64(cfg.MaxCandidateMB) << 20}
	limits.MaxResponseBytes = int64(cfg.MaxResponseMB) << 20
	if cfg.RssLimitMB == 0 {
		return limits, nil
	}
	gate := sysmem.NewGate(uint64(cfg.RssLimitMB)*1024*1024, sysmem.ReadRSS, logger)
	logger.Info("rss fuse enabled", "limit_mb", cfg.RssLimitMB,
		"high_watermark_mb", float64(cfg.RssLimitMB)*0.85,
		"low_watermark_mb", float64(cfg.RssLimitMB)*0.70)
	return limits, gate
}

// SetPipelineLimits injects deployment limits before the factory is shared.
// Zero-value limits retain the same defaults used by standalone CLI calls.
func (f *EngineFactory) SetPipelineLimits(limits PipelineConfig) {
	if limits.Candidates.Segments > 0 && limits.Candidates.Bytes > 0 && limits.Candidates.ItemBytes > 0 {
		f.candidateLimits = limits.Candidates
	}
	if limits.MaxResponseBytes != 0 {
		f.maxResponseBytes = limits.MaxResponseBytes
	}
}

func (r *PreviewRunner) SetPipelineLimits(limits PipelineConfig) {
	r.factory.SetPipelineLimits(limits)
}

func (r *RevisionPreviewRunner) SetPipelineLimits(limits PipelineConfig) {
	r.factory.SetPipelineLimits(limits)
}

func (r *QuickTranslateRunner) SetPipelineLimits(limits PipelineConfig) {
	r.factory.SetPipelineLimits(limits)
}
