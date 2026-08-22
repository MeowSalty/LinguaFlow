package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

const (
	RoundModeTranslate  = "translate"
	RoundModeExtract    = "extract"
	RoundModeAdjudicate = "adjudicate"
	RoundModeSemanticQA = "semantic_qa"
	RoundModeRevise     = "revise"
	RoundModeCorrect    = "correct"
)

// PostprocessConfig 是 pipeline 级别的后处理配置。
type PostprocessConfig struct {
	TrimSpaces bool
}

// batchJob 描述一个待处理的批次任务。
type batchJob struct {
	idxs    []int
	attempt int // 池内已消耗的重试次数
}

// batchResult 描述一个批次的处理结果。
type batchResult struct {
	unresolved      []int        // 需要下一池处理
	retry           *batchJob    // 池内 in-flight 退避重试
	callbackResult  *BatchResult // 可选，供 BatchHandler 回调使用
	failedSegments  []int        // 终态扫描失败的段索引（如 semantic_qa）；不计入 Unresolved
	fatalUnresolved []int        // 致命错误（如 401/403）：直接落入 finalUnresolved，跳过剩余池
}

// RunRoundResult 是 RunRound 的返回结果。
type RunRoundResult struct {
	// Unresolved 是所有批次处理后仍未解决的索引（含 fatalUnresolved，供跨轮传播）。
	Unresolved []int
	// FailedSegments 是终态扫描失败的段索引（如 semantic_qa）。
	FailedSegments []int
	// FailedBatches 是终态失败的批次数。
	FailedBatches int
	// Resolved 是本轮成功处理的段索引（池 0 扫描集合 − finalUnresolved − failedSegments）。
	// 供调用方累加到 in-memory resolved 集合，驱动跨轮增量。
	Resolved []int
}

// poolResult 是单个池的执行结果。
type poolResult struct {
	unresolved      []int
	fatalUnresolved []int
	failedSegments  []int
	failedBatches   int
}

// RunRound 是通用的并发批次执行引擎。
// 池数恒按 Retry.MaxAttempts+1 跑（与 Shrink 解耦）：池内全并发，池间严格串行；
// 失败段显式传入下一池重切。Shrink 仅控制每池批次约束的缩比系数。
func RunRound(
	ctx context.Context,
	round Round,
	doc *Document,
	batchHandler func(ctx context.Context, result BatchResult) error,
	logger *slog.Logger,
	reporter progress.Reporter,
) (RunRoundResult, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if reporter == nil {
		reporter = progress.Nop{}
	}

	handler := round.Handler
	// 非翻译轮（extract/adjudicate/semantic_qa）不接缩批，engine_factory 构造 pipeline.Round
	// 时不填 Shrink（零值 0）。此处兜底为 1.0 使 SSE 文案显示"重切"而非"缩放 0.00"。
	// translate 轮的 Shrink 已由 NormalizeShrink 处理（合法域 (0,1]，校验拒绝 0），此处对其无影响。
	// 多池行为不受 Shrink 值影响（池数恒由 max_attempts+1 决定，非翻译 handler 的 BuildBatches 不读 Shrink）。
	if round.Shrink == 0 {
		round.Shrink = 1.0
	}
	// 池数恒由 max_attempts+1 决定（与 shrink 解耦）。
	// shrink 仅控制每池批次约束的缩比：1.0 = 多池同尺寸重切，(0,1) = 每池缩小。
	maxPools := round.Retry.MaxAttempts + 1
	if maxPools < 1 {
		maxPools = 1
	}
	// 在途重试预算内部封顶 min(max_attempts+1, maxTransientRetries)，不暴露给用户。
	transientBudget := transientBudgetFor(round.Retry)

	var pending []int // nil = 池 0 由 handler 扫描 doc
	var allFailedSegments []int
	var failedBatches int
	var finalUnresolved []int
	var allFatalUnresolved []int
	// 池 0 扫描集合（pending==nil 时由 batches 求和）；用于计算 Resolved。
	var scannedIdxs []int
	stageStarted := false

	for poolIndex := 0; poolIndex < maxPools; poolIndex++ {
		if ctx.Err() != nil {
			break
		}

		batches, err := handler.BuildBatches(ctx, doc, pending, poolIndex)
		if err != nil {
			return RunRoundResult{}, err
		}
		if len(batches) == 0 {
			// 仅池 0 空 batches 视为"无任务"；后续池空 batches 保留上一池的 unresolved
			if poolIndex == 0 {
				finalUnresolved = nil
			}
			break
		}

		if !stageStarted {
			totalSegments := 0
			for _, batch := range batches {
				totalSegments += len(batch)
			}
			reporter.StageStart(handler.ModeName(), totalSegments)
			stageStarted = true
			defer reporter.StageDone()
		}

		// 记录池 0 的扫描集合（用于最终计算 Resolved）。
		if poolIndex == 0 {
			seen := make(map[int]struct{}, len(batches))
			for _, batch := range batches {
				for _, idx := range batch {
					if _, ok := seen[idx]; !ok {
						scannedIdxs = append(scannedIdxs, idx)
						seen[idx] = struct{}{}
					}
				}
			}
		}

		// 进入本池的实际段数：池 0 由 batches 求和（pending 为 nil），后续池用 pending 长度。
		poolPending := len(pending)
		if poolPending == 0 {
			for _, batch := range batches {
				poolPending += len(batch)
			}
		}

		logger.Info("running shrink pool",
			"mode", handler.ModeName(),
			"pool", poolIndex,
			"batches", len(batches),
			"shrink", round.Shrink)

		// 池事件无条件发射（不再 gated on shrinkEnabled）。
		emitPoolEvent(reporter, progress.PoolEvent{
			Mode:       handler.ModeName(),
			PoolIndex:  poolIndex,
			MaxPools:   maxPools,
			Batches:    len(batches),
			Pending:    poolPending,
			ShrinkRate: round.Shrink,
			Phase:      "pool_start",
		})

		pr, err := runPool(ctx, round, handler, doc, batches, transientBudget, batchHandler, logger)
		if err != nil {
			return RunRoundResult{}, err
		}

		allFailedSegments = append(allFailedSegments, pr.failedSegments...)
		failedBatches += pr.failedBatches
		finalUnresolved = pr.unresolved
		// fatalUnresolved 段不进 pending（跳过剩余池），由 RunRound 直接并入 finalUnresolved 供跨轮传播。
		allFatalUnresolved = append(allFatalUnresolved, pr.fatalUnresolved...)

		if len(pr.unresolved) == 0 {
			break
		}
		if poolIndex+1 >= maxPools {
			break
		}
		pending = uniqueSortedInts(pr.unresolved)
		logger.Info("advancing to next shrink pool",
			"pool", poolIndex+1, "pending", len(pending), "shrink", round.Shrink)

		emitPoolEvent(reporter, progress.PoolEvent{
			Mode:       handler.ModeName(),
			PoolIndex:  poolIndex,
			MaxPools:   maxPools,
			Batches:    len(batches),
			Pending:    len(pending),
			ShrinkRate: round.Shrink,
			Phase:      "pool_advance",
		})
	}

	if !stageStarted {
		return RunRoundResult{}, nil
	}

	// fatalUnresolved 并入 finalUnresolved，参与跨轮传播（语义一致：均为"下一同模式轮 pending"）。
	if len(allFatalUnresolved) > 0 {
		finalUnresolved = append(finalUnresolved, allFatalUnresolved...)
		finalUnresolved = uniqueSortedInts(finalUnresolved)
	}

	if err := handler.Finalize(ctx, doc, finalUnresolved); err != nil {
		return RunRoundResult{}, err
	}

	// 计算 Resolved：池 0 扫描集合 − finalUnresolved − failedSegments。
	resolved := computeResolved(scannedIdxs, finalUnresolved, allFailedSegments)

	return RunRoundResult{
		Unresolved:     finalUnresolved,
		FailedSegments: allFailedSegments,
		FailedBatches:  failedBatches,
		Resolved:       resolved,
	}, nil
}

// maxTransientRetries 是在途重试预算的内部封顶。
const maxTransientRetries = 3

// transientBudgetFor 返回在途重试预算：min(max_attempts+1, maxTransientRetries)，下限 1。
// 与 RunRound/runPool 使用同一口径，供 handler 判断"最后一次有效尝试"以落终态。
func transientBudgetFor(retry backend.RetryPolicy) int {
	b := retry.MaxAttempts + 1
	if b < 1 {
		b = 1
	}
	if b > maxTransientRetries {
		b = maxTransientRetries
	}
	return b
}

// computeResolved 返回本轮成功处理的段索引集合。
// scannedIdxs 为池 0 的扫描集合；excluded 为未解决（含 fatal）与终态失败的并集。
func computeResolved(scannedIdxs, unresolved, failedSegments []int) []int {
	if len(scannedIdxs) == 0 {
		return nil
	}
	excluded := make(map[int]struct{}, len(unresolved)+len(failedSegments))
	for _, idx := range unresolved {
		excluded[idx] = struct{}{}
	}
	for _, idx := range failedSegments {
		excluded[idx] = struct{}{}
	}
	out := make([]int, 0, len(scannedIdxs))
	seen := make(map[int]struct{}, len(scannedIdxs))
	for _, idx := range scannedIdxs {
		if _, ok := excluded[idx]; ok {
			continue
		}
		if _, dup := seen[idx]; dup {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Ints(out)
	return out
}

// runPool 在单个池内并发执行所有批次；in-flight retry 复用席位，unresolved 释放席位。
// transientBudget 是在途重试预算（内部封顶后的值）。
func runPool(
	ctx context.Context,
	round Round,
	handler RoundHandler,
	doc *Document,
	batches [][]int,
	transientBudget int,
	batchHandler func(ctx context.Context, result BatchResult) error,
	logger *slog.Logger,
) (poolResult, error) {
	concurrency := round.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	jobs := make(chan batchJob, concurrency*2)
	results := make(chan batchResult, concurrency*2)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	var handlerErr atomic.Value
	var pendingMu sync.Mutex

	var nextPending []int
	var fatalUnresolved []int
	var failedSegments []int
	var failedBatches int

	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if runCtx.Err() != nil {
					pendingMu.Lock()
					nextPending = append(nextPending, job.idxs...)
					pendingMu.Unlock()
					continue
				}

				result := handler.ProcessBatch(runCtx, doc, job.idxs, job.attempt, logger)

				if batchHandler != nil && result.callbackResult != nil {
					if herr := batchHandler(runCtx, *result.callbackResult); herr != nil {
						logger.Error("batch handler error, terminating pool", "err", herr)
						handlerErr.Store(herr)
						runCancel()
						pendingMu.Lock()
						nextPending = append(nextPending, job.idxs...)
						pendingMu.Unlock()
						results <- batchResult{}
						continue
					}
				}

				results <- result
			}
		}()
	}

	done := make(chan struct{})
	var submitWg sync.WaitGroup
	submitWg.Add(1)
	go func() {
		defer submitWg.Done()
		for _, batch := range batches {
			select {
			case <-done:
				return
			case jobs <- batchJob{idxs: batch, attempt: 0}:
			}
		}
	}()

	active := len(batches)
	for active > 0 {
		select {
		case <-runCtx.Done():
			goto cleanup
		case result := <-results:
			pendingMu.Lock()
			nextPending = append(nextPending, result.unresolved...)
			fatalUnresolved = append(fatalUnresolved, result.fatalUnresolved...)
			if len(result.failedSegments) > 0 {
				failedSegments = append(failedSegments, result.failedSegments...)
				failedBatches++
			}
			pendingMu.Unlock()

			if result.retry != nil && result.retry.attempt < transientBudget {
				select {
				case <-runCtx.Done():
					pendingMu.Lock()
					nextPending = append(nextPending, result.retry.idxs...)
					pendingMu.Unlock()
					active--
				case jobs <- *result.retry:
					// in-flight 重试不递减 active（复用席位）
				}
			} else {
				if result.retry != nil {
					pendingMu.Lock()
					nextPending = append(nextPending, result.retry.idxs...)
					pendingMu.Unlock()
				}
				active--
			}
		}
	}

cleanup:
	close(done)
	submitWg.Wait()
	close(jobs)
	wg.Wait()
	if v := handlerErr.Load(); v != nil {
		return poolResult{}, v.(error)
	}

	return poolResult{
		unresolved:      nextPending,
		fatalUnresolved: fatalUnresolved,
		failedSegments:  failedSegments,
		failedBatches:   failedBatches,
	}, nil
}

// uniqueSortedInts 去重并排序。
func uniqueSortedInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	out := append([]int(nil), in...)
	sort.Ints(out)
	j := 0
	for i := 1; i < len(out); i++ {
		if out[i] != out[j] {
			j++
			out[j] = out[i]
		}
	}
	return out[:j+1]
}

// segmentIDStringsFromDoc 使用 doc 中稳定的 Segment.ID 标识段落。
func segmentIDStringsFromDoc(doc *Document, idxs []int) []string {
	out := make([]string, len(idxs))
	for i, idx := range idxs {
		out[i] = doc.Segments[idx].ID
	}
	return out
}

// httpStatusFromErr 从错误中提取 HTTP 状态码。
func httpStatusFromErr(err error) int {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		return hsErr.HTTPStatus()
	}
	return 0
}

// isFatalBackendError 判断是否为不可恢复的致命错误。
func isFatalBackendError(err error) bool {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code == 401 || code == 403
	}
	return false
}

// backoffDuration 计算退避等待时间。
func backoffDuration(attempt int, retry backend.RetryPolicy, lastErr error) time.Duration {
	wait := retry.Backoff << attempt
	if wait < minRateLimitBackoff {
		wait = minRateLimitBackoff
	}

	var raErr backend.RetryAfterError
	if errors.As(lastErr, &raErr) && raErr.HTTPStatus() == 429 {
		if ra := raErr.GetRetryAfter(); ra > wait {
			wait = ra
		}
	}

	if retry.Jitter {
		wait += time.Duration(rand.Int63n(int64(wait) + 1))
	}
	return wait
}

// minRateLimitBackoff 是 429 错误的最小退避时间。
const minRateLimitBackoff = 5 * time.Second

// emitPoolEvent 将池级事件分发给实现了 PoolObserver 的 Reporter（可选接口）。
func emitPoolEvent(reporter progress.Reporter, evt progress.PoolEvent) {
	obs, ok := reporter.(progress.PoolObserver)
	if !ok {
		return
	}
	obs.OnPoolEvent(evt)
}

// backendErrorBatchEvent 构造 backend 调用失败的 BatchEvent（公共 16 字段）。
// 供 extract/adjudicate/semantic_qa 的 fatal/retryable/interrupt/preserve 分支共用，
// 消除逐字重复的字面量，使 BatchEvent schema 变更只需改一处。
// triedBackends 为 nil 表示无尝试记录（extract 多后端循环不追踪）。
//
// 各结局（fatal 跳池+跨轮 / retryable 同批退避 / interrupt 外部中断 / preserve 终态保留）
// 在调用方的日志层通过不同 message 区分；BatchEvent.ErrorType 统一为 "backend_error"
// 以保持 progress.go 定义的枚举值（前端/测试按此匹配）。
func backendErrorBatchEvent(
	stage string,
	doc *Document,
	idxs []int,
	backendName string,
	triedBackends []string,
	callErr error,
	attempt int,
	roundIndex int,
	durationMs int64,
	sys, usr string,
	req backend.Request,
) progress.BatchEvent {
	return progress.BatchEvent{
		Stage:          stage,
		SegmentIDs:     segmentIDStringsFromDoc(doc, idxs),
		SegmentCount:   len(idxs),
		BackendName:    backendName,
		TriedBackends:  triedBackends,
		Status:         "failed",
		DurationMs:     durationMs,
		SentContent:    usr,
		ErrorType:      "backend_error",
		ErrorMessage:   callErr.Error(),
		HTTPStatus:     httpStatusFromErr(callErr),
		RoundIndex:     roundIndex,
		Attempt:        attempt,
		SystemPrompt:   sys,
		UserMessage:    usr,
		ResponseFormat: req.ResponseFormat,
		JSONSchema:     req.JSONSchema,
	}
}
