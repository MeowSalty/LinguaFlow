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
	got := BuildContextAwareBatches(doc, []int{1, 3, 7}, constraint, 1, false)
	want := BuildContinuousPendingBatches(doc, []int{1, 3, 7}, constraint)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("disabled should fall back to continuous, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_WindowZero(t *testing.T) {
	doc := testDoc(10)
	constraint := segConstraint(5)
	got := BuildContextAwareBatches(doc, []int{1, 3, 7}, constraint, 0, true)
	want := BuildContinuousPendingBatches(doc, []int{1, 3, 7}, constraint)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=0 should fall back to continuous, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_NoOverlap(t *testing.T) {
	doc := testDoc(10)
	// 1 和 7 的窗口 [0,2] 和 [6,8] 不重叠
	got := BuildContextAwareBatches(doc, []int{1, 7}, segConstraint(10), 1, true)
	want := [][]int{{1}, {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non-overlapping should be separate batches, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Overlap(t *testing.T) {
	doc := testDoc(10)
	// 1 的窗口 [0,2]，3 的窗口 [2,4]，重叠 → 合并
	got := BuildContextAwareBatches(doc, []int{1, 3}, segConstraint(10), 1, true)
	want := [][]int{{1, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overlapping should merge, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Window2_Merge(t *testing.T) {
	doc := testDoc(10)
	// 1 的窗口 [-1,3]，5 的窗口 [3,7]，重叠 → 合并
	got := BuildContextAwareBatches(doc, []int{1, 5}, segConstraint(10), 2, true)
	want := [][]int{{1, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=2 should merge 1 and 5, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Window2_Separate(t *testing.T) {
	doc := testDoc(10)
	// 1 的窗口 [-1,3]，7 的窗口 [5,9]，不重叠
	got := BuildContextAwareBatches(doc, []int{1, 7}, segConstraint(10), 2, true)
	want := [][]int{{1}, {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=2 should separate 1 and 7, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_BatchSizeSplit(t *testing.T) {
	doc := testDoc(10)
	// 合并后 [1,3,5,7,9]，batchSize=2
	got := BuildContextAwareBatches(doc, []int{1, 3, 5, 7, 9}, segConstraint(2), 1, true)
	want := [][]int{{1, 3}, {5, 7}, {9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("batch size splitting failed, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Empty(t *testing.T) {
	doc := testDoc(1)
	got := BuildContextAwareBatches(doc, nil, segConstraint(5), 1, true)
	if got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}

func TestBuildContextAwareBatches_WordConstraint(t *testing.T) {
	sources := []string{"a", "bb", "ccc", "dddd", "eeeee"}
	doc := testDocWithSources(sources)
	// MaxWords=4: "a"(1) + "bb"(1) + "ccc"(1) = 3 words → 继续; + "dddd"(1) = 4 → 继续; + "eeeee"(1) = 5 > 4 → 切
	got := BuildContextAwareBatches(doc, []int{0, 1, 2, 3, 4}, wordConstraint(4), 0, false)
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
	got := BuildContextAwareBatches(doc, []int{0, 1, 2}, wordConstraint(5), 0, false)
	want := [][]int{{0, 1}, {2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CJK word constraint: got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_DualConstraint_SegmentsFirst(t *testing.T) {
	sources := []string{"a", "b", "c", "d", "e"}
	doc := testDocWithSources(sources)
	// MaxSegments=2, MaxWords=100 → segments limit hits first
	got := BuildContextAwareBatches(doc, []int{0, 1, 2, 3, 4}, dualConstraint(2, 100), 0, false)
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
	got := BuildContextAwareBatches(doc, []int{0, 1, 2, 3}, dualConstraint(100, 3), 0, false)
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
