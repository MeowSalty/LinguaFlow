package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// 本文件覆盖 runPool 的提交确认门（位于 batchHandler 回调之后、结果派发之前，
// 判定条件为 ctx/runCtx 任一已死，对全部 result 形态生效）：
//   - extract 成功空 result：副作用（术语入库）在 ProcessBatch 内以 runCtx 发生，
//     父 ctx 取消时成功形态不代表落库，必须让行——不计数、不登记断点；
//   - semantic_qa/revise 的 preserve 形态：runCtx 被 handlerErr fail-fast 取消
//     （ctx 仍存活）时同样不可信——preserve 与成功同形，必须让行；
//   - 对照：两个 ctx 都存活时，同样的成功空 result 必须正常计数并登记断点，
//     防止门把正常路径也吞掉。

// TestRunPool_ExtractSuccessNotRegisteredAfterCtxCancel extract 轮的取消时序：
// batchHandler 为 nil、ProcessBatch 返回成功空 batchResult（extract 成功形态），
// 术语在 ProcessBatch 内以 runCtx 写库——批次处理期间父 ctx 被取消（模拟写库前
// 一刻取消抵达，handler 静默降级后仍返回成功形态），该批必须按让行处理：段落回
// 未解决、不计数、不登记断点。否则断点被伪造，恢复时该段的术语抽取被永久跳过
// 且零信号。
func TestRunPool_ExtractSuccessNotRegisteredAfterCtxCancel(t *testing.T) {
	doc := newTestDoc(2)
	rep := &resolvedCountingReporter{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := &resolvedSubsetHandler{modeName: "extract", segsPerBatch: 2}
	h.processFn = func(_ []int, _ int) batchResult {
		// 单批单 worker：取消必然发生在本批 ProcessBatch 内、门判定前。
		cancel()
		return batchResult{} // extract 成功形态：空 result，无 callbackResult
	}

	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(ctx, round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v（取消不经 error 上抛）", err)
	}
	dones, resolved, _ := rep.snapshot()
	if dones != 0 {
		t.Fatalf("SegmentDone=%d want 0（副作用未确认不得计数）", dones)
	}
	if len(resolved) != 0 {
		t.Fatalf("SegmentResolved=%v want 空（绝不伪造断点）", resolved)
	}
	if got := sortedCopy(result.Unresolved); !equalInts(got, []int{0, 1}) {
		t.Fatalf("Unresolved=%v want [0 1]（未确认批次的段回到未解决）", got)
	}
	if len(result.Resolved) != 0 {
		t.Fatalf("Resolved=%v want 空", result.Resolved)
	}
}

// TestRunPool_PreserveShapeNotRegisteredAfterFailFast runCtx 被 fail-fast 取消
// （ctx 仍存活）时，在途批次的 preserve/成功同形 result 不可信：semantic_qa 与
// revise 的 preserveResult 与成功形态无法区分，且两者落库用的正是 runCtx
// （batchHandler 形参）。构造：两个在途批次均返回带 callbackResult 的成功同形
// result；先到 batchHandler 的批次阻塞到 runCtx 已死才放行（形参即 runCtx，
// Done 通知先于返回），后到的批次返回错误触发 handlerErr/runCancel——用 handler
// 内部同步把「runCtx 已死才过门」的顺序钉死，不依赖调度时序。
func TestRunPool_PreserveShapeNotRegisteredAfterFailFast(t *testing.T) {
	doc := newTestDoc(2)
	rep := &resolvedCountingReporter{}

	h := &resolvedSubsetHandler{modeName: "semantic_qa", segsPerBatch: 1}
	h.processFn = func(idxs []int, _ int) batchResult {
		// preserve 形态：原文回填、不产出新 issue（Issues 为 nil），
		// batchHandler 跳过写库后返回 nil——与成功形态无法区分。
		segs := make([]TranslatedSegment, 0, len(idxs))
		for _, idx := range idxs {
			segs = append(segs, TranslatedSegment{
				Index:      idx,
				ID:         doc.Segments[idx].ID,
				SourceText: doc.Segments[idx].Source,
				TargetText: doc.Segments[idx].Target,
			})
		}
		return batchResult{callbackResult: &BatchResult{Segments: segs}}
	}

	handlerErr := errors.New("persist failed")
	var handlerCalls atomic.Int32
	batchHandler := func(batchCtx context.Context, _ BatchResult) error {
		if handlerCalls.Add(1) == 1 {
			// 先到的批次（preserve 形态）阻塞到触发批报错取消 runCtx 为止：
			// 保证它在 fail-fast 落地之后才走到提交确认门。超时兜底让构造失败
			//（触发批未报错）以断言失败收场而非悬挂。
			select {
			case <-batchCtx.Done():
				return nil
			case <-time.After(5 * time.Second):
				return nil
			}
		}
		return handlerErr // 触发批：handlerErr fail-fast，runCancel 取消 runCtx
	}

	round := Round{
		Concurrency: 2, // 两批各占一个 worker，双方必然都抵达 batchHandler
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	_, err := RunRound(context.Background(), round, doc, batchHandler, quietLogger(), rep)
	if !errors.Is(err, handlerErr) {
		t.Fatalf("RunRound err=%v，want batchHandler 错误上抛", err)
	}
	dones, resolved, _ := rep.snapshot()
	if dones != 0 {
		t.Fatalf("SegmentDone=%d want 0（runCtx 已死的批次不得计数）", dones)
	}
	if len(resolved) != 0 {
		t.Fatalf("SegmentResolved=%v want 空（runCtx 被 fail-fast 取消时 "+
			"preserve 形态不可信，绝不伪造断点）", resolved)
	}
}

// TestRunPool_ExtractSuccessCountedWhenCtxAlive 对照组（防过度让行）：ctx 与
// runCtx 都存活时，同样的成功空 batchResult（extract 形态、batchHandler 为 nil）
// 必须正常计数并登记断点——门只在任一 ctx 已死时让行，不得吞掉正常路径。
func TestRunPool_ExtractSuccessCountedWhenCtxAlive(t *testing.T) {
	doc := newTestDoc(2)
	rep := &resolvedCountingReporter{}

	h := &resolvedSubsetHandler{modeName: "extract", segsPerBatch: 2}
	// processFn 为 nil → 默认返回空 batchResult{}：extract 成功形态。

	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	dones, resolved, completes := rep.snapshot()
	if dones != 2 {
		t.Fatalf("SegmentDone=%d want 2（ctx 存活时成功批正常计数）", dones)
	}
	if len(resolved) != 2 {
		t.Fatalf("SegmentResolved=%v want 2 项（与 SegmentDone 配对）", resolved)
	}
	if completes != 1 {
		t.Fatalf("BatchComplete=%d want 1（逐批 flush）", completes)
	}
	if got := sortedCopy(result.Resolved); !equalInts(got, []int{0, 1}) {
		t.Fatalf("Resolved=%v want [0 1]", got)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved=%v want 空", result.Unresolved)
	}
}
