package protect

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// mixedFragText 含行内代码、markdown 链接与 {placeholder} 占位符三类受保护片段。
const mixedFragText = "Run `code --flag` and see [docs](https://example.com/a) for {placeholder} details."

// TestProtectText_EquivalentToSegmentProtect 验证 ProtectText 与 Segment 级 Protect
// 走同一实现：对同一 Protector 与文本，两者产出的占位符形态文本与映射完全一致，
// 且 RestoreText 能按映射往返还原原文。
func TestProtectText_EquivalentToSegmentProtect(t *testing.T) {
	p := FromRules([]string{"code", "link", "placeholder", "xml"})

	textLevel, mapping, err := ProtectText(p, mixedFragText)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}

	seg := &model.Segment{Source: mixedFragText}
	if err := p.Protect(seg); err != nil {
		t.Fatalf("segment protect: %v", err)
	}
	if textLevel != seg.Source {
		t.Errorf("protected text mismatch:\ntext-level: %q\nsegment:    %q", textLevel, seg.Source)
	}
	if !reflect.DeepEqual(mapping, seg.Protected) {
		t.Errorf("mapping mismatch:\ntext-level: %#v\nsegment:    %#v", mapping, seg.Protected)
	}
	if len(mapping) == 0 {
		t.Fatal("expected at least one protected fragment")
	}
	if restored := RestoreText(textLevel, mapping); restored != mixedFragText {
		t.Errorf("round-trip mismatch\nwant: %q\ngot:  %q", mixedFragText, restored)
	}
}

// TestProtectText_NilProtector 验证 nil Protector 走降级路径：原样返回。
func TestProtectText_NilProtector(t *testing.T) {
	const in = "plain text `code` {x}"
	got, mapping, err := ProtectText(nil, in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != in {
		t.Errorf("text: want %q, got %q", in, got)
	}
	if mapping != nil {
		t.Errorf("mapping: want nil, got %#v", mapping)
	}
}

// TestRestoreText_EmptyMapping 验证空映射透传。
func TestRestoreText_EmptyMapping(t *testing.T) {
	const in = "nothing to restore __LF_000001__"
	if got := RestoreText(in, nil); got != in {
		t.Errorf("nil mapping: want %q, got %q", in, got)
	}
	if got := RestoreText(in, map[string]string{}); got != in {
		t.Errorf("empty mapping: want %q, got %q", in, got)
	}
}

// TestProtectText_MergesAdjacentPlaceholders 验证相邻占位符合并生效：
// `code` 与 {ph} 直接相连时，两者合并为单占位符，值为片段拼接；
// 还原后得到完整原文。
func TestProtectText_MergesAdjacentPlaceholders(t *testing.T) {
	p := FromRules([]string{"code", "link", "placeholder", "xml"})
	const in = "`code`{ph}"

	got, mapping, err := ProtectText(p, in)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	if len(mapping) != 1 {
		t.Fatalf("expected single merged placeholder, got %d keys: %#v (text=%q)", len(mapping), mapping, got)
	}
	for k, v := range mapping {
		if v != in {
			t.Errorf("merged value of %s: got %q, want %q", k, v, in)
		}
	}
	if restored := RestoreText(got, mapping); restored != in {
		t.Errorf("round-trip mismatch\nwant: %q\ngot:  %q", in, restored)
	}
}

// TestProtectText_SkipsLiteralPlaceholderCollision 验证 nextKey 跳过文本中已出现
// 的同形字面量：分配 key 不与既有 __LF_000001__ 字面量碰撞（否则出现次数校验
// 无法区分真假占位符，还原会落在错误位置）；回显字面量时由 invented 校验
// 显式拒绝（fail-closed）。
func TestProtectText_SkipsLiteralPlaceholderCollision(t *testing.T) {
	p := FromRules([]string{"link"})
	const in = "见 __LF_000001__ 与 [docs](https://example.com/a)"

	protected, mapping, err := ProtectText(p, in)
	if err != nil {
		t.Fatalf("protect: %v", err)
	}
	if len(mapping) != 1 {
		t.Fatalf("mapping=%#v want exactly one entry (text=%q)", mapping, protected)
	}
	var key string
	for k := range mapping {
		key = k
	}
	if key == "__LF_000001__" {
		t.Fatal("allocated key collides with pre-existing literal")
	}
	if strings.Count(protected, key) != 1 || strings.Count(protected, "__LF_000001__") != 1 {
		t.Fatalf("protected=%q must carry key and literal exactly once each", protected)
	}

	// LLM 原样回显：字面量不在映射中 → invented 显式拒绝，而非还原错位。
	seg := &model.Segment{Target: protected, Protected: mapping}
	_, _, invented := PlaceholderViolations(seg)
	if !reflect.DeepEqual(invented, []string{"__LF_000001__"}) {
		t.Fatalf("invented=%v want [__LF_000001__]", invented)
	}
}
