package protect

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// TestRubyProtector_MultipleRT 回归钉住多段 <rt> 注音的用户可见缺陷：
// 旧实现对 <ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby> 只识别首对注音，剥离后为
// 「何故ぜこんなにも」——第二对注音「ぜ」以正文身份泄漏进送翻文本，且注音
// 清单静默缺「故/ぜ」。修复后剥离结果与等价的相邻独立元素一致。
func TestRubyProtector_MultipleRT(t *testing.T) {
	seg := &model.Segment{Source: "<ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>こんなにも"}
	if err := NewRubyProtector().Protect(seg); err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	if want := "何故こんなにも"; seg.Source != want {
		t.Errorf("seg.Source = %q, want %q", seg.Source, want)
	}
	items, ok := seg.Meta["ruby_items"].([]ruby.Item)
	if !ok {
		t.Fatalf("Meta[ruby_items] = %T, want []ruby.Item", seg.Meta["ruby_items"])
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	it := items[0]
	if it.ID != "1" || it.SourceBase != "何故" || it.SourceText != "なぜ" {
		t.Errorf("item = %+v, want {ID:1, SourceBase:何故, SourceText:なぜ}", it)
	}
}

// 无 ruby 的段：不建 Meta、不写任何 ruby key，源文原样
// （保持「无注音 → Meta 不被写入任何 ruby key」的历史约定）。
func TestRubyProtector_NoRuby(t *testing.T) {
	seg := &model.Segment{Source: "plain text 无注音"}
	if err := NewRubyProtector().Protect(seg); err != nil {
		t.Fatalf("Protect() error = %v", err)
	}
	if seg.Meta != nil {
		if _, exists := seg.Meta["ruby_items"]; exists {
			t.Errorf("Meta[ruby_items] 存在, want 未写入")
		}
	}
	if seg.Source != "plain text 无注音" {
		t.Errorf("seg.Source = %q, want 原样", seg.Source)
	}
}
