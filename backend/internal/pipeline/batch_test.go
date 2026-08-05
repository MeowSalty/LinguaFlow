package pipeline

import (
	"reflect"
	"testing"
)

// testDoc 构建一个简单的 Document 用于测试，每段 Source 为 "seg-N"。
func testDoc(n int) *Document {
	segs := make([]Segment, n)
	for i := 0; i < n; i++ {
		segs[i] = Segment{Source: "seg-" + itoa(i), Translate: true}
	}
	return &Document{Segments: segs}
}

// testDocWithSources 构建 Document，使用自定义的每段 Source 文本。
func testDocWithSources(sources []string) *Document {
	segs := make([]Segment, len(sources))
	for i, s := range sources {
		segs[i] = Segment{Source: s, Translate: true}
	}
	return &Document{Segments: segs}
}

func segConstraint(maxSegs int) BatchConstraint {
	return BatchConstraint{MaxSegments: maxSegs}
}

func wordConstraint(maxWords int) BatchConstraint {
	return BatchConstraint{MaxWords: maxWords}
}

func dualConstraint(maxSegs, maxWords int) BatchConstraint {
	return BatchConstraint{MaxSegments: maxSegs, MaxWords: maxWords}
}

func TestBuildContextAwareBatches_Disabled(t *testing.T) {
	doc := testDoc(10)
	constraint := segConstraint(5)
	got := BuildContextAwareBatches(doc, []int{1, 3, 7}, constraint, 1, false, nil)
	want := BuildContinuousPendingBatches(doc, []int{1, 3, 7}, constraint)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("disabled should fall back to continuous, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_WindowZero(t *testing.T) {
	doc := testDoc(10)
	constraint := segConstraint(5)
	got := BuildContextAwareBatches(doc, []int{1, 3, 7}, constraint, 0, true, nil)
	want := BuildContinuousPendingBatches(doc, []int{1, 3, 7}, constraint)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=0 should fall back to continuous, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_NoOverlap(t *testing.T) {
	doc := testDoc(10)
	// 1 和 7 的窗口 [0,2] 和 [6,8] 不重叠
	got := BuildContextAwareBatches(doc, []int{1, 7}, segConstraint(10), 1, true, nil)
	want := [][]int{{1}, {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non-overlapping should be separate batches, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Overlap(t *testing.T) {
	doc := testDoc(10)
	// 1 的窗口 [0,2]，3 的窗口 [2,4]，重叠 → 合并
	got := BuildContextAwareBatches(doc, []int{1, 3}, segConstraint(10), 1, true, nil)
	want := [][]int{{1, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overlapping should merge, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Window2_Merge(t *testing.T) {
	doc := testDoc(10)
	// 1 的窗口 [-1,3]，5 的窗口 [3,7]，重叠 → 合并
	got := BuildContextAwareBatches(doc, []int{1, 5}, segConstraint(10), 2, true, nil)
	want := [][]int{{1, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=2 should merge 1 and 5, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Window2_Separate(t *testing.T) {
	doc := testDoc(10)
	// 1 的窗口 [-1,3]，7 的窗口 [5,9]，不重叠
	got := BuildContextAwareBatches(doc, []int{1, 7}, segConstraint(10), 2, true, nil)
	want := [][]int{{1}, {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=2 should separate 1 and 7, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_BatchSizeSplit(t *testing.T) {
	doc := testDoc(10)
	// 合并后 [1,3,5,7,9]，batchSize=2
	got := BuildContextAwareBatches(doc, []int{1, 3, 5, 7, 9}, segConstraint(2), 1, true, nil)
	want := [][]int{{1, 3}, {5, 7}, {9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("batch size splitting failed, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Empty(t *testing.T) {
	doc := testDoc(1)
	got := BuildContextAwareBatches(doc, nil, segConstraint(5), 1, true, nil)
	if got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}

func TestBuildContextAwareBatches_WordConstraint(t *testing.T) {
	sources := []string{"a", "bb", "ccc", "dddd", "eeeee"}
	doc := testDocWithSources(sources)
	// MaxWords=4: "a"(1) + "bb"(1) + "ccc"(1) = 3 words → 继续; + "dddd"(1) = 4 → 继续; + "eeeee"(1) = 5 > 4 → 切
	got := BuildContextAwareBatches(doc, []int{0, 1, 2, 3, 4}, wordConstraint(4), 0, false, nil)
	want := [][]int{{0, 1, 2, 3}, {4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("word constraint: got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_CJKWordConstraint(t *testing.T) {
	sources := []string{"你好世界", "hello", "世界"}
	doc := testDocWithSources(sources)
	// "你好世界" = 4 CJK words, "hello" = 1 word, "世界" = 2 CJK words
	// MaxWords=5: 4+1=5 → ok; +2=7 > 5 → 切
	got := BuildContextAwareBatches(doc, []int{0, 1, 2}, wordConstraint(5), 0, false, nil)
	want := [][]int{{0, 1}, {2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CJK word constraint: got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_DualConstraint_SegmentsFirst(t *testing.T) {
	sources := []string{"a", "b", "c", "d", "e"}
	doc := testDocWithSources(sources)
	// MaxSegments=2, MaxWords=100 → segments limit hits first
	got := BuildContextAwareBatches(doc, []int{0, 1, 2, 3, 4}, dualConstraint(2, 100), 0, false, nil)
	want := [][]int{{0, 1}, {2, 3}, {4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dual constraint segments first: got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_DualConstraint_WordsFirst(t *testing.T) {
	sources := []string{"hello world", "foo bar", "x", "y"}
	doc := testDocWithSources(sources)
	// "hello world"=2, "foo bar"=2, "x"=1, "y"=1
	// MaxSegments=100, MaxWords=3 → words limit: 2+2=4 > 3 → 切 after first seg
	// [0]=2 words → ok; [1]=2 words, 2+2=4>3 → 切; [2]=1, [3]=1, 1+1=2≤3 → 合并
	got := BuildContextAwareBatches(doc, []int{0, 1, 2, 3}, dualConstraint(100, 3), 0, false, nil)
	want := [][]int{{0}, {1, 2}, {3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dual constraint words first: got %v want %v", got, want)
	}
}

func TestSplitByConstraint_SingleSegmentExceeds(t *testing.T) {
	sources := []string{"this is a very long sentence with many words"}
	doc := testDocWithSources(sources)
	// 单段超限 → 独占一个批次
	got := splitByConstraint(doc, []int{0}, wordConstraint(3))
	want := [][]int{{0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single segment exceeds: got %v want %v", got, want)
	}
}

func TestSplitByConstraint_BothZero(t *testing.T) {
	doc := testDoc(5)
	// 两者都为 0 → 不切分
	got := splitByConstraint(doc, []int{0, 1, 2, 3, 4}, BatchConstraint{})
	want := [][]int{{0, 1, 2, 3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("both zero: got %v want %v", got, want)
	}
}

func TestBuildContinuousPendingBatches_WithWordConstraint(t *testing.T) {
	sources := []string{"a", "b", "c", "x", "y"}
	doc := testDocWithSources(sources)
	// 单一连续组 [0,1,2,3,4]
	// MaxWords=2: 每段 1 词
	// splitByConstraint: [0,1](2 words) → 切; [2,3](2 words) → 切; [4](1 word) → 余
	got := BuildContinuousPendingBatches(doc, []int{0, 1, 2, 3, 4}, wordConstraint(2))
	// batches: [0,1], [2,3]; leftovers: [4] → final: [0,1], [2,3], [4]
	want := [][]int{{0, 1}, {2, 3}, {4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("continuous with word constraint: got %v want %v", got, want)
	}
}

func TestBuildContinuousPendingBatches_DiscontinuousWithWordConstraint(t *testing.T) {
	sources := []string{"a", "b", "c", "x", "y"}
	doc := testDocWithSources(sources)
	// 不连续组: [0,1,2] 和 [4]
	// MaxWords=2: 每段 1 词
	// run [0,1,2] → split to [0,1](batch), [2](leftover)
	// run [4] → [4](leftover)
	// batches: [0,1]; leftovers: [2](len=1), [4](len=1) → sorted by idx: [2], [4]
	got := BuildContinuousPendingBatches(doc, []int{0, 1, 2, 4}, wordConstraint(2))
	want := [][]int{{0, 1}, {2}, {4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("discontinuous with word constraint: got %v want %v", got, want)
	}
}

func TestBuildPackedPendingBatches_DiscontinuousFills(t *testing.T) {
	sources := []string{"a", "b", "c", "x", "y"}
	doc := testDocWithSources(sources)
	// 允许空洞：pending [0,1,2,4] 每段 1 词，MaxWords=2
	// [0,1](2) → 切; [2,4](2) 可同批（索引不连续）
	got := BuildPackedPendingBatches(doc, []int{0, 1, 2, 4}, wordConstraint(2), 0)
	want := [][]int{{0, 1}, {2, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packed discontinuous: got %v want %v", got, want)
	}
}

func TestBuildPackedPendingBatches_SegmentLimit(t *testing.T) {
	doc := testDoc(10)
	got := BuildPackedPendingBatches(doc, []int{0, 2, 5, 7, 9}, segConstraint(2), 0)
	want := [][]int{{0, 2}, {5, 7}, {9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packed seg limit: got %v want %v", got, want)
	}
}

func TestBuildPackedPendingBatches_MaxIndexSpan(t *testing.T) {
	doc := testDoc(20)
	// MaxSegments 足够大；span=3：同批 max-min 不得超过 3
	// pending: 0,1,5,6,10
	// [0,1] span=1 ok; +5 → 5-0=5>3 → 切; [5,6] ok; +10 → 10-5=5>3 → 切; [10]
	got := BuildPackedPendingBatches(doc, []int{0, 1, 5, 6, 10}, segConstraint(10), 3)
	want := [][]int{{0, 1}, {5, 6}, {10}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packed index span: got %v want %v", got, want)
	}
}

func TestBuildPackedPendingBatches_SpanZeroDisables(t *testing.T) {
	doc := testDoc(20)
	// span=0 与负值均不限制跨度，仅受段数约束
	got := BuildPackedPendingBatches(doc, []int{0, 10, 19}, segConstraint(3), 0)
	want := [][]int{{0, 10, 19}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("span disabled: got %v want %v", got, want)
	}
	got = BuildPackedPendingBatches(doc, []int{0, 10, 19}, segConstraint(3), -1)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("span negative disabled: got %v want %v", got, want)
	}
}

func TestBuildPackedPendingBatches_Empty(t *testing.T) {
	doc := testDoc(1)
	got := BuildPackedPendingBatches(doc, nil, segConstraint(5), 0)
	if got != nil {
		t.Errorf("empty pending: got %v want nil", got)
	}
}

// TestEstimateContextWords_AlignedWithExpand 断言预估函数选出的上下文段集合与
// ExpandBatchWithContext 实际选出的非批内段集合一致（同源保证，含 skip/装饰/占位段）。
func TestEstimateContextWords_AlignedWithExpand(t *testing.T) {
	sources := []string{"ctx0", "◇ ◇", "ctx2", "batch", "ctx4", "[PH]", "ctx6"}
	doc := testDocWithSources(sources)
	doc.Segments[1].Skip = true
	doc.Segments[2].OriginalSource = "context two words"
	doc.Segments[5].Protected = map[string]string{"[PH]": "x"}

	batchIdxs := []int{3}
	ctxWindow := 3
	expanded := ExpandBatchWithContext(doc, batchIdxs, len(doc.Segments), ctxWindow, 0)

	batchSet := map[int]struct{}{3: {}}
	var actualCtxIdxs []int
	actualWords := 0
	for _, idx := range expanded.Idxs {
		if _, in := batchSet[idx]; in {
			continue
		}
		actualCtxIdxs = append(actualCtxIdxs, idx)
		src := doc.Segments[idx].OriginalSource
		if src == "" {
			src = doc.Segments[idx].Source
		}
		actualWords += CountWords(src)
	}

	est := estimateContextWords(doc, batchIdxs, ctxWindow)
	if est != actualWords {
		t.Errorf("estimator %d != actual expanded context words %d (ctx idxs %v)",
			est, actualWords, actualCtxIdxs)
	}
}

// TestEstimateContextWordsWithPrefix_MatchesReference 断言前缀和版预估与参照版（estimateContextWords）
// 在多种场景下结果一致，保证前缀和优化不改变语义。参照版直接复用 ExpandBatchWithContext，
// 故本测试通过传递性间接验证前缀和版与 ExpandBatchWithContext 同源。
func TestEstimateContextWordsWithPrefix_MatchesReference(t *testing.T) {
	sources := []string{"ctx0", "◇ ◇", "ctx2", "batch", "ctx4", "[PH]", "ctx6", "seven words here now"}
	doc := testDocWithSources(sources)
	doc.Segments[1].Skip = true
	doc.Segments[2].OriginalSource = "context two words here"
	doc.Segments[5].Protected = map[string]string{"[PH]": "x"}

	prefix := buildEligibleWordPrefix(doc)

	cases := []struct {
		name   string
		batch  []int
		window int
	}{
		{"single_mid_w1", []int{3}, 1},
		{"single_mid_w3", []int{3}, 3},
		{"single_edge_w5", []int{0}, 5},
		{"pair_w1", []int{1, 3}, 1},
		{"pair_w2", []int{3, 6}, 2},
		{"triple_w0", []int{0, 3, 7}, 0},
		{"empty_w1", []int{}, 1},
	}
	for _, tc := range cases {
		ref := estimateContextWords(doc, tc.batch, tc.window)
		got := estimateContextWordsWithPrefix(doc, tc.batch, tc.window, prefix)
		if ref != got {
			t.Errorf("%s: prefix %d != reference %d", tc.name, got, ref)
		}
	}
}

// TestSplitByConstraint_ContextWordsBudget pending 段短、上下文段长，断言计入上下文后切批更细。
func TestSplitByConstraint_ContextWordsBudget(t *testing.T) {
	sources := []string{
		"alpha beta gamma",   // 0 context (3 words)
		"a",                  // 1 pending
		"delta echo foxtrot", // 2 context (3 words)
		"b",                  // 3 pending
		"golf hotel india",   // 4 context (3 words)
	}
	doc := testDocWithSources(sources)
	ctxWindow := 1
	est := func(c []int) int { return estimateContextWords(doc, c, ctxWindow) }
	constraint := wordConstraint(7)

	got := splitByConstraintAndSpan(doc, []int{1, 3}, constraint, 0, est)
	want := [][]int{{1}, {3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("context budget: got %v want %v", got, want)
	}

	gotNil := splitByConstraintAndSpan(doc, []int{1, 3}, constraint, 0, nil)
	wantNil := [][]int{{1, 3}}
	if !reflect.DeepEqual(gotNil, wantNil) {
		t.Errorf("nil estimator baseline: got %v want %v", gotNil, wantNil)
	}
}

// TestSplitByConstraint_EstimatorNil_BackwardCompat estimator=nil 时行为与旧版一致。
func TestSplitByConstraint_EstimatorNil_BackwardCompat(t *testing.T) {
	sources := []string{"a", "b", "c", "d", "e"}
	doc := testDocWithSources(sources)
	constraint := wordConstraint(2)
	idxs := []int{0, 1, 2, 3, 4}

	got := splitByConstraintAndSpan(doc, idxs, constraint, 0, nil)
	want := splitByConstraint(doc, idxs, constraint)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nil estimator should match splitByConstraint: got %v want %v", got, want)
	}
	wantExplicit := [][]int{{0, 1}, {2, 3}, {4}}
	if !reflect.DeepEqual(got, wantExplicit) {
		t.Errorf("nil estimator: got %v want %v", got, wantExplicit)
	}
}

// TestExpandBatchWithContext_MaxCharsTruncates maxChars=5 对 10 字上下文段截断为 5 字+"…"。
func TestExpandBatchWithContext_MaxCharsTruncates(t *testing.T) {
	sources := []string{"0123456789", "batch", "abcdefghij"}
	doc := testDocWithSources(sources)
	expanded := ExpandBatchWithContext(doc, []int{1}, 3, 1, 5)

	if !reflect.DeepEqual(expanded.Idxs, []int{0, 1, 2}) {
		t.Errorf("idxs: got %v want [0 1 2]", expanded.Idxs)
	}
	if trunc, ok := expanded.TruncatedSrc[0]; !ok || trunc != "01234…" {
		t.Errorf("seg 0 trunc: got %q ok=%v want %q", trunc, ok, "01234…")
	}
	if trunc, ok := expanded.TruncatedSrc[2]; !ok || trunc != "abcde…" {
		t.Errorf("seg 2 trunc: got %q ok=%v want %q", trunc, ok, "abcde…")
	}
	if _, ok := expanded.TruncatedSrc[1]; ok {
		t.Errorf("batch seg 1 must not appear in TruncatedSrc")
	}
}

// TestExpandBatchWithContext_MaxCharsZero_NoTruncate maxChars=0 不截断。
func TestExpandBatchWithContext_MaxCharsZero_NoTruncate(t *testing.T) {
	sources := []string{"0123456789", "batch", "abcdefghij"}
	doc := testDocWithSources(sources)
	expanded := ExpandBatchWithContext(doc, []int{1}, 3, 1, 0)

	if !reflect.DeepEqual(expanded.Idxs, []int{0, 1, 2}) {
		t.Errorf("idxs: got %v want [0 1 2]", expanded.Idxs)
	}
	if len(expanded.TruncatedSrc) != 0 {
		t.Errorf("maxChars=0 should not truncate, got %v", expanded.TruncatedSrc)
	}
}

// TestExpandBatchWithContext_BatchSegmentNeverTruncated 批内段即使超 max_chars 也不截断。
func TestExpandBatchWithContext_BatchSegmentNeverTruncated(t *testing.T) {
	sources := []string{"contextlong1", "verylongbatchsegment", "contextlong2"}
	doc := testDocWithSources(sources)
	expanded := ExpandBatchWithContext(doc, []int{1}, 3, 1, 5)

	if _, ok := expanded.TruncatedSrc[1]; ok {
		t.Errorf("batch segment must never be truncated")
	}
	if _, ok := expanded.TruncatedSrc[0]; !ok {
		t.Errorf("context seg 0 should be truncated")
	}
	if _, ok := expanded.TruncatedSrc[2]; !ok {
		t.Errorf("context seg 2 should be truncated")
	}
}

// TestSplitByConstraint_PureSegmentMode_IgnoresContextBudget 纯行数模式（MaxWords=0）下，
// 即使上下文段超长，切批仍只按 MaxSegments 切、estimator 不被调用。
func TestSplitByConstraint_PureSegmentMode_IgnoresContextBudget(t *testing.T) {
	sources := []string{
		"alpha beta gamma delta",  // 0 context (4 words)
		"a",                       // 1 pending
		"echo foxtrot golf hotel", // 2 context (4 words)
		"b",                       // 3 pending
		"india juliet kilo lima",  // 4 context (4 words)
	}
	doc := testDocWithSources(sources)
	ctxWindow := 1
	calls := 0
	est := func(c []int) int { calls++; return estimateContextWords(doc, c, ctxWindow) }

	got := splitByConstraintAndSpan(doc, []int{1, 3}, segConstraint(1), 0, est)
	want := [][]int{{1}, {3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pure segment mode: got %v want %v", got, want)
	}
	if calls != 0 {
		t.Errorf("estimator must not be called in pure segment mode, got %d calls", calls)
	}
}

// TestSplitByConstraint_DualMode_SegmentsCountsPendingOnly 行+字模式：MaxSegments 只数 pending，
// 含上下文的批次仍按 pending 段数拆分（上下文不撑破段数上限）。
func TestSplitByConstraint_DualMode_SegmentsCountsPendingOnly(t *testing.T) {
	sources := []string{
		"ctx one two three",     // 0 context (4 words)
		"p1",                    // 1 pending
		"ctx four five six",     // 2 context (4 words)
		"p2",                    // 3 pending
		"ctx seven eight nine",  // 4 context (4 words)
		"p3",                    // 5 pending
		"ctx ten eleven twelve", // 6 context (4 words)
	}
	doc := testDocWithSources(sources)
	ctxWindow := 1
	est := func(c []int) int { return estimateContextWords(doc, c, ctxWindow) }
	constraint := dualConstraint(2, 1000) // MaxWords 很高，仅段数驱动

	got := splitByConstraintAndSpan(doc, []int{1, 3, 5}, constraint, 0, est)
	want := [][]int{{1, 3}, {5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dual mode pending-only segments: got %v want %v", got, want)
	}
}

// TestEstimateContextWords_IgnoresTranslateFlag 零星待译：Translate=false 的段仍被计入上下文预算
// （与 ExpandBatchWithContext 同源，不读 Translate）。
func TestEstimateContextWords_IgnoresTranslateFlag(t *testing.T) {
	sources := []string{"ctx one", "pending", "ctx two", "ctx three"}
	doc := testDocWithSources(sources)
	doc.Segments[0].Translate = false
	doc.Segments[2].Translate = false

	batchIdxs := []int{1}
	ctxWindow := 1
	est := estimateContextWords(doc, batchIdxs, ctxWindow)
	want := CountWords("ctx one") + CountWords("ctx two") // 2 + 2 = 4
	if est != want {
		t.Errorf("estimator should count Translate=false context segments: got %d want %d", est, want)
	}

	expanded := ExpandBatchWithContext(doc, batchIdxs, len(doc.Segments), ctxWindow, 0)
	if !reflect.DeepEqual(expanded.Idxs, []int{0, 1, 2}) {
		t.Errorf("Translate=false context segs must still be expanded: got %v want [0 1 2]", expanded.Idxs)
	}
}

// TestSplitByConstraint_SparsePending_NoContextEligible pending 两侧全是 Skip/占位/装饰段，
// estimator 返回 0、切批行为与无上下文一致。
func TestSplitByConstraint_SparsePending_NoContextEligible(t *testing.T) {
	sources := []string{"◇", "a", "[PH]", "b", "◇"}
	doc := testDocWithSources(sources)
	doc.Segments[2].Protected = map[string]string{"[PH]": "x"}
	ctxWindow := 1
	est := func(c []int) int { return estimateContextWords(doc, c, ctxWindow) }

	if est([]int{1}) != 0 || est([]int{1, 3}) != 0 {
		t.Errorf("estimator should return 0 when no eligible context")
	}

	constraint := wordConstraint(2)
	got := splitByConstraintAndSpan(doc, []int{1, 3}, constraint, 0, est)
	gotNil := splitByConstraintAndSpan(doc, []int{1, 3}, constraint, 0, nil)
	if !reflect.DeepEqual(got, gotNil) {
		t.Errorf("no eligible context should match nil estimator: got %v nil %v", got, gotNil)
	}
	want := [][]int{{1, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sparse no-context: got %v want %v", got, want)
	}
}

// TestSplitByConstraint_SparsePending_PhysicalLimit 单段 pending + ctxWindow=2 + 紧 max_words，
// [e] 独占一批（无法再拆，物理边界），max_words 被突破属预期。
func TestSplitByConstraint_SparsePending_PhysicalLimit(t *testing.T) {
	five := "one two three four five" // 5 words
	sources := []string{five, five, five, five, five}
	doc := testDocWithSources(sources)
	ctxWindow := 2
	est := func(c []int) int { return estimateContextWords(doc, c, ctxWindow) }
	constraint := wordConstraint(10) // < 25 (5 pending + 4×5 context)

	got := splitByConstraintAndSpan(doc, []int{2}, constraint, 0, est)
	want := [][]int{{2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("physical limit: single pending must occupy its own batch, got %v want %v", got, want)
	}
}
