package worker

import (
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/sysmem"
)

// PipelineRuntime translates validated deployment units into the admission
// budget and the process-wide RSS gate used by JobRunner.
func PipelineRuntime(cfg config.PipelineConfig, logger *slog.Logger) (PipelineConfig, *sysmem.Gate) {
	if logger == nil {
		logger = slog.Default()
	}
	limits := PipelineConfig{MaxInflightWeight: int64(cfg.MaxInflightWeightMB) * 1024 * 1024, MaxInflightResources: cfg.MaxInflightResources}
	if cfg.RssLimitMB == 0 {
		return limits, nil
	}
	gate := sysmem.NewGate(uint64(cfg.RssLimitMB)*1024*1024, sysmem.ReadRSS, logger)
	logger.Info("rss fuse enabled", "limit_mb", cfg.RssLimitMB,
		"high_watermark_mb", float64(cfg.RssLimitMB)*0.85,
		"low_watermark_mb", float64(cfg.RssLimitMB)*0.70)
	return limits, gate
}
