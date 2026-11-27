package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
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
	notBefore time.Time
	idxs      []int
	attempt   int // 池内已消耗的重试次数
}

// batchResult 描述一个批次的处理结果。
type batchResult struct {
	waitCapacity    *WorkDeferred
	waitJob         *batchJob
	candidates      []*Candidate
	extraction      *storedExtraction
	storedParseErr  error
	err             error
	unresolved      []int        // 需要下一池处理
	retry           *batchJob    // 池内 in-flight 退避重试
	callbackResult  *BatchResult // 可选，供 BatchHandler 回调使用
	failedSegments  []int        // 终态扫描失败的段索引（如 semantic_qa）；不计入 Unresolved
	fatalUnresolved []int        // 致命错误（如 401/403）：直接落入 finalUnresolved，跳过剩余池
	// deferred 标记 runPool worker 的让行点（暂停/槽位获取失败/获取后复查暂停、
	// batchHandler 回调失败、提交确认门判定副作用未确认）：idxs 已被塞入
	// nextPending（未解决），result 不携带任何失败字段。收集点据此跳过一切
	// 计数——否则空 result 会被误判为「全批成功」。
	deferred bool
	// idxs 是本批的段索引（由 runPool worker 在派发结果前回填 job.idxs，handler
	// 不感知）。收集点据此推导 resolved 子集，而非从 handler 返回值反推。
	idxs []int
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
	resolved        []int
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
) (result RunRoundResult, err error) {
	if round.Runtime == nil {
		return RunRoundResult{}, errors.New("round requires an execution runtime")
	}
	if _, ok := round.Handler.(stagedHandler); ok {
		if round.Store != nil && batchHandler != nil {
			return RunRoundResult{}, errors.New("staged round requires callbacks to be configured on the round store")
		}
		if round.Store == nil {
			store := NewMemoryRoundStore(applyMemoryTranslation(round.Handler, doc, batchHandler))
			defer func() { err = errors.Join(err, store.Close()) }()
			round.Store = store
		}
		return RunStagedRound(ctx, round, doc, round.Store, round.Runtime, logger, reporter)
	}
	if _, captures := round.Handler.(capturedRoundHandler); !captures {
		if _, local := round.Handler.(*CorrectHandler); !local {
			return RunRoundResult{}, fmt.Errorf("unsupported round handler %T", round.Handler)
		}
	}
	return runStoredRound(ctx, round, doc, batchHandler, logger, reporter)
}

func applyMemoryTranslation(handler RoundHandler, doc *Document, apply func(context.Context, BatchResult) error) func(context.Context, BatchResult) error {
	h, ok := handler.(*TranslateHandler)
	if !ok || h.TM == nil {
		return apply
	}
	if _, nop := h.TM.(tm.Nop); nop {
		return apply
	}
	return func(ctx context.Context, batch BatchResult) error {
		if apply != nil {
			if err := apply(ctx, batch); err != nil {
				return err
			}
		}
		for _, segment := range batch.Segments {
			if err := h.TM.Add(ctx, segment.SourceText, segment.TargetText, doc.SourceLang, doc.TargetLang); err != nil {
				h.logger().Warn("save translation memory entry", "segment", segment.ID, "err", err)
			}
		}
		return nil
	}
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

type batchProcessor func(context.Context, batchJob) batchResult

// runPool schedules bounded work and collects its confirmed outcomes.
func runPool(
	ctx context.Context,
	round Round,
	doc *Document,
	batches [][]int,
	transientBudget int,
	reporter progress.Reporter,
	process batchProcessor,
) (poolResult, error) {
	if len(batches) == 0 {
		return poolResult{}, nil
	}
	concurrency := round.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	concurrency = min(concurrency, len(batches))

	// Workers and queued results never need more slots than there are batches.
	bufferSize := len(batches)
	if concurrency <= math.MaxInt/2 {
		bufferSize = concurrency * 2
	}
	if bufferSize > len(batches) {
		bufferSize = len(batches)
	}
	poolOwner := "pool-channels:" + NewWorkID()
	poolCtx := round.Runtime.BuildContext(ctx, poolOwner)
	b, err := backend.NewBuildBoundsFromContext(poolCtx, 0, 0)
	if err != nil {
		return poolResult{}, err
	}
	defer round.Runtime.Admission.ReleaseBuild(poolOwner)
	if err = backend.ReserveSlice[batchJob](b, int64(bufferSize)); err != nil {
		return poolResult{}, err
	}
	if err = backend.ReserveSlice[batchResult](b, int64(bufferSize)); err != nil {
		return poolResult{}, err
	}
	jobs := make(chan batchJob, bufferSize)
	results := make(chan batchResult, bufferSize)
	defer func() { jobs, results = nil, nil }()

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	var handlerErr atomic.Value
	var stopOnce sync.Once
	stop := func(err error) {
		stopOnce.Do(func() {
			handlerErr.Store(err)
			runCancel()
		})
	}
	var pendingMu sync.Mutex

	var nextPending []int
	var fatalUnresolved []int
	var failedSegments []int
	var failedBatches int
	var resolved []int

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
				result := batchResult{deferred: true}
				if waitForBatch(runCtx, round.Gate, job.notBefore) {
					result = process(runCtx, job)
				}
				if result.err != nil {
					result.deferred = true
					stop(result.err)
				}
				if result.deferred && result.waitCapacity == nil {
					pendingMu.Lock()
					nextPending = append(nextPending, job.idxs...)
					pendingMu.Unlock()
				}
				result.idxs = job.idxs
				if result.waitCapacity != nil {
					copyJob := job
					result.waitJob = &copyJob
				}
				results <- result
			}
		}()
	}

	done := make(chan struct{})
	sourceCredits := make(chan struct{}, min(concurrency, max(1, round.Runtime.Policy.PreparePending)))
	returnSourceCredit := func() { <-sourceCredits }
	var submitWg sync.WaitGroup
	submitWg.Add(1)
	go func() {
		defer submitWg.Done()
		for batchIndex, batch := range batches {
			select {
			case sourceCredits <- struct{}{}:
			case <-done:
				pendingMu.Lock()
				for _, remaining := range batches[batchIndex:] {
					nextPending = append(nextPending, remaining...)
				}
				pendingMu.Unlock()
				return
			}
			job := batchJob{idxs: batch}
			for _, idx := range batch {
				cursor := doc.Segments[idx].RoundWork.Cursor
				job.attempt = max(job.attempt, cursor.Attempt)
				if cursor.NextAttemptAt.After(job.notBefore) {
					job.notBefore = cursor.NextAttemptAt
				}
			}
			select {
			case <-done:
				returnSourceCredit()
				// Closing delivery cannot discard the source batches that never
				// reached a worker. They remain eligible in the same pool/attempt.
				pendingMu.Lock()
				for _, remaining := range batches[batchIndex:] {
					nextPending = append(nextPending, remaining...)
				}
				pendingMu.Unlock()
				return
			case jobs <- job:
			}
		}
	}()

	active := len(batches)
	applyBatchResult := func(result batchResult) {
		pendingMu.Lock()
		nextPending = append(nextPending, result.unresolved...)
		fatalUnresolved = append(fatalUnresolved, result.fatalUnresolved...)
		if len(result.failedSegments) > 0 {
			failedSegments = append(failedSegments, result.failedSegments...)
			failedBatches++
		}
		pendingMu.Unlock()

		if !result.deferred && result.retry == nil {
			excluded := append(append([]int(nil), result.unresolved...), result.fatalUnresolved...)
			notifyResolvedSegments(result.idxs, result, reporter)
			if _, durable := reporter.(interface{ FlushCheckpoint(context.Context) error }); !durable {
				reporter.BatchComplete()
			}
			if checked, ok := reporter.(interface{ StageError() error }); ok {
				if err := checked.StageError(); err != nil {
					stop(err)
					return
				}
			}
			resolved = append(resolved, computeResolved(result.idxs, excluded, result.failedSegments)...)
		}
		progress.NotifyWorkState(reporter)
	}

	// deferRetry 把无法重投的在途重试转为未解决（排空期 jobs 已关闭）。
	deferRetry := func(result batchResult) {
		if result.retry == nil {
			return
		}
		pendingMu.Lock()
		nextPending = append(nextPending, result.retry.idxs...)
		pendingMu.Unlock()
	}

	var capacityWaiting []batchResult
	capacityReady := func(result batchResult) bool {
		wait := result.waitCapacity.Wait
		select {
		case <-wait.CapacityChanged:
			return true
		default:
		}
		select {
		case <-wait.PolicyChanged:
			return true
		default:
		}
		return !wait.AvailableAt.IsZero() && !time.Now().Before(wait.AvailableAt)
	}
	capacityWake := false
	for active > 0 {
		preparationChanged := round.Runtime.Preparation.Changed()
		admissionChanged := round.Runtime.Admission.Changed()
		globalPolicyChanged := round.Runtime.Admission.PolicyChanged()
		pauseRequested := round.Gate.Done()
		if capacityWake {
			for i := 0; i < len(capacityWaiting); {
				// A global notification may come from this invocation's own
				// snapshot refund. Only the actual wait condition can authorize
				// retry; unrelated Busy descriptors keep their source credit.
				if !capacityReady(capacityWaiting[i]) {
					i++
					continue
				}
				select {
				case jobs <- *capacityWaiting[i].waitJob:
					copy(capacityWaiting[i:], capacityWaiting[i+1:])
					capacityWaiting[len(capacityWaiting)-1] = batchResult{}
					capacityWaiting = capacityWaiting[:len(capacityWaiting)-1]
				default:
					goto waitingSubmitted
				}
			}
		}
	waitingSubmitted:
		capacityWake = false
		var wakeAt time.Time
		var policyChanged <-chan struct{}
		for _, v := range capacityWaiting {
			w := v.waitCapacity.Wait
			if policyChanged == nil {
				policyChanged = w.PolicyChanged
			}
			if !w.AvailableAt.IsZero() && (wakeAt.IsZero() || w.AvailableAt.Before(wakeAt)) {
				wakeAt = w.AvailableAt
			}
		}
		var timer *time.Timer
		var timerC <-chan time.Time
		if !wakeAt.IsZero() {
			timer = time.NewTimer(max(time.Until(wakeAt), time.Nanosecond))
			timerC = timer.C
		}
		select {
		case <-runCtx.Done():
			if timer != nil {
				timer.Stop()
			}
			goto cleanup
		case <-pauseRequested:
			if timer != nil {
				timer.Stop()
			}
			goto cleanup
		case result := <-results:
			if result.waitCapacity != nil {
				capacityWaiting = append(capacityWaiting, result)
				w := result.waitCapacity.Wait
				select {
				case <-w.CapacityChanged:
					capacityWake = true
				default:
				}
				select {
				case <-w.PolicyChanged:
					capacityWake = true
				default:
				}
				if !w.AvailableAt.IsZero() && !time.Now().Before(w.AvailableAt) {
					capacityWake = true
				}
			} else {
				applyBatchResult(result)

				if result.retry != nil && result.retry.attempt < transientBudget {
					select {
					case <-runCtx.Done():
						deferRetry(result)
						active--
						returnSourceCredit()
					case jobs <- *result.retry:
						// in-flight 重试不递减 active（复用席位）
					}
				} else {
					deferRetry(result)
					active--
					returnSourceCredit()
				}
			}
		case <-preparationChanged:
			capacityWake = true
		case <-admissionChanged:
			capacityWake = true
		case <-policyChanged:
			capacityWake = true
		case <-globalPolicyChanged:
			capacityWake = true
		case <-timerC:
			capacityWake = true
		}
		if timer != nil {
			timer.Stop()
		}
	}

cleanup:
	for _, result := range capacityWaiting {
		pendingMu.Lock()
		nextPending = append(nextPending, result.idxs...)
		pendingMu.Unlock()
	}
	close(done)
	submitWg.Wait()
	close(jobs)

	// 排空 in-flight 结果：worker 可能已经跑完批次（副作用落库、batchHandler
	// 回调成功）却还没把 result 交出来。必须在 wg.Wait() 之前持续接收，否则
	//   (a) 这些批次的计数与断点永久丢失——业务结果已在库里，恢复时却无从判断
	//       该段已处理，非翻译轮会重复调用 LLM，轮次进度也永远补不齐；
	//   (b) results 缓冲在取消时已满且仍有 worker 阻塞在
	//       发送上时，wg.Wait() 永不返回 —— runPool 悬挂 → 资源 goroutine 悬挂
	//       → 整个任务卡死。
	// 排空期 jobs 已关闭，重试不能重投，一律转 nextPending。
	workersDone := make(chan struct{})
	drainResult := func(result batchResult) {
		if result.waitCapacity != nil {
			pendingMu.Lock()
			nextPending = append(nextPending, result.idxs...)
			pendingMu.Unlock()
		}
		applyBatchResult(result)
		deferRetry(result)
	}
	go func() {
		wg.Wait()
		close(workersDone)
	}()
	for workersRunning := true; workersRunning; {
		select {
		case result := <-results:
			drainResult(result)
		case <-workersDone:
			workersRunning = false
		}
	}
	// worker 已全部退出，不再有发送方：关闭后 range 排空缓冲残留。
	close(results)
	for result := range results {
		drainResult(result)
	}

	if v := handlerErr.Load(); v != nil {
		return poolResult{}, v.(error)
	}

	return poolResult{
		resolved:        uniqueSortedInts(resolved),
		unresolved:      nextPending,
		fatalUnresolved: fatalUnresolved,
		failedSegments:  failedSegments,
		failedBatches:   failedBatches,
	}, nil
}

func waitForBatch(ctx context.Context, gate *PauseGate, notBefore time.Time) bool {
	if wait := time.Until(notBefore); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-gate.Done():
			return false
		case <-timer.C:
		}
	}
	return ctx.Err() == nil && !gate.Paused()
}

// Durable completion is owned by the batch processor. The collector reports
// only the confirmed subset and never registers a second completion record.
func notifyResolvedSegments(idxs []int, result batchResult, rep progress.Reporter) {
	if len(idxs) == 0 {
		return
	}
	excluded := make(map[int]struct{}, len(result.unresolved)+len(result.fatalUnresolved)+len(result.failedSegments)+len(idxs))
	for _, idx := range result.unresolved {
		excluded[idx] = struct{}{}
	}
	for _, idx := range result.fatalUnresolved {
		excluded[idx] = struct{}{}
	}
	for _, idx := range result.failedSegments {
		excluded[idx] = struct{}{}
	}
	for _, idx := range idxs {
		if _, ok := excluded[idx]; ok {
			continue
		}
		excluded[idx] = struct{}{} // idxs 内重复索引只通知一次
		rep.SegmentDone()
	}
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
