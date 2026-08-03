package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"math"
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
	unresolved     []int        // 需要下一池处理
	retry          *batchJob    // 池内 in-flight 退避重试
	callbackResult *BatchResult // 可选，供 BatchHandler 回调使用
	failedSegments []int        // 终态扫描失败的段索引（如 semantic_qa）；不计入 Unresolved
}

// RunRoundResult 是 RunRound 的返回结果。
type RunRoundResult struct {
	// Unresolved 是所有批次处理后仍未解决的索引。
	Unresolved []int
	// FailedSegments 是终态扫描失败的段索引（如 semantic_qa）。
	FailedSegments []int
	// FailedBatches 是终态失败的批次数。
	FailedBatches int
}

// poolResult 是单个池的执行结果。
type poolResult struct {
	unresolved     []int
	failedSegments []int
	failedBatches  int
}

// RunRound 是通用的并发批次执行引擎。
// 启用 Shrink 时在内部跑多池：池内全并发，池间严格串行；失败段显式传入下一池重切。
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
	shrinkEnabled := round.Shrink > 0 && round.Shrink < 1 && !math.IsNaN(round.Shrink) && !math.IsInf(round.Shrink, 0)
	maxPools := 1
	if shrinkEnabled {
		maxPools = round.Retry.MaxAttempts + 1
		if maxPools < 1 {
			maxPools = 1
		}
	}
	totalAttempts := round.Retry.MaxAttempts + 1
	if totalAttempts < 1 {
		totalAttempts = 1
	}

	var pending []int // nil = 池 0 由 handler 扫描 doc
	var allFailedSegments []int
	var failedBatches int
	var finalUnresolved []int
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

		logger.Info("running shrink pool",
			"mode", handler.ModeName(),
			"pool", poolIndex,
			"batches", len(batches),
			"shrink", round.Shrink)

		pr, err := runPool(ctx, round, handler, doc, batches, totalAttempts, batchHandler, logger)
		if err != nil {
			return RunRoundResult{}, err
		}

		allFailedSegments = append(allFailedSegments, pr.failedSegments...)
		failedBatches += pr.failedBatches
		finalUnresolved = pr.unresolved

		if len(pr.unresolved) == 0 {
			break
		}
		if poolIndex+1 >= maxPools {
			break
		}
		pending = uniqueSortedInts(pr.unresolved)
		logger.Info("advancing to next shrink pool",
			"pool", poolIndex+1, "pending", len(pending), "shrink", round.Shrink)
	}

	if !stageStarted {
		return RunRoundResult{}, nil
	}

	if err := handler.Finalize(ctx, doc, finalUnresolved); err != nil {
		return RunRoundResult{}, err
	}

	return RunRoundResult{
		Unresolved:     finalUnresolved,
		FailedSegments: allFailedSegments,
		FailedBatches:  failedBatches,
	}, nil
}

// runPool 在单个池内并发执行所有批次；in-flight retry 复用席位，unresolved 释放席位。
func runPool(
	ctx context.Context,
	round Round,
	handler RoundHandler,
	doc *Document,
	batches [][]int,
	totalAttempts int,
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
			if len(result.failedSegments) > 0 {
				failedSegments = append(failedSegments, result.failedSegments...)
				failedBatches++
			}
			pendingMu.Unlock()

			if result.retry != nil && result.retry.attempt < totalAttempts {
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
		unresolved:     nextPending,
		failedSegments: failedSegments,
		failedBatches:  failedBatches,
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

// isRetryableByBackoff 判断错误是否为 429/503 限流错误。
// 供 adjudicate 等非 translate handler 使用；translate 改用 backend.IsRetryable。
func isRetryableByBackoff(err error) bool {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code == 429 || code == 503
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
