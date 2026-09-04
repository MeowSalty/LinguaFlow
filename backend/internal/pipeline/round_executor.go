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
		// 池推进前暂停检查：暂停时不进入新池（本池已完成批次的结果保留，
		// 未解决段落由断点集合覆盖，resume 时从 pending 重新切批）。
		if round.Gate != nil && round.Gate.Paused() {
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

		pr, err := runPool(ctx, round, handler, doc, batches, transientBudget, batchHandler, logger, reporter)
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
// transientBudget 是在途重试预算（内部封顶后的值）。reporter 承接收集点的
// 段计数/断点登记（本函数是计数的唯一判定源，见收集点注释）。
func runPool(
	ctx context.Context,
	round Round,
	handler RoundHandler,
	doc *Document,
	batches [][]int,
	transientBudget int,
	batchHandler func(ctx context.Context, result BatchResult) error,
	logger *slog.Logger,
	reporter progress.Reporter,
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
				// 暂停闸门：批次派发前检查，暂停中不派发新批次。
				// 检查通过后先登记在途计数再获取槽位；任一步失败（ctx 取消）
				// 则本批段落保持未解决，由断点集合/下一轮 pending 过滤覆盖。
				// 暂停让行必须发空 result 保持 active 计数平衡：pause 不取消
				// runCtx，主循环无法经 Done 分支提前退出，只能靠 result 归零。
				if round.Gate != nil {
					if round.Gate.Paused() {
						pendingMu.Lock()
						nextPending = append(nextPending, job.idxs...)
						pendingMu.Unlock()
						results <- batchResult{deferred: true, idxs: job.idxs}
						continue
					}
					round.Gate.AcquireInflight()
				}
				if round.Slots != nil {
					if !round.Slots.Acquire(runCtx) {
						if round.Gate != nil {
							round.Gate.ReleaseInflight()
						}
						pendingMu.Lock()
						nextPending = append(nextPending, job.idxs...)
						pendingMu.Unlock()
						results <- batchResult{deferred: true, idxs: job.idxs}
						continue
					}
				}
				// 获取槽位可能阻塞较久（Station 容量被所有在途资源共享），
				// 阻塞期间到达的暂停需在此复查——否则排队 worker 会在槽位
				// 释放后照常执行一整批 LLM 调用，排空时间被放大为排队深度。
				if round.Gate != nil && round.Gate.Paused() {
					if round.Slots != nil {
						round.Slots.Release()
					}
					round.Gate.ReleaseInflight()
					pendingMu.Lock()
					nextPending = append(nextPending, job.idxs...)
					pendingMu.Unlock()
					results <- batchResult{deferred: true, idxs: job.idxs}
					continue
				}

				result := handler.ProcessBatch(runCtx, doc, job.idxs, job.attempt, logger)

				if round.Slots != nil {
					round.Slots.Release()
				}
				if round.Gate != nil {
					round.Gate.ReleaseInflight()
				}

				if batchHandler != nil && result.callbackResult != nil {
					if herr := batchHandler(runCtx, *result.callbackResult); herr != nil {
						logger.Error("batch handler error, terminating pool", "err", herr)
						handlerErr.Store(herr)
						runCancel()
						pendingMu.Lock()
						nextPending = append(nextPending, job.idxs...)
						pendingMu.Unlock()
						results <- batchResult{deferred: true, idxs: job.idxs}
						continue
					}
				}
				// 提交确认门：副作用是否真的落库无法从 result 的成功形态判断，必须
				// 看两个 ctx——副作用经由哪条 ctx 写库没有统一约定：走 batchHandler
				// 的模式中 translate/correct/adjudicate 忽略形参、以闭包捕获的资源级
				// ctx 落库（见 worker/job_runner.go 的 batchHandler 声明），故以 ctx
				// 判定；semantic_qa/revise 以形参（即 runCtx）落库，extract 无
				// batchHandler、术语在 ProcessBatch 内以 runCtx 写库，故 runCtx 也
				// 必须判定——它同时覆盖 handlerErr fail-fast 取消 runCtx（ctx 仍
				// 存活）的窗口，以及父 ctx 取消向子 ctx 传播的瞬间窗口。ctx 已死时
				// 各 handler 对 context.Canceled 的处理是「静默跳过该段并返回 nil」，
				// 而 semantic_qa/revise 的 preserveResult 与成功形态无法区分——
				// 回调返回 nil 也不可信。任一 ctx 已死即按让行处理：段落回
				// nextPending，绝不计数、绝不登记断点。
				//
				// 方向性取舍：伪造断点会让非翻译轮在恢复时永久跳过该段（裁决/
				// 质检/修订/术语抽取结果静默丢失），而漏登记只是恢复后重扫一次
				// （翻译轮由 Segment.status 过滤掉，零成本）。宁可重扫，绝不伪造。
				// 暂停不取消 ctx（PauseGate 与 ctx 解耦），pause/resume 主流程不走
				// 这道门。代价：runCtx 因 fail-fast 被取消时会多让行几个在途批次，
				// 该轮本就会 abort 并重跑，重扫成本可接受。
				if ctx.Err() != nil || runCtx.Err() != nil {
					pendingMu.Lock()
					nextPending = append(nextPending, job.idxs...)
					pendingMu.Unlock()
					results <- batchResult{deferred: true, idxs: job.idxs}
					continue
				}

				result.idxs = job.idxs
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
	// resolvedNotifier 是支持轮次断点持久化的 Reporter（当前仅 DBReporter）；
	// 未实现则跳过断点登记（与 emitPoolEvent 的探测惯例一致）。
	resolvedNotifier, _ := reporter.(progress.SegmentResolvedNotifier)

	// applyBatchResult 是收集点记账的唯一实现：主循环与 cleanup 排空共用同一份
	// 终态判定，避免两条路径语义漂移（排空路径若自带一份判定，取消场景的计数
	// 与断点口径会与正常路径悄悄分叉）。重试的席位调度不在此处——它需要 active
	// 记账与 jobs 通道，排空期两者都已不可用。
	//
	// 计数与断点登记的唯一判定源：本批终态时从 job.idxs 推导 resolved 子集，
	// 逐段 SegmentDone（计数）+ SegmentResolved（断点），随后 BatchComplete
	// 触发缓冲区 flush。handler 不再触碰进度计数。
	//
	// 批次终态判定（按收集到的 result 形态）：
	//   - deferred=true：让行点（暂停/槽位失败/batchHandler 失败/副作用未确认
	//     （含 extract）），idxs 已进 nextPending——空 result 不代表全批成功，
	//     跳过一切计数；
	//   - retry != nil：同批退避重试（含超预算转 nextPending），非终态——
	//     重试后的终态批次再计，保证池推进/在途重试不重复计数；
	//   - 其余为终态：resolved = idxs − unresolved − fatalUnresolved − failedSegments。
	//
	// 各 handler 返回组合的核实结论（resolved 公式对全部组合成立）：
	//   - translate：unresolved 为漏译/占位符违规段（FilterPendingIdxs 过滤
	//     上下文段后），成功段仅写 doc 不单独报告；fatal=401/403 整批
	//     fatalUnresolved。unresolved 恒 ⊆ idxs，idxs−unresolved 即成功段；
	//   - extract：成功=空 batchResult（idxs 全 resolved）；失败=unresolved/
	//     fatalUnresolved 整批（idxs）；无部分成功形态。成功的空 result 也要过
	//     提交确认门——术语在 ProcessBatch 内以 runCtx 落库，ctx 已死时成功
	//     形态同样不代表副作用落库，由门转 deferred；
	//   - adjudicate：成功=callbackResult（idxs 全 resolved）；失败=unresolved/
	//     fatalUnresolved 整批；无部分成功形态；
	//   - semantic_qa：成功=callbackResult（idxs 全 resolved）；失败=unresolved
	//     整批、fatalUnresolved 整批、terminalFailure=failedSegments 整批（软警告，
	//     未解决——callbackResult 里的 preserve 段不计 resolved）；外部中断
	//     preserve：ctx 或 runCtx 任一已死时都被 worker 侧的提交确认门转成
	//     deferred，不会以终态形态到达本函数——runCtx 被 fail-fast 取消时
	//     （ctx 仍存活）preserve 形态与成功同形，同样不可信；
	//   - revise：部分成功=callbackResult（returned 段）+ unresolved=missing 段，
	//     idxs−missing 恰为 callback 段；terminalFailure=failedSegments（未解决，
	//     callback 内的 preserve 段不计）；fatal=整批 fatalUnresolved；外部中断
	//     preserve 同 semantic_qa（ctx 或 runCtx 任一已死时被门转成 deferred）；
	//   - correct：纯本地恒成功=callbackResult（idxs 全 resolved）。
	//   综上：failedSegments 恒 = 整批 idxs 且恒伴随 callbackResult、从不伴随
	//   unresolved/retry；unresolved/fatalUnresolved 恒 ⊆ idxs；无任何形态
	//   会把同一 idx 同时计入 resolved 与失败/未解决集合。
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
			notifyResolvedSegments(result.idxs, result, reporter, resolvedNotifier)
			reporter.BatchComplete()
		}
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

	for active > 0 {
		select {
		case <-runCtx.Done():
			goto cleanup
		case result := <-results:
			applyBatchResult(result)

			if result.retry != nil && result.retry.attempt < transientBudget {
				select {
				case <-runCtx.Done():
					deferRetry(result)
					active--
				case jobs <- *result.retry:
					// in-flight 重试不递减 active（复用席位）
				}
			} else {
				deferRetry(result)
				active--
			}
		}
	}

cleanup:
	close(done)
	submitWg.Wait()
	close(jobs)

	// 排空 in-flight 结果：worker 可能已经跑完批次（副作用落库、batchHandler
	// 回调成功）却还没把 result 交出来。必须在 wg.Wait() 之前持续接收，否则
	//   (a) 这些批次的计数与断点永久丢失——业务结果已在库里，恢复时却无从判断
	//       该段已处理，非翻译轮会重复调用 LLM，轮次进度也永远补不齐；
	//   (b) results 缓冲（容量 concurrency*2）在取消时已满且仍有 worker 阻塞在
	//       发送上时，wg.Wait() 永不返回 —— runPool 悬挂 → 资源 goroutine 悬挂
	//       → 整个任务卡死。
	// 排空期 jobs 已关闭，重试不能重投，一律转 nextPending。
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()
	for workersRunning := true; workersRunning; {
		select {
		case result := <-results:
			applyBatchResult(result)
			deferRetry(result)
		case <-workersDone:
			workersRunning = false
		}
	}
	// worker 已全部退出，不再有发送方：关闭后 range 排空缓冲残留。
	close(results)
	for result := range results {
		applyBatchResult(result)
		deferRetry(result)
	}

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

// notifyResolvedSegments 对终态批次的 resolved 子集逐段通知：
// SegmentDone 推进进度计数，SegmentResolved（Reporter 实现
// SegmentResolvedNotifier 时）登记轮次断点，两者由同一判定驱动，
// 保证 segment_completed ≡ 断点集合基数的 checkpoint 不变式。
//
// resolved = idxs − unresolved − fatalUnresolved − failedSegments（各自去重；
// 计数口径从「已尝试」收敛为「已解决」：失败尝试、池推进重试与暂停让行
// 段均不计数，completed 恒 ≤ total）。
// unresolvable 恒 ⊆ idxs（核实结论见收集点注释），故每个 idx 至多通知一次；
// 若 handler 返回异常组合（集合溢出 idxs），以 idxs 为界防御性截断。
func notifyResolvedSegments(
	idxs []int,
	result batchResult,
	rep progress.Reporter,
	notifier progress.SegmentResolvedNotifier,
) {
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
		if notifier != nil {
			notifier.SegmentResolved(idx)
		}
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
