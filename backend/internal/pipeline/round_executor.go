package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

const (
	RoundModeTranslate = "translate"
	RoundModeExtract   = "extract"
)

// PostprocessConfig 是 pipeline 级别的后处理配置。
type PostprocessConfig struct {
	TrimSpaces bool
}

// batchJob 描述一个待处理的批次任务。
type batchJob struct {
	idxs    []int
	attempt int // 已消耗的重试次数
}

// batchResult 描述一个批次的处理结果。
type batchResult struct {
	unresolved     []int        // 需要下一轮处理
	missing        []int        // 需要 round 级重新分批
	retry          *batchJob    // 需要重新入队（缩批或退避后重试）
	callbackResult *BatchResult // 可选，供 BatchHandler 回调使用
}

// RunRoundResult 是 RunRound 的返回结果。
type RunRoundResult struct {
	// Unresolved 是所有批次处理后仍未解决的索引。
	Unresolved []int
}

// RunRound 是通用的并发批次执行引擎。完全不知道段落、翻译等概念。
// handler 负责分批策略和批次处理，RunRound 只负责并发调度和重试。
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

	batches, err := handler.BuildBatches(ctx, doc)
	if err != nil {
		return RunRoundResult{}, err
	}
	if len(batches) == 0 {
		return RunRoundResult{}, nil
	}

	totalSegments := 0
	for _, batch := range batches {
		totalSegments += len(batch)
	}
	reporter.StageStart(handler.ModeName(), totalSegments)
	defer reporter.StageDone()

	totalAttempts := round.Retry.MaxAttempts + 1
	jobs := make(chan batchJob, round.Concurrency*2)
	results := make(chan batchResult, round.Concurrency*2)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	var handlerErr atomic.Value
	var pendingMu sync.Mutex

	var nextPending []int
	var missingSegs []int

	// 启动 worker pool
	var wg sync.WaitGroup
	for w := 0; w < round.Concurrency; w++ {
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

				// 调用 BatchHandler 回调
				if batchHandler != nil && result.callbackResult != nil {
					if herr := batchHandler(runCtx, *result.callbackResult); herr != nil {
						logger.Error("batch handler error, terminating round", "err", herr)
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

	// 提交批次任务
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

	// 收集结果
	active := len(batches)
	for active > 0 {
		select {
		case <-runCtx.Done():
			goto cleanup
		case result := <-results:
			pendingMu.Lock()
			nextPending = append(nextPending, result.unresolved...)
			pendingMu.Unlock()

			if result.retry != nil && result.retry.attempt < totalAttempts {
				select {
				case <-runCtx.Done():
					pendingMu.Lock()
					nextPending = append(nextPending, result.retry.idxs...)
					pendingMu.Unlock()
					missingSegs = append(missingSegs, result.missing...)
					active--
				case jobs <- *result.retry:
					missingSegs = append(missingSegs, result.missing...)
				}
			} else {
				if result.retry != nil {
					pendingMu.Lock()
					nextPending = append(nextPending, result.retry.idxs...)
					pendingMu.Unlock()
				}
				missingSegs = append(missingSegs, result.missing...)
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
		return RunRoundResult{}, v.(error)
	}

	// 缺失段重新分批（round 级重试）
	if len(missingSegs) > 0 && ctx.Err() == nil {
		sort.Ints(missingSegs)
		// 去重
		missingSegs = uniqueInts(missingSegs)
		logger.Info("retrying missing segments", "missing", len(missingSegs))

		retryBatches := [][]int{missingSegs}
		retryResult, retryErr := runMissingRetry(runCtx, handler, doc, retryBatches, totalAttempts, logger, batchHandler)
		if retryErr != nil {
			return RunRoundResult{}, retryErr
		}
		nextPending = append(nextPending, retryResult...)
	}

	// 调用 handler.Finalize
	if err := handler.Finalize(ctx, doc, nextPending); err != nil {
		return RunRoundResult{}, err
	}

	return RunRoundResult{Unresolved: nextPending}, nil
}

// runMissingRetry 对缺失段进行 round 级重试。
func runMissingRetry(
	ctx context.Context,
	handler RoundHandler,
	doc *Document,
	batches [][]int,
	totalAttempts int,
	logger *slog.Logger,
	batchHandler func(ctx context.Context, result BatchResult) error,
) ([]int, error) {
	jobs := make(chan batchJob, len(batches)*2)
	results := make(chan batchResult, len(batches)*2)

	var wg sync.WaitGroup
	for w := 0; w < min(4, len(batches)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					results <- batchResult{unresolved: job.idxs}
					continue
				}
				result := handler.ProcessBatch(ctx, doc, job.idxs, job.attempt, logger)
				if batchHandler != nil && result.callbackResult != nil {
					if herr := batchHandler(ctx, *result.callbackResult); herr != nil {
						logger.Error("batch handler error in missing retry", "err", herr)
						results <- batchResult{unresolved: job.idxs}
						continue
					}
				}
				results <- result
			}
		}()
	}

	go func() {
		for _, batch := range batches {
			jobs <- batchJob{idxs: batch, attempt: 0}
		}
	}()

	var nextPending []int
	active := len(batches)
	for active > 0 {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nextPending, ctx.Err()
		case result := <-results:
			nextPending = append(nextPending, result.unresolved...)
			if result.retry != nil && result.retry.attempt < totalAttempts {
				jobs <- *result.retry
			} else {
				if result.retry != nil {
					nextPending = append(nextPending, result.retry.idxs...)
				}
				active--
			}
		}
	}
	close(jobs)
	wg.Wait()
	return nextPending, nil
}

// uniqueInts 对已排序的 int 切片去重。
func uniqueInts(sorted []int) []int {
	if len(sorted) <= 1 {
		return sorted
	}
	j := 0
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[j] {
			j++
			sorted[j] = sorted[i]
		}
	}
	return sorted[:j+1]
}

// pendingSegmentIDStrings 将索引切片转为字符串切片。
func pendingSegmentIDStrings(pendingIdxs []int) []string {
	segIDs := make([]string, len(pendingIdxs))
	for i, idx := range pendingIdxs {
		segIDs[i] = strconv.Itoa(idx)
	}
	return segIDs
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

// shrinkTo 计算缩批后的大小。
func shrinkTo(idxs []int, shrink float64) int {
	if shrink <= 0 || shrink >= 1 || math.IsNaN(shrink) || math.IsInf(shrink, 0) {
		return 1
	}
	next := int(math.Floor(float64(len(idxs)) * shrink))
	if next >= len(idxs) {
		next = len(idxs) - 1
	}
	if next < 1 {
		return 1
	}
	return next
}
