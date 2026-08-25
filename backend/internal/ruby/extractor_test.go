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
