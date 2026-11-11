package worker

import (
	"context"
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
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
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
func NewRevisionPreviewRunner(logger *slog.Logger, client *ent.Client, limiterPool *backend.LimiterPool, httpClients ...telemetry.HTTPClientFactory) *RevisionPreviewRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &RevisionPreviewRunner{logger: logger, client: client, factory: NewEngineFactory(logger, limiterPool, httpClients...)}
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
) (out *service.RevisionPreviewResult, runnerErr error) {
	if snapshot == nil {
		return nil, errors.New("revision preview: snapshot is nil")
	}
	var inputErr error
	ctx, documentLease, inputErr := prepareWorkerInputs(ctx, r, allSegments, "", len(snapshot.Rounds), r.factory.maxResponseBytes)
	if inputErr != nil {
		return nil, inputErr
	}
	defer func() {
		if runnerErr != nil {
			service.ExecutionInputOwner(ctx).ReleaseExecutionInputs()
		}
	}()
	if r.factory.rootBudget != nil {
		defer func() { _ = r.factory.rootBudget.ForgetActor(service.ExecutionScope(ctx).ActorID) }()
	}
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
		meta, err := service.ExecutionSegmentMeta(ctx, row)
		if err != nil {
			return nil, err
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
	var doc *pipeline.Document
	_ = documentLease.RetainBacking(func() {
		if doc != nil {
			clear(doc.Segments)
			doc.Segments = nil
			doc.Vars = nil
			doc.ResolvedIndices = nil
		}
		doc = nil
	})
	if e := documentLease.RunBuild(ctx, func() error {
		doc = pipeline.BuildDocumentFromSegments(inputs, snapshot.SourceLang, snapshot.TargetLang, resourceRow.Format)
		return nil
	}); e != nil {
		return nil, e
	}
	clear(inputs)
	inputs = nil
	for i := range doc.Segments {
		doc.Segments[i].Translate = i == targetSegmentIdx
	}

	qaConfig.Glossary, inputErr = r.buildRevisionGlossary(ctx, projectRow, snapshot.GlossaryEnabled)
	if inputErr != nil {
		return nil, inputErr
	}
	qaConfig.Format = resourceRow.Format
	var qaEngine *qa.Engine
	if qaConfig.Enabled {
		qaEngine = qa.NewEngine(qaConfig, r.logger)
	}
	collectorCtx := ctx
	ctx, cancelCollector := context.WithCancel(ctx)
	defer cancelCollector()
	collector := preview.NewBoundedMemoryCollector(collectorCtx, preview.CollectorBudget{Reserve: documentLease.RetainPayload, Release: documentLease.ReleasePayload, RunBuild: documentLease.RunBuild, Cancel: cancelCollector})
	_ = documentLease.RetainBacking(func() { _ = collector.Close() })
	// 执行 snapshot 已裁剪为单 revise 轮，但顶层 Strategy（计划级物化）天然存活。
	// Repair 用服务层显式派生的 repairOpts 覆盖；Ruby 读顶层 Strategy，
	// 保证预览与真实作业行为一致。
	engineCfg := BuildEngineConfig(snapshot)
	engineCfg.Repair = repairOpts
	engineCfg.Ruby = engine.RubyConfig{
		Enabled:       snapshot.Strategy.Ruby.Enabled,
		PreserveKinds: snapshot.Strategy.Ruby.PreserveKinds,
	}
	eng, err := r.factory.BuildEngineWithConfig(ctx, snapshot, engineCfg, engine.RuntimeResources{
		Glossary: qaConfig.Glossary,
		TM:       r.buildRevisionTM(snapshot.TMEnabled),
	}, collector)
	if err != nil {
		return nil, fmt.Errorf("revision preview: build engine: %w", err)
	}
	defer func() { _ = eng.Close() }()
	defer func() {
		if e := collector.Err(); e != nil {
			runnerErr = e
		}
		if runnerErr != nil {
			out = &service.RevisionPreviewResult{Status: "failed", Metrics: CollectMeterMetrics(eng)}
		}
	}()

	result, execErr := eng.ExecuteRound(ctx, 0, doc,
		engine.WithSegmentFilter([]int{targetSegmentIdx}),
		// roundSnapshot.Revise 已在入口校验非 nil；写回时移除的 issue 集合与
		// 送进 prompt 的目标集合严格一致（服务层已收窄为请求 code 交集）。
		engine.WithBatchHandler(buildReviseBatchHandlerCommon(doc, qaEngine, targetSegmentIdx, roundSnapshot.Revise.IssueCodes)),
	)
	if isExecutionCapacity(execErr) {
		return nil, execErr
	}
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
			ExecutionInputOwnership: service.ExecutionInputOwner(ctx),
			Status:                  "failed",
			SegmentID:               allSegments[targetSegmentIdx].ID,
			SourceText:              allSegments[targetSegmentIdx].SourceText,
			TargetText:              doc.Segments[targetSegmentIdx].Target,
			QualityIssues:           doc.Segments[targetSegmentIdx].Issues,
			Metrics:                 CollectMeterMetrics(eng),
			Collector:               collector,
			RoundSummary:            []service.RevisionRoundSummary{summary},
			Warnings:                []string{warning},
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
		ExecutionInputOwnership: service.ExecutionInputOwner(ctx),
		Status:                  status,
		SegmentID:               allSegments[targetSegmentIdx].ID,
		SourceText:              allSegments[targetSegmentIdx].SourceText,
		TargetText:              target.Target,
		QualityIssues:           target.Issues,
		Metrics:                 CollectMeterMetrics(eng),
		Collector:               collector,
		RoundSummary:            []service.RevisionRoundSummary{summary},
	}, nil
}

func (r *RevisionPreviewRunner) buildRevisionGlossary(ctx context.Context, projectRow *ent.Project, enabled bool) (glossary.Glossary, error) {
	if !enabled {
		return glossary.Nop{}, nil
	}
	g, _, err := service.LoadExecutionGlossary(ctx, r.client, projectRow, false)
	return g, err
}

func (r *RevisionPreviewRunner) buildRevisionTM(_ bool) tm.TranslationMemory {
	return preview.NoopTM{}
}
