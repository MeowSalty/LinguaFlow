package ruby

import (
	"reflect"
	"strconv"
	"testing"
)

// TestExtract_ItemsWithIDs 验证提取阶段：
//   - items 为 []Item，ID 按顺序分配 "1".."N"，SourceBase/SourceText 正确
//   - 剥离后的文本仅保留基底
//
// 使用 "、" 分隔每对 ruby，避免相邻 per-kanji 被 mergeAdjacentRuby 合并，
// 从而产出 6 个独立 item。
func TestExtract_ItemsWithIDs(t *testing.T) {
	source := "<ruby>我<rt>wǒ</rt></ruby>、<ruby>想<rt>xiǎng</rt></ruby>、<ruby>要<rt>yào</rt></ruby>、<ruby>一<rt>yī</rt></ruby>、<ruby>杯<rt>bēi</rt></ruby>、<ruby>水<rt>shuǐ</rt></ruby>"

	items, stripped := Extract(source)

	if want := "我、想、要、一、杯、水"; stripped != want {
		t.Errorf("stripped = %q, want %q", stripped, want)
	}
	if len(items) != 6 {
		t.Fatalf("len(items) = %d, want 6", len(items))
	}

	wantBases := []string{"我", "想", "要", "一", "杯", "水"}
	wantTexts := []string{"wǒ", "xiǎng", "yào", "yī", "bēi", "shuǐ"}
	for i, it := range items {
		if want := strconv.Itoa(i + 1); it.ID != want {
			t.Errorf("items[%d].ID = %q, want %q", i, it.ID, want)
		}
		if it.SourceBase != wantBases[i] || it.SourceText != wantTexts[i] {
			t.Errorf("items[%d] = %+v, want base %q text %q", i, it, wantBases[i], wantTexts[i])
		}
		if it.Aligned {
			t.Errorf("items[%d].Aligned = true, want false", i)
		}
	}
}

// TestExtract_NoRuby 验证无 ruby 元素时文本原样返回、items 为 nil。
func TestExtract_NoRuby(t *testing.T) {
	source := "plain text 无注音"
	items, stripped := Extract(source)
	if stripped != source {
		t.Errorf("stripped = %q, want 原样 %q", stripped, source)
	}
	if items != nil {
		t.Errorf("items = %v, want nil", items)
	}
}

// TestStripRubyTagsCleansAuxTags 验证 StripRubyTags 清理 base 与 trailing 中的
// 辅助标签（<rp>/<rb> 仅删标签本身，其回退文本内容保留——与历史行为一致）。
func TestStripRubyTagsCleansAuxTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"single ruby", "<ruby>呪<rt>じゅ</rt></ruby>", "呪"},
		{"ruby with trailing text", "<ruby>呪<rt>じゅ</rt></ruby>術", "呪術"},
		{"multiple ruby", "<ruby>呪<rt>じゅ</rt></ruby><ruby>術<rt>じゅつ</rt></ruby>", "呪術"},
		{"no ruby", "呪術廻戦", "呪術廻戦"},
		{"empty", "", ""},
		// <rp> 提供不支持 ruby 的浏览器的回退文本（括号）：
		// 标签本身被清理，回退文本内容保留（base 与 trailing 两侧同理）。
		{"rp tags in base and trailing", "<ruby>漢<rp>(</rp><rt>かん</rt><rp>)</rp></ruby>字", "漢()字"},
		{"rb tag inside base", "<ruby><rb>漢</rb><rt>かん</rt></ruby>", "漢"},
		// 多段 <rt>：全部注音跨度删除，基底逐字保留（与相邻独立元素形式一致）。
		{"multiple rt", "<ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>こんなにも", "何故こんなにも"},
		// 多段 rt 中 <rp> 回退文本保留（每对注音两侧的括号均来自 <rp> 回退文本）。
		{"multiple rt with rp", "<ruby>漢<rp>(</rp><rt>かん</rt><rp>)</rp>字<rp>(</rp><rt>じ</rt><rp>)</rp></ruby>", "漢()字()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripRubyTags(tt.in); got != tt.want {
				t.Errorf("StripRubyTags(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestMergeAdjacentRuby_WordLevel 验证相邻 per-kanji ruby 合并为词级注音：
// 提取产物为单条 item（基底/标注均为拼接结果）。
func TestMergeAdjacentRuby_WordLevel(t *testing.T) {
	items, stripped := Extract("<ruby>微<rt>ほほ</rt></ruby><ruby>笑<rt>え</rt></ruby>")
	if want := "微笑"; stripped != want {
		t.Errorf("stripped = %q, want %q", stripped, want)
	}
	want := []Item{{ID: "1", SourceBase: "微笑", SourceText: "ほほえ"}}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}
}

// TestExtract_MultipleRT 多段 <rt> 的 ruby 元素（HTML 规范允许、日文 EPUB 常见）
// 逐对产出且相邻对合并为词级，语义与等价的相邻独立元素完全一致：
// 注音不再以正文身份泄漏进剥离后文本，每对注音都进入 items。
func TestExtract_MultipleRT(t *testing.T) {
	items, stripped := Extract("<ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>こんなにも")
	if want := "何故こんなにも"; stripped != want {
		t.Errorf("stripped = %q, want %q", stripped, want)
	}
	want := []Item{{ID: "1", SourceBase: "何故", SourceText: "なぜ"}}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}
}

// 多段 rt 与 <rb> 显式基底混合：每对取各自 <rb> 内的基底，合并为词级。
func TestExtract_MultipleRTWithRB(t *testing.T) {
	items, stripped := Extract("<ruby><rb>漢</rb><rt>かん</rt><rb>字</rb><rt>じ</rt></ruby>")
	if want := "漢字"; stripped != want {
		t.Errorf("stripped = %q, want %q", stripped, want)
	}
	want := []Item{{ID: "1", SourceBase: "漢字", SourceText: "かんじ"}}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}
}

// 多段 rt 但基底非汉字（送り仮名「み」）：不合并，逐对独立产出。
func TestExtract_MultipleRTNoMerge(t *testing.T) {
	items, stripped := Extract("<ruby>笑<rt>え</rt>み<rt>み</rt></ruby>")
	if want := "笑み"; stripped != want {
		t.Errorf("stripped = %q, want %q", stripped, want)
	}
	want := []Item{
		{ID: "1", SourceBase: "笑", SourceText: "え"},
		{ID: "2", SourceBase: "み", SourceText: "み"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}
}

// 多段 rt 元素与其后的独立元素连续合并为一条词级注音。
func TestExtract_MultipleRTCrossElementMerge(t *testing.T) {
	items, stripped := Extract("<ruby>漢<rt>かん</rt>字<rt>じ</rt></ruby><ruby>語<rt>ご</rt></ruby>")
	if want := "漢字語"; stripped != want {
		t.Errorf("stripped = %q, want %q", stripped, want)
	}
	want := []Item{{ID: "1", SourceBase: "漢字語", SourceText: "かんじご"}}
	if !reflect.DeepEqual(items, want) {
		t.Errorf("items = %+v, want %+v", items, want)
	}
}

// 畸形元素（rt 未闭合、无 rt）不产出条目，文本原样保留（含标签）。
func TestExtract_MalformedKeptAsIs(t *testing.T) {
	for _, source := range []string{"<ruby>漢<rt>かん</ruby>", "<ruby>漢</ruby>"} {
		items, stripped := Extract(source)
		if items != nil {
			t.Errorf("Extract(%q) items = %v, want nil", source, items)
		}
		if stripped != source {
			t.Errorf("Extract(%q) stripped = %q, want 原样", source, stripped)
		}
	}
}

// ElementSpans 返回含完整 rt 对的元素跨度；无 rt 元素与普通文本不返回。
func TestElementSpans(t *testing.T) {
	// 多段 rt 元素：单个完整跨度
	source := "<ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>こんなにも"
	got := ElementSpans(source)
	want := [][2]int{{0, len(source) - len("こんなにも")}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ElementSpans = %v, want %v", got, want)
	}

	// 无 rt 对元素与普通文本：不返回
	for _, s := range []string{"<ruby>漢</ruby>", "plain text", "<ruby>漢<rt>かん</ruby>", ""} {
		if got := ElementSpans(s); got != nil {
			t.Errorf("ElementSpans(%q) = %v, want nil", s, got)
		}
	}

	// 多个元素：升序、不重叠
	el1 := "<ruby>呪<rt>じゅ</rt></ruby>"
	el2 := "<ruby>術<rt>じゅつ</rt></ruby>"
	got = ElementSpans(el1 + el2)
	want = [][2]int{{0, len(el1)}, {len(el1), len(el1) + len(el2)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ElementSpans = %v, want %v", got, want)
	}
}
