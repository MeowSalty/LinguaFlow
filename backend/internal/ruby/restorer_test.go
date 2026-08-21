package ruby

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

func TestAssignIDs(t *testing.T) {
	items := []Item{
		{SourceBase: "我"},
		{ID: "9", SourceBase: "想"},
		{SourceBase: "要"},
	}
	got := AssignIDs(items)
	if &got[0] != &items[0] {
		t.Error("AssignIDs 应返回同一切片")
	}
	wants := []string{"1", "9", "2"}
	for i, want := range wants {
		if items[i].ID != want {
			t.Errorf("items[%d].ID = %q, want %q", i, items[i].ID, want)
		}
	}
}

func TestItemsByID(t *testing.T) {
	items := []Item{
		{ID: "1", SourceBase: "我"},
		{ID: "2", SourceBase: "想"},
		{SourceBase: "要"}, // 空 ID → 以 "" 为键
	}
	m := ItemsByID(items)
	if len(m) != 3 {
		t.Fatalf("len(ItemsByID) = %d, want 3", len(m))
	}
	if m["1"] != &items[0] {
		t.Error(`ItemsByID["1"] 应指向 items[0]`)
	}
	if m["2"] != &items[1] {
		t.Error(`ItemsByID["2"] 应指向 items[1]`)
	}
	if m[""] != &items[2] {
		t.Error(`ItemsByID[""] 应指向 items[2]（空 ID 条目）`)
	}
}

func TestMergeByOutput_IDMatch(t *testing.T) {
	items := []Item{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	output := []OutputEntry{
		{Base: "I", Text: "aɪ", Kind: "phonetic", ID: "1"},
		{Base: "want", Text: "wɒnt", Kind: "phonetic", ID: "2"},
		{Base: "water", Text: "wɔːtər", Kind: "phonetic", ID: "3"},
	}
	MergeByOutput(items, output)

	wantTargets := []string{"I", "want", "water"}
	wantTexts := []string{"aɪ", "wɒnt", "wɔːtər"}
	for i := range items {
		if !items[i].Aligned {
			t.Errorf("items[%d].Aligned = false, want true", i)
		}
		if items[i].TargetBase != wantTargets[i] || items[i].TargetText != wantTexts[i] {
			t.Errorf("items[%d] = %+v, want base %q text %q", i, items[i], wantTargets[i], wantTexts[i])
		}
		if items[i].Kind != "phonetic" {
			t.Errorf("items[%d].Kind = %q, want phonetic", i, items[i].Kind)
		}
	}
}

// TestMergeByOutput_RestructureNoMismatch 是核心场景：
// 6 条原文注音（我/想/要/一/杯/水）→ 3 条译文条目（结构变化），
// 旧路径按位置会把 output[0]("I") 错配到 item "1" 之外的条目；
// 按 ID 关联后，item "1" 必须拿到 ID "1" 的条目，绝不取 output[0]。
func TestMergeByOutput_RestructureNoMismatch(t *testing.T) {
	items := []Item{
		{ID: "1", SourceBase: "我"},
		{ID: "2", SourceBase: "想"},
		{ID: "3", SourceBase: "要"},
		{ID: "4", SourceBase: "一"},
		{ID: "5", SourceBase: "杯"},
		{ID: "6", SourceBase: "水"},
	}
	output := []OutputEntry{
		{Base: "I", Text: "aɪ", Kind: "phonetic", ID: "1"},
		{Base: "want", Text: "wɒnt", Kind: "phonetic", ID: "3"},
		{Base: "water", Text: "wɔːtər", Kind: "phonetic", ID: "5"},
	}
	MergeByOutput(items, output)

	// ID 显式关联：item "1" 拿到 ID "1" 的条目（I），而非位置 output[0]
	if items[0].TargetBase != "I" || !items[0].Aligned {
		t.Errorf("items[0] = %+v, want TargetBase I aligned", items[0])
	}
	if items[2].TargetBase != "want" || !items[2].Aligned {
		t.Errorf("items[2] = %+v, want TargetBase want aligned", items[2])
	}
	if items[4].TargetBase != "water" || !items[4].Aligned {
		t.Errorf("items[4] = %+v, want TargetBase water aligned", items[4])
	}
	// 无对应 ID 的条目保持未对齐
	for _, i := range []int{1, 3, 5} {
		if items[i].Aligned {
			t.Errorf("items[%d] = %+v, want unaligned", i, items[i])
		}
		if items[i].TargetBase != "" || items[i].TargetText != "" {
			t.Errorf("items[%d] target 字段应为空: %+v", i, items[i])
		}
	}
}

func TestMergeByOutput_PositionalFallback(t *testing.T) {
	items := []Item{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	// 全部条目无 ID → 按位置依次对齐
	output := []OutputEntry{
		{Base: "I", Text: "aɪ", Kind: "phonetic"},
		{Base: "want", Text: "wɒnt", Kind: "phonetic"},
		{Base: "water", Text: "wɔːtər", Kind: "phonetic"},
	}
	MergeByOutput(items, output)

	wantBases := []string{"I", "want", "water"}
	for i, want := range wantBases {
		if !items[i].Aligned || items[i].TargetBase != want {
			t.Errorf("items[%d] = %+v, want aligned base %q", i, items[i], want)
		}
	}
}

// TestMergeByOutput_UnknownDiscarded_DupFirstWins 验证失败模式契约
// （计划 .kilo/plans/1786765987113-ruby-unified-item-alignment.md:115-116）：
//   - 未知 ID（不在集合内）丢弃，不消费任何 item，相关 item 保持未对齐
//   - 重复 ID：首条生效，余者丢弃，不抢占其它 item
//   - 无空闲 item 时条目被丢弃，不 panic
func TestMergeByOutput_UnknownDiscarded_DupFirstWins(t *testing.T) {
	items := []Item{{ID: "1"}, {ID: "2"}, {ID: "3"}}
	output := []OutputEntry{
		{Base: "first", Text: "f", Kind: "phonetic", ID: "1"}, // ID 匹配 → item 1
		{Base: "dup", Text: "d", Kind: "phonetic", ID: "1"},   // 重复 ID → 丢弃（不消费 item 2）
		{Base: "x", Text: "x", Kind: "phonetic", ID: "99"},    // 未知 ID → 丢弃（不消费 item 3）
		{Base: "w", Text: "w", Kind: "phonetic", ID: "2"},     // ID 匹配 → item 2
	}
	MergeByOutput(items, output)

	if items[0].TargetBase != "first" || !items[0].Aligned {
		t.Errorf("items[0] = %+v, want first aligned（重复 ID 首条生效）", items[0])
	}
	if items[1].TargetBase != "w" || !items[1].Aligned {
		t.Errorf("items[1] = %+v, want w aligned（重复/未知 id 不抢占，id \"2\" 正常命中）", items[1])
	}
	// item 3：未知 id "99" 与重复 id "1" 都被丢弃，不再位置回退消费它 → 保持未对齐
	if items[2].Aligned || items[2].TargetBase != "" || items[2].TargetText != "" {
		t.Errorf("items[2] = %+v, want 纯未对齐（未知/重复 id 不消费 item）", items[2])
	}

	// 无空闲 item：重复条目被丢弃，不 panic
	one := []Item{{ID: "1"}}
	MergeByOutput(one, []OutputEntry{
		{Base: "a", Text: "a", ID: "1"},
		{Base: "b", Text: "b", ID: "1"},
	})
	if one[0].TargetBase != "a" {
		t.Errorf("one[0].TargetBase = %q, want a（首条生效）", one[0].TargetBase)
	}
}

// TestMergeByOutput_IDWhitespaceTrimmed 验证非空但纯空白的 ID 按"无 ID"处理：
// 走位置回退，避免 JSON 路径 id 带 blank 被误判为未知 id 而丢弃合法条目。
func TestMergeByOutput_IDWhitespaceTrimmed(t *testing.T) {
	items := []Item{{ID: "1"}, {ID: "2"}}
	output := []OutputEntry{
		{Base: "I", Text: "aɪ", Kind: "phonetic", ID: " 1 "}, // trim 后 "1" → 命中 item 1
		{Base: "w", Text: "w", Kind: "phonetic", ID: "   "},  // trim 后 "" → 位置回退 → item 2
	}
	MergeByOutput(items, output)
	if !items[0].Aligned || items[0].TargetBase != "I" {
		t.Errorf("items[0] = %+v, want I aligned（id 含空白应 trim 后匹配）", items[0])
	}
	if !items[1].Aligned || items[1].TargetBase != "w" {
		t.Errorf("items[1] = %+v, want w aligned（纯空白 id 走位置回退）", items[1])
	}
}

// TestMergeByOutput_EmptyBaseIgnored 验证 Base 为空的条目不参与任何匹配：
// 即使其 ID 命中了某个 item，该 item 也不会被对齐。
func TestMergeByOutput_EmptyBaseIgnored(t *testing.T) {
	items := []Item{{ID: "1"}, {ID: "2"}}
	output := []OutputEntry{
		{Base: "", Text: "x", Kind: "phonetic", ID: "2"},
		{Base: "I", Text: "aɪ", Kind: "phonetic", ID: "1"},
	}
	MergeByOutput(items, output)
	if items[1].Aligned {
		t.Errorf("items[1] 不应因空 base 条目对齐: %+v", items[1])
	}
	if !items[0].Aligned || items[0].TargetBase != "I" {
		t.Errorf("items[0] = %+v, want aligned base I", items[0])
	}
}

func TestUnaligned(t *testing.T) {
	items := []Item{
		{ID: "1", Aligned: true},
		{ID: "2"},
		{ID: "3", Aligned: true},
		{ID: "4"},
	}
	u := Unaligned(items)
	if len(u) != 2 || u[0].ID != "2" || u[1].ID != "4" {
		t.Fatalf("Unaligned = %+v, want items 2 and 4", u)
	}
}

// TestRestoreItems_UsesOwnSourceBaseFallback 是核心修复场景：
// 译文不含 TargetBase 但包含条目自身的 SourceBase → 用自身 SourceBase
// 回退定位插入（而非旧路径按位置的 originals[i]）。
func TestRestoreItems_UsesOwnSourceBaseFallback(t *testing.T) {
	r := &Restorer{}

	seg := &model.Segment{Target: "我喝水。"}
	items := []Item{
		{ID: "1", SourceBase: "我", SourceText: "wǒ", TargetBase: "I", TargetText: "aɪ", Kind: "phonetic"},
		{ID: "2", SourceBase: "杯", SourceText: "bēi", TargetBase: "cup", TargetText: "kʌp", Kind: "phonetic"},
	}
	res, err := r.RestoreItems(seg, items)
	if err != nil {
		t.Fatalf("RestoreItems error: %v", err)
	}
	if res.Matched != 1 {
		t.Errorf("Matched = %d, want 1", res.Matched)
	}
	if res.Total != 2 {
		t.Errorf("Total = %d, want 2", res.Total)
	}
	// 核心断言：插入用的是自身 SourceBase（我）+ TargetText（aɪ）
	if want := "<ruby>我<rt>aɪ</rt></ruby>喝水。"; seg.Target != want {
		t.Errorf("seg.Target = %q, want %q", seg.Target, want)
	}
	if !items[0].Aligned {
		t.Error("items[0].Aligned = false, want true（SourceBase 回退成功）")
	}
	if items[1].Aligned {
		t.Error("items[1].Aligned = true, want false（TargetBase 与 SourceBase 都不在译文中）")
	}

	// TargetBase 命中时优先用 TargetBase（不触发回退）
	seg2 := &model.Segment{Target: "I want water"}
	items2 := []Item{
		{ID: "1", SourceBase: "我", SourceText: "wǒ", TargetBase: "I", TargetText: "aɪ", Kind: "phonetic"},
	}
	res2, err := r.RestoreItems(seg2, items2)
	if err != nil {
		t.Fatalf("RestoreItems error: %v", err)
	}
	if res2.Matched != 1 {
		t.Errorf("Matched = %d, want 1", res2.Matched)
	}
	if want := "<ruby>I<rt>aɪ</rt></ruby> want water"; seg2.Target != want {
		t.Errorf("seg2.Target = %q, want %q", seg2.Target, want)
	}
	if !items2[0].Aligned {
		t.Error("items2[0].Aligned = false, want true（TargetBase 命中）")
	}
}

// TestRestoreItems_EmptyInput 验证空译文/空 items 返回零值结果。
func TestRestoreItems_EmptyInput(t *testing.T) {
	r := &Restorer{}
	res, err := r.RestoreItems(&model.Segment{}, nil)
	if err != nil || res != (RestoreResult{}) {
		t.Errorf("空输入 = %+v, %v; want RestoreResult{}", res, err)
	}
	res, err = r.RestoreItems(&model.Segment{Target: ""}, []Item{{ID: "1", TargetBase: "I"}})
	if err != nil || res != (RestoreResult{}) {
		t.Errorf("空译文 = %+v, %v; want RestoreResult{}", res, err)
	}
}

func TestParseSectionRubyOutput_FourField(t *testing.T) {
	lines := []string{
		"1: I | aɪ | phonetic | 1",
		// base 中包含 | 时仍按最右侧解析
		"2: a|b | c | phonetic | 2",
	}
	m := ParseSectionRubyOutput(lines)

	want := OutputEntry{Base: "I", Text: "aɪ", Kind: "phonetic", ID: "1"}
	if got := m["1"]; !reflect.DeepEqual(got, []OutputEntry{want}) {
		t.Errorf(`m["1"] = %+v, want %+v`, got, want)
	}

	want2 := OutputEntry{Base: "a|b", Text: "c", Kind: "phonetic", ID: "2"}
	if got := m["2"]; !reflect.DeepEqual(got, []OutputEntry{want2}) {
		t.Errorf(`m["2"] = %+v, want %+v`, got, want2)
	}
}

func TestParseSectionRubyOutput_ThreeField(t *testing.T) {
	lines := []string{
		"1: I | aɪ | phonetic",
		// 3 字段行中 base 包含 | 仍可解析
		"2: a|b | c | phonetic",
	}
	m := ParseSectionRubyOutput(lines)

	want := OutputEntry{Base: "I", Text: "aɪ", Kind: "phonetic", ID: ""}
	if got := m["1"]; !reflect.DeepEqual(got, []OutputEntry{want}) {
		t.Errorf(`m["1"] = %+v, want %+v`, got, want)
	}

	want2 := OutputEntry{Base: "a|b", Text: "c", Kind: "phonetic", ID: ""}
	if got := m["2"]; !reflect.DeepEqual(got, []OutputEntry{want2}) {
		t.Errorf(`m["2"] = %+v, want %+v`, got, want2)
	}
}

// section 模式以 "基底/标注#id" 展示条目，system prompt 要求 LLM 原样回显 "#id"。
// item ID 由 AssignIDs 分配为裸数字，解析阶段必须归一化掉 "#" 前缀，
// 否则 MergeByOutput 用裸数字建索引、用 "#1" 查找会全部漏配 → 注音丢失。
func TestParseSectionRubyOutput_HashPrefixID(t *testing.T) {
	lines := []string{
		"1: 雷神皇 | 雷神大人 | creative | #1",
		"2: 戳 | たた | phonetic | #2",
	}
	m := ParseSectionRubyOutput(lines)

	want := OutputEntry{Base: "雷神皇", Text: "雷神大人", Kind: "creative", ID: "1"}
	if got := m["1"]; !reflect.DeepEqual(got, []OutputEntry{want}) {
		t.Errorf(`m["1"] = %+v, want %+v`, got, want)
	}

	want2 := OutputEntry{Base: "戳", Text: "たた", Kind: "phonetic", ID: "2"}
	if got := m["2"]; !reflect.DeepEqual(got, []OutputEntry{want2}) {
		t.Errorf(`m["2"] = %+v, want %+v`, got, want2)
	}
}

// 归一化后，带 "#" 前缀的 LLM 输出能正确匹配 AssignIDs 分配的裸数字 item ID。
func TestMergeByOutput_HashPrefixID(t *testing.T) {
	items := []Item{
		{ID: "1", SourceBase: "雷神皇", SourceText: "カミナリサマ"},
		{ID: "2", SourceBase: "戳", SourceText: "たた"},
	}
	out := ParseSectionRubyOutput([]string{
		"1: 雷神皇 | 雷神大人 | creative | #1",
		"1: 戳 | たた | phonetic | #2",
	})
	MergeByOutput(items, out["1"])

	for _, it := range items {
		if !it.Aligned {
			t.Errorf("item ID=%q 未对齐，期望对齐（# 前缀归一化后应命中）", it.ID)
		}
	}
}
