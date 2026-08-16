package ruby

import (
	"strconv"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// TestProtect_WritesRubyItemsWithIDs 验证 Protect 阶段：
//   - ruby_annotations 仍以 []Annotation 写入（管线依赖的类型不变）
//   - ruby_items 为 []Item，ID 按顺序分配 "1".."N"，SourceBase/SourceText 正确
//   - 源文本中的 ruby 标签被剥离
//
// 使用 "、" 分隔每对 ruby，避免相邻 per-kanji 被 mergeAdjacentRuby 合并，
// 从而产出 6 个独立 item。
func TestProtect_WritesRubyItemsWithIDs(t *testing.T) {
	source := "<ruby>我<rt>wǒ</rt></ruby>、<ruby>想<rt>xiǎng</rt></ruby>、<ruby>要<rt>yào</rt></ruby>、<ruby>一<rt>yī</rt></ruby>、<ruby>杯<rt>bēi</rt></ruby>、<ruby>水<rt>shuǐ</rt></ruby>"
	seg := &model.Segment{Source: source}

	var e Extractor
	if err := e.Protect(seg); err != nil {
		t.Fatalf("Protect error: %v", err)
	}

	if want := "我、想、要、一、杯、水"; seg.Source != want {
		t.Errorf("seg.Source = %q, want %q", seg.Source, want)
	}

	anns, ok := seg.Meta["ruby_annotations"].([]Annotation)
	if !ok {
		t.Fatalf("ruby_annotations type = %T, want []Annotation", seg.Meta["ruby_annotations"])
	}
	if len(anns) != 6 {
		t.Fatalf("len(ruby_annotations) = %d, want 6", len(anns))
	}

	items, ok := seg.Meta["ruby_items"].([]Item)
	if !ok {
		t.Fatalf("ruby_items type = %T, want []Item", seg.Meta["ruby_items"])
	}
	if len(items) != 6 {
		t.Fatalf("len(ruby_items) = %d, want 6", len(items))
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
	// 与 annotations 一一对应
	for i, a := range anns {
		if a.Base != wantBases[i] || a.Text != wantTexts[i] {
			t.Errorf("anns[%d] = %+v, want base %q text %q", i, a, wantBases[i], wantTexts[i])
		}
	}
}

// TestProtect_NoRuby_NoMeta 验证无 ruby 元素时 Meta 不被写入任何 ruby key。
func TestProtect_NoRuby_NoMeta(t *testing.T) {
	seg := &model.Segment{Source: "plain text 无注音"}
	var e Extractor
	if err := e.Protect(seg); err != nil {
		t.Fatalf("Protect error: %v", err)
	}
	if seg.Meta != nil {
		t.Fatalf("seg.Meta = %v, want nil", seg.Meta)
	}
}
