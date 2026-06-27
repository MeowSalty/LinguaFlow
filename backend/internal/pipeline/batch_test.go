package pipeline

import (
	"reflect"
	"testing"
)

func TestBuildContextAwareBatches_Disabled(t *testing.T) {
	got := BuildContextAwareBatches([]int{1, 3, 7}, 5, 1, false)
	want := BuildContinuousPendingBatches([]int{1, 3, 7}, 5)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("disabled should fall back to continuous, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_WindowZero(t *testing.T) {
	got := BuildContextAwareBatches([]int{1, 3, 7}, 5, 0, true)
	want := BuildContinuousPendingBatches([]int{1, 3, 7}, 5)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=0 should fall back to continuous, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_NoOverlap(t *testing.T) {
	// 1 和 7 的窗口 [0,2] 和 [6,8] 不重叠
	got := BuildContextAwareBatches([]int{1, 7}, 10, 1, true)
	want := [][]int{{1}, {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non-overlapping should be separate batches, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Overlap(t *testing.T) {
	// 1 的窗口 [0,2]，3 的窗口 [2,4]，重叠 → 合并
	got := BuildContextAwareBatches([]int{1, 3}, 10, 1, true)
	want := [][]int{{1, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overlapping should merge, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Window2_Merge(t *testing.T) {
	// 1 的窗口 [-1,3]，5 的窗口 [3,7]，重叠 → 合并
	got := BuildContextAwareBatches([]int{1, 5}, 10, 2, true)
	want := [][]int{{1, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=2 should merge 1 and 5, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Window2_Separate(t *testing.T) {
	// 1 的窗口 [-1,3]，7 的窗口 [5,9]，不重叠
	got := BuildContextAwareBatches([]int{1, 7}, 10, 2, true)
	want := [][]int{{1}, {7}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("window=2 should separate 1 and 7, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_BatchSizeSplit(t *testing.T) {
	// 合并后 [1,3,5,7,9]，batchSize=2
	got := BuildContextAwareBatches([]int{1, 3, 5, 7, 9}, 2, 1, true)
	want := [][]int{{1, 3}, {5, 7}, {9}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("batch size splitting failed, got %v want %v", got, want)
	}
}

func TestBuildContextAwareBatches_Empty(t *testing.T) {
	got := BuildContextAwareBatches(nil, 5, 1, true)
	if got != nil {
		t.Errorf("empty input should return nil, got %v", got)
	}
}
