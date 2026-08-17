package engine

import (
	"sort"
	"testing"
)

// TestNewResolvedByMode_ContainsAllNonTranslateModes 验证 helper 初始化的模式集
// 覆盖所有参与跨轮增量的非翻译模式，且 map 初始化为可写的空集合。
// 新增 handler 模式时若漏改 crossRoundResolvedModes，本测试会失败。
func TestNewResolvedByMode_ContainsAllNonTranslateModes(t *testing.T) {
	m := NewResolvedByMode()
	wantModes := []string{"extract", "adjudicate", "semantic_qa", "correct"}
	if len(m) != len(wantModes) {
		t.Fatalf("mode count=%d want %d (modes=%v)", len(m), len(wantModes), m)
	}
	for _, mode := range wantModes {
		set, ok := m[mode]
		if !ok {
			t.Fatalf("模式 %q 缺失", mode)
		}
		if set == nil {
			t.Fatalf("模式 %q 的集合为 nil，应为可写空 map", mode)
		}
		if len(set) != 0 {
			t.Fatalf("模式 %q 初始集合非空：%v", mode, set)
		}
	}
	// translate 不应在 map 中（由 DB status 驱动，不参与跨轮 in-memory 增量）。
	if _, ok := m["translate"]; ok {
		t.Fatalf("translate 不应在 resolvedByMode 中（由 DB status 驱动增量）")
	}
}

// TestAccumulateResolved_AddsToCorrectMode 验证成功段累加到对应模式的集合。
func TestAccumulateResolved_AddsToCorrectMode(t *testing.T) {
	m := NewResolvedByMode()
	AccumulateResolved(m, "extract", []int{0, 2, 4})
	AccumulateResolved(m, "adjudicate", []int{1, 3})

	if got := sortedKeys(m["extract"]); !equalInts(got, []int{0, 2, 4}) {
		t.Fatalf("extract resolved=%v want [0 2 4]", got)
	}
	if got := sortedKeys(m["adjudicate"]); !equalInts(got, []int{1, 3}) {
		t.Fatalf("adjudicate resolved=%v want [1 3]", got)
	}
	// 不同模式间不共享：extract 的段不影响 semantic_qa。
	if len(m["semantic_qa"]) != 0 {
		t.Fatalf("semantic_qa 应为空，实际=%v", m["semantic_qa"])
	}
}

// TestAccumulateResolved_Idempotent 验证重复累加同一索引不产生重复（map 语义）。
func TestAccumulateResolved_Idempotent(t *testing.T) {
	m := NewResolvedByMode()
	AccumulateResolved(m, "extract", []int{1, 1, 2, 2, 1})
	if got := sortedKeys(m["extract"]); !equalInts(got, []int{1, 2}) {
		t.Fatalf("重复累加后 resolved=%v want [1 2]", got)
	}
}

// TestAccumulateResolved_UnknownModeIgnored 验证未知模式（如 translate）被忽略，
// 不 panic、不污染 map（write to nil map 会被 ok-guard 拦截）。
func TestAccumulateResolved_UnknownModeIgnored(t *testing.T) {
	m := NewResolvedByMode()
	// translate 不在 map 中，应被安全忽略。
	AccumulateResolved(m, "translate", []int{0, 1, 2})
	AccumulateResolved(m, "nonexistent", []int{9})
	// map 内容不变。
	for mode, set := range m {
		if len(set) != 0 {
			t.Fatalf("模式 %q 的集合应保持空，实际=%v", mode, set)
		}
	}
}

// TestAccumulateResolved_NilResolvedSlice 验证空 Resolved 切片是 no-op。
func TestAccumulateResolved_NilResolvedSlice(t *testing.T) {
	m := NewResolvedByMode()
	AccumulateResolved(m, "extract", nil)
	AccumulateResolved(m, "extract", []int{})
	if len(m["extract"]) != 0 {
		t.Fatalf("空 Resolved 累加后集合应仍为空，实际=%v", m["extract"])
	}
}

func sortedKeys(m map[int]struct{}) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
