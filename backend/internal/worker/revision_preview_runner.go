package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// RevisionPreviewRunner executes one in-memory revise round for a target
// segment. It never persists segment or resource changes.
type RevisionPreviewRunner struct {
	logger  *slog.Logger
	client  *ent.Client
	factory *EngineFactory
}

// NewRevisionPreviewRunner creates a revision preview runner.
func NewRevisionPreviewRunner(logger *slog.Logger, client *ent.Client, limiterPool *backend.LimiterPool) *RevisionPreviewRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &RevisionPreviewRunner{logger: logger, client: client, factory: NewEngineFactory(logger, limiterPool)}
}

// RunRevisionPreview executes the synthetic single revise round against all
// resource segments, keeping only the requested segment eligible for revision.
func (r *RevisionPreviewRunner) RunRevisionPreview(
	ctx context.Context,
	snapshot *service.JobExecutionSnapshot,
	projectRow *ent.Project,
	resourceRow *ent.Resource,
	allSegments []*ent.Segment,
	targetSegmentIdx int,
	qaConfig qa.Config,
	repairOpts repair.Options,
	synthesized bool,
) (*service.RevisionPreviewResult, error) {
	if snapshot == nil || len(snapshot.Rounds) != 1 || snapshot.Rounds[0].Mode != "revise" {
		return nil, errors.New("revision preview: snapshot must contain one revise round")
	}
	if targetSegmentIdx < 0 || targetSegmentIdx >= len(allSegments) {
		return nil, fmt.Errorf("revision preview: target segment index %d out of range", targetSegmentIdx)
	}

	roundSnapshot := snapshot.Rounds[0]
	if roundSnapshot.Revise == nil {
		return nil, errors.New("revision preview: revise snapshot is nil")
	}
	inputs := make([]pipeline.SegmentInput, len(allSegments))
	for i, row := range allSegments {
		var meta map[string]any
		if row.Meta != nil {
			_ = json.Unmarshal([]byte(*row.Meta), &meta)
		}
		target := ""
		if row.TargetText != nil {
			target = *row.TargetText
		}
		inputs[i] = pipeline.SegmentInput{
			ID:         strconv.Itoa(row.SegmentIndex),
			SourceText: row.SourceText,
			Meta:       meta,
			TargetText: target,
			Issues:     row.QualityIssues,
			Status:     string(row.Status),
		}
	}
	doc := pipeline.BuildDocumentFromSegments(inputs, snapshot.SourceLang, snapshot.TargetLang, resourceRow.Format)
	for i := range doc.Segments {
		doc.Segments[i].Translate = i == targetSegmentIdx
	}

	qaConfig.Glossary = r.buildRevisionGlossary(ctx, projectRow, snapshot.GlossaryEnabled)
	qaConfig.Format = resourceRow.Format
	var qaEngine *qa.Engine
	if qaConfig.Enabled {
		qaEngine = qa.NewEngine(qaConfig, r.logger)
	}
	collector := preview.NewMemoryCollector()
	// 执行 snapshot 只含 revise 轮，BuildEngineConfig 找不到 translate 轮会把
	// Repair/Ruby 置零（= 不修复、不建 Restorer）；用服务层从完整 snapshot 派生的
	// repairOpts 与合成轮携带的借用 Ruby 策略覆盖，保持预览与真实作业行为一致。
	engineCfg := BuildEngineConfig(snapshot)
	engineCfg.Repair = repairOpts
	engineCfg.Ruby = engine.RubyConfig{
		Enabled:       roundSnapshot.Revise.RubyEnabled,
		PreserveKinds: roundSnapshot.Revise.RubyPreserveKinds,
	}
	eng, err := r.factory.BuildEngineWithConfig(ctx, snapshot, engineCfg, engine.RuntimeResources{
		Glossary: qaConfig.Glossary,
		TM:       r.buildRevisionTM(snapshot.TMEnabled),
	}, collector)
	if err != nil {
		return nil, fmt.Errorf("revision preview: build engine: %w", err)
	}
	defer func() { _ = eng.Close() }()

	result, execErr := eng.ExecuteRound(ctx, 0, doc,
		engine.WithSegmentFilter([]int{targetSegmentIdx}),
		// roundSnapshot.Revise 已在入口校验非 nil；写回时移除的 issue 集合与
		// 送进 prompt 的目标集合严格一致（服务层已收窄为请求 code 交集）。
		engine.WithBatchHandler(buildReviseBatchHandlerCommon(doc, qaEngine, targetSegmentIdx, roundSnapshot.Revise.IssueCodes)),
	)
	summary := service.RevisionRoundSummary{
		Index:       0,
		Mode:        "revise",
		Backend:     roundSnapshot.Backend.Name,
		Synthesized: synthesized,
	}
	if execErr != nil {
		summary.Status = "failed"
		warning := fmt.Sprintf("round 0 (revise) failed: %s", execErr)
		if errors.Is(execErr, context.Canceled) {
			warning = "round 0 (revise) cancelled"
		}
		return &service.RevisionPreviewResult{
			Status:        "failed",
			SegmentID:     allSegments[targetSegmentIdx].ID,
			SourceText:    allSegments[targetSegmentIdx].SourceText,
			TargetText:    doc.Segments[targetSegmentIdx].Target,
			QualityIssues: doc.Segments[targetSegmentIdx].Issues,
			Metrics:       CollectMeterMetrics(eng),
			Collector:     collector,
			RoundSummary:  []service.RevisionRoundSummary{summary},
			Warnings:      []string{warning},
		}, nil
	}
	if result.FailedBatchCount > 0 || result.UnresolvedCount > 0 || len(result.Unresolved) > 0 {
		summary.Status = "partial"
	} else {
		summary.Status = "success"
	}

	target := doc.Segments[targetSegmentIdx]
	status := "success"
	if target.Target == "" {
		status = "failed"
	} else if summary.Status != "success" {
		status = "partial"
	}
	return &service.RevisionPreviewResult{
		Status:        status,
		SegmentID:     allSegments[targetSegmentIdx].ID,
		SourceText:    allSegments[targetSegmentIdx].SourceText,
		TargetText:    target.Target,
		QualityIssues: target.Issues,
		Metrics:       CollectMeterMetrics(eng),
		Collector:     collector,
		RoundSummary:  []service.RevisionRoundSummary{summary},
	}, nil
}

func (r *RevisionPreviewRunner) buildRevisionGlossary(ctx context.Context, projectRow *ent.Project, enabled bool) glossary.Glossary {
	if !enabled {
		return glossary.Nop{}
	}
	base, err := service.NewDatabaseGlossary(ctx, r.client, projectRow)
	if err != nil {
		r.logger.Warn("revision preview: failed to load database glossary, using empty base", "err", err)
		return preview.NewOverlayGlossary(nil)
	}
	return preview.NewOverlayGlossary(base)
}

func (r *RevisionPreviewRunner) buildRevisionTM(_ bool) tm.TranslationMemory {
	return preview.NoopTM{}
}
