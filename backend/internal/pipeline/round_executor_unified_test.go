package pipeline

import (
	"context"
	"log/slog"
	"sort"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// TestRunRound_MaxAttemptsZeroSinglePool 验证 max_attempts=0 时单池（无重试）。
// 成功一次即终止，仅一个 pool_start 事件。
func TestRunRound_MaxAttemptsZeroSinglePool(t *testing.T) {
	doc := newTestDoc(2)
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"a","2":"b"}}`,
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.Retry = backend.RetryPolicy{MaxAttempts: 0} // 单池
	})
	h.Renderer = newTestRenderer(t)

	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved=%v want empty", result.Unresolved)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	// max_attempts=0 → maxPools=1，仅 pool0_start
	if len(rep.events) != 1 {
		t.Fatalf("pool events=%d want 1 (single pool): %+v", len(rep.events), rep.events)
	}
	if rep.events[0].MaxPools != 1 {
		t.Fatalf("events[0].MaxPools=%d want 1", rep.events[0].MaxPools)
	}
}

// TestRunRound_ShrinkOneMultiPoolSameSize 验证 shrink=1.0 + max_attempts=2 → 三池同尺寸重切。
// 第一池全失败（parse）→ 进池 1 → 进池 2；shrink=1.0 不缩批。
func TestRunRound_ShrinkOneMultiPoolSameSize(t *testing.T) {
	doc := newTestDoc(2)
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"not-json", // pool0 全失败
			// pool1/pool2 同尺寸（shrink=1.0）仍两段一批
			`{"translations":{"1":"a","2":"b"}}`, // pool1 成功
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.FallbackShrink = 1.0
		h.Retry = backend.RetryPolicy{MaxAttempts: 2} // maxPools=3
	})
	h.Renderer = newTestRenderer(t)

	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      1.0,
		Handler:     h,
	}
	_, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	// 三池：pool0_start → pool0_advance → pool1_start（pool1 成功即终止）
	if len(rep.events) != 3 {
		t.Fatalf("pool events=%d want 3: %+v", len(rep.events), rep.events)
	}
	for i, e := range rep.events {
		if e.MaxPools != 3 {
			t.Fatalf("events[%d].MaxPools=%d want 3", i, e.MaxPools)
		}
		if e.ShrinkRate != 1.0 {
			t.Fatalf("events[%d].ShrinkRate=%v want 1.0", i, e.ShrinkRate)
		}
	}
}

// fakeRoundHandler 是用于测试池/重试/致命错误路由的最小 RoundHandler。
type fakeRoundHandler struct {
	modeName        string
	pending         []int // 非 nil 时 BuildBatches 返回这些索引；nil 时返回所有 doc 段
	buildCalls      int   // BuildBatches 被调用的次数（用于断言池数）
	processFn       func(idxs []int, attempt int) batchResult
	finalUnresolved []int
}

func (h *fakeRoundHandler) ModeName() string { return h.modeName }

func (h *fakeRoundHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	h.buildCalls++
	if pending != nil {
		return [][]int{pending}, nil
	}
	if h.pending != nil {
		return [][]int{h.pending}, nil
	}
	idxs := make([]int, len(doc.Segments))
	for i := range doc.Segments {
		idxs[i] = i
	}
	return [][]int{idxs}, nil
}

func (h *fakeRoundHandler) ProcessBatch(_ context.Context, _ *Document, idxs []int, attempt int, _ *slog.Logger) batchResult {
	if h.processFn != nil {
		return h.processFn(idxs, attempt)
	}
	return batchResult{}
}

func (h *fakeRoundHandler) Finalize(_ context.Context, _ *Document, unresolved []int) error {
	h.finalUnresolved = append([]int(nil), unresolved...)
	return nil
}

// TestRunRound_FatalUnresolvedSkipsRemainingPools 验证 fatalUnresolved 段跳过剩余池，
// 但仍计入 finalUnresolved 供跨轮传播。
func TestRunRound_FatalUnresolvedSkipsRemainingPools(t *testing.T) {
	doc := newTestDoc(2)
	rep := &recordingPoolObserver{}
	h := &fakeRoundHandler{
		modeName: "translate",
		processFn: func(idxs []int, attempt int) batchResult {
			// 段 0 致命失败（401）→ fatalUnresolved；段 1 成功
			return batchResult{fatalUnresolved: []int{0}}
		},
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 3}, // maxPools=4
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// fatalUnresolved 段（0）并入 finalUnresolved（跨轮传播）
	want := []int{0}
	got := append([]int{}, result.Unresolved...)
	sort.Ints(got)
	if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
		t.Fatalf("Unresolved=%v want %v", got, want)
	}
	// 关键：fatalUnresolved 段跳过剩余池——BuildBatches 只被调用一次（仅 pool 0）
	// （否则段 0 会进 pending 进入 pool 1）
	if h.buildCalls != 1 {
		t.Fatalf("BuildBatches calls=%d want 1 (fatalUnresolved skips remaining pools)", h.buildCalls)
	}
}

// TestRunRound_ResolvedExcludesUnresolved 验证 Resolved = 池 0 扫描集合 − finalUnresolved − failedSegments。
func TestRunRound_ResolvedExcludesUnresolved(t *testing.T) {
	doc := newTestDoc(4)
	// 4 段：段 0、1 成功；段 2 unresolved（进池重切）；段 3 fatalUnresolved。
	// 单池（max_attempts=0）→ unresolved 与 fatalUnresolved 均并入 finalUnresolved。
	h := &fakeRoundHandler{
		modeName: "translate",
		processFn: func(idxs []int, attempt int) batchResult {
			var fatal, unresolved []int
			for _, i := range idxs {
				if i == 2 {
					unresolved = append(unresolved, i)
				} else if i == 3 {
					fatal = append(fatal, i)
				}
			}
			if len(fatal) > 0 {
				return batchResult{fatalUnresolved: fatal, unresolved: unresolved}
			}
			return batchResult{unresolved: unresolved}
		},
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0}, // 单池
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// 池 0 扫描 = [0,1,2,3]；finalUnresolved = [2,3]（unresolved+fatal）；Resolved 应 = [0,1]
	wantResolved := []int{0, 1}
	got := append([]int{}, result.Resolved...)
	sort.Ints(got)
	if len(got) != len(wantResolved) {
		t.Fatalf("Resolved=%v want %v", got, wantResolved)
	}
	for i := range wantResolved {
		if got[i] != wantResolved[i] {
			t.Fatalf("Resolved=%v want %v", got, wantResolved)
		}
	}
}

// TestRunRound_ResolvedIndicesExcludedFromScan 验证 handler 的 BuildBatches 在
// pending==nil（池 0）时排除 doc.ResolvedIndices 中的段，且 Resolved 不含被排除段。
// 这是跨轮增量的核心保证：上一同模式轮 resolved 的段不会被下一轮全量重扫。
// 注：fakeRoundHandler 的 BuildBatches 不读 ResolvedIndices（模拟生产 handler 是 RunRound
// 的黑盒），故此处用一个尊重 ResolvedIndices 的自定义 handler 验证端到端语义。
func TestRunRound_ResolvedIndicesExcludedFromScan(t *testing.T) {
	doc := newTestDoc(4)
	// 预设段 0、2 已由上一同模式轮解决（跨轮增量载体）。
	doc.ResolvedIndices = map[int]struct{}{0: {}, 2: {}}

	scanned := make(map[int]struct{})
	h := &resolvedAwareHandler{
		modeName: "extract",
		scanFn: func(doc *Document, pending []int) []int {
			if pending != nil {
				return pending // 池>0 不参考 ResolvedIndices
			}
			var out []int
			for i := range doc.Segments {
				if doc.ResolvedIndices != nil {
					if _, ok := doc.ResolvedIndices[i]; ok {
						continue
					}
				}
				out = append(out, i)
			}
			return out
		},
		processFn: func(idxs []int) batchResult {
			for _, i := range idxs {
				scanned[i] = struct{}{}
			}
			return batchResult{}
		},
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0}, // 单池
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// 池 0 扫描应排除段 0、2（仅 [1,3]）。
	for _, excluded := range []int{0, 2} {
		if _, hit := scanned[excluded]; hit {
			t.Fatalf("段 %d 不应被扫描（ResolvedIndices 排除），但实际被处理", excluded)
		}
	}
	for _, want := range []int{1, 3} {
		if _, hit := scanned[want]; !hit {
			t.Fatalf("段 %d 应被扫描但缺失", want)
		}
	}
	// Resolved = 扫描集合 [1,3] − 空 unresolved − 空 failedSegments = [1,3]。
	wantResolved := []int{1, 3}
	got := append([]int{}, result.Resolved...)
	sort.Ints(got)
	if len(got) != len(wantResolved) {
		t.Fatalf("Resolved=%v want %v", got, wantResolved)
	}
	for i := range wantResolved {
		if got[i] != wantResolved[i] {
			t.Fatalf("Resolved=%v want %v", got, wantResolved)
		}
	}
}

// resolvedAwareHandler 是尊重 doc.ResolvedIndices 的测试 handler，
// 模拟生产 extract/adjudicate/semantic_qa 的 BuildBatches 语义（池 0 排除已解决段）。
type resolvedAwareHandler struct {
	modeName  string
	scanFn    func(doc *Document, pending []int) []int
	processFn func(idxs []int) batchResult
	finalCall int
}

func (h *resolvedAwareHandler) ModeName() string { return h.modeName }

func (h *resolvedAwareHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	idxs := h.scanFn(doc, pending)
	if len(idxs) == 0 {
		return nil, nil
	}
	return [][]int{idxs}, nil
}

func (h *resolvedAwareHandler) ProcessBatch(_ context.Context, _ *Document, idxs []int, _ int, _ *slog.Logger) batchResult {
	if h.processFn != nil {
		return h.processFn(idxs)
	}
	return batchResult{}
}

func (h *resolvedAwareHandler) Finalize(_ context.Context, _ *Document, _ []int) error { return nil }

// TestRunRound_PendingIgnoresResolvedIndices 验证 pending!=nil（池>0）时 handler
// 仅处理给定 unresolved，不参考 doc.ResolvedIndices（跨轮增量只在池 0 生效）。
func TestRunRound_PendingIgnoresResolvedIndices(t *testing.T) {
	doc := newTestDoc(4)
	// 即便 ResolvedIndices 含段 1，池>0 收到 pending=[1] 仍应处理它。
	doc.ResolvedIndices = map[int]struct{}{1: {}}

	var processed []int
	h := &fakeRoundHandler{
		modeName: "extract",
		processFn: func(idxs []int, attempt int) batchResult {
			processed = append(processed, idxs...)
			return batchResult{}
		},
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 1}, // maxPools=2
		Shrink:      1.0,
		Handler:     h,
	}
	// 直接构造：池 0 提供初始 pending（绕过 BuildBatches 扫描）。
	// 通过两次 RunRound 模拟：第一次让段 1 unresolved 进池 1，第二次验证。
	// 实际上更简单：构造一个首轮全 unresolved 的 handler，验证池 1 处理段 1。
	h.processFn = func(idxs []int, attempt int) batchResult {
		processed = append(processed, idxs...)
		// 池 0（attempt=0）返回段 unresolved；池 1（attempt>0 的重试不适用此场景，
		// 因 pending 推进发生在 unresolved 通道，而非 retry）。
		// 用 unresolved 让段进入池 1。
		if attempt == 0 {
			return batchResult{unresolved: idxs}
		}
		return batchResult{}
	}
	_, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// 段 1 应被处理（即便在 ResolvedIndices 中），证明池>0 不受 ResolvedIndices 影响。
	seg1Hit := false
	for _, i := range processed {
		if i == 1 {
			seg1Hit = true
		}
	}
	if !seg1Hit {
		t.Fatalf("段 1 在 pending（池>0）中应被处理，实际处理集=%v", processed)
	}
}
