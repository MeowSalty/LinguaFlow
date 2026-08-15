package qa

import (
	"reflect"
	"testing"
)

func TestProtectedRegions_Single(t *testing.T) {
	target := `hello <a href="x">world</a> end`
	protected := map[string]string{"tag": `<a href="x">world</a>`}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{6, 27}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestProtectedRegions_Multiple(t *testing.T) {
	target := `a <b>c</b> d <e>f</e> g`
	protected := map[string]string{"b": `<b>c</b>`, "e": `<e>f</e>`}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{2, 10}, {13, 21}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestProtectedRegions_NotFound(t *testing.T) {
	target := `hello world`
	protected := map[string]string{"x": "zzz"}
	got := ProtectedRegions(target, protected)
	if got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestProtectedRegions_Adjacent(t *testing.T) {
	target := `<a>x</a><b>y</b>`
	protected := map[string]string{"a": `<a>x</a>`, "b": `<b>y</b>`}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{0, 8}, {8, 16}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestProtectedRegions_EmptyNil(t *testing.T) {
	if got := ProtectedRegions("abc", nil); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
	if got := ProtectedRegions("abc", map[string]string{}); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
	if got := ProtectedRegions("", map[string]string{"a": "x"}); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

func TestProtectedRegions_EmptyValue(t *testing.T) {
	protected := map[string]string{"a": "", "b": "<x>"}
	got := ProtectedRegions("a <x> b", protected)
	want := [][2]int{{2, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestProtectedRegions_Overlap(t *testing.T) {
	target := `abcXYZdef`
	protected := map[string]string{"long": "bcXYZde", "short": "XYZ"}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{1, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestProtectedRegions_CJK(t *testing.T) {
	target := `你好<a>x</a>世界`
	protected := map[string]string{"tag": `<a>x</a>`}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{2, 10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

func TestStripRegions_Single(t *testing.T) {
	target := `hello <a href="x">world</a> end`
	regions := [][2]int{{6, 27}}
	got := StripRegions(target, regions)
	want := `hello  end`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestStripRegions_Multiple(t *testing.T) {
	target := `a <b>c</b> d <e>f</e> g`
	regions := [][2]int{{2, 10}, {13, 21}}
	got := StripRegions(target, regions)
	want := `a  d  g`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestStripRegions_Empty(t *testing.T) {
	target := `hello world`
	got := StripRegions(target, nil)
	if got != target {
		t.Fatalf("want %q, got %q", target, got)
	}
	got = StripRegions(target, [][2]int{})
	if got != target {
		t.Fatalf("want %q, got %q", target, got)
	}
}

func TestStripRegions_StartEnd(t *testing.T) {
	got := StripRegions(`<a>x</a>hello`, [][2]int{{0, 8}})
	if got != `hello` {
		t.Fatalf("want %q, got %q", "hello", got)
	}
	got = StripRegions(`hello<a>x</a>`, [][2]int{{5, 13}})
	if got != `hello` {
		t.Fatalf("want %q, got %q", "hello", got)
	}
	got = StripRegions(`<a>x</a>`, [][2]int{{0, 8}})
	if got != `` {
		t.Fatalf("want %q, got %q", "", got)
	}
}

func TestStripRegions_CJK(t *testing.T) {
	target := `你好<a>x</a>世界`
	regions := [][2]int{{2, 10}}
	got := StripRegions(target, regions)
	want := `你好世界`
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestProtectedRegions_EmptyValueSkipped(t *testing.T) {
	protected := map[string]string{"a": "", "b": "x"}
	got := ProtectedRegions("x y z", protected)
	want := [][2]int{{0, 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// 多个占位符映射到同一保护值时（如重复 <br/>），必须屏蔽译文中所有出现位置。
func TestProtectedRegions_DuplicateValue(t *testing.T) {
	target := `第一行<br/>第二行<br/>`
	protected := map[string]string{
		"__LF_000001__": "<br/>",
		"__LF_000002__": "<br/>",
	}
	got := ProtectedRegions(target, protected)
	// 第一行(3 rune) + <br/>(5) + 第二行(3) + <br/>(5)
	want := [][2]int{{3, 8}, {11, 16}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	// 剔除后应不含任何 <br/>
	if stripped := StripRegions(target, got); stripped != `第一行第二行` {
		t.Fatalf("StripRegions want %q, got %q", `第一行第二行`, stripped)
	}
}

// 相邻重复保护值（如 <br/><br/>）必须两段都被屏蔽。
func TestProtectedRegions_DuplicateValueAdjacent(t *testing.T) {
	target := `A<br/><br/>B`
	protected := map[string]string{
		"__LF_000001__": "<br/>",
		"__LF_000002__": "<br/>",
	}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{1, 6}, {6, 11}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if stripped := StripRegions(target, got); stripped != `AB` {
		t.Fatalf("StripRegions want %q, got %q", `AB`, stripped)
	}
}

// 单个占位符映射的值在译文中重复出现多次时，所有副本都应被屏蔽。
func TestProtectedRegions_RepeatedValue(t *testing.T) {
	target := "x.y.z.y."
	protected := map[string]string{"p": "."}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{1, 2}, {3, 4}, {5, 6}, {7, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if stripped := StripRegions(target, got); stripped != "xyzy" {
		t.Fatalf("StripRegions want %q, got %q", "xyzy", stripped)
	}
}

// 两个保护值互为前缀且部分重叠时，较长区域应吞并较短区域，避免尾部内容泄漏。
// target="x ab y abcd z"：short="ab" 出现两次（偏移2、7），long="abcd" 起点为7。
// 期望合并后 long 的 [7,11] 覆盖 short 在 [7,9] 的副本，整体不残留 "cd"。
func TestProtectedRegions_PrefixOverlapMerge(t *testing.T) {
	target := `x ab y abcd z`
	protected := map[string]string{"short": "ab", "long": "abcd"}
	got := ProtectedRegions(target, protected)
	// short@[2,4] 独立；short@[7,9] 被 long@[7,11] 吞并扩展为 [7,11]
	want := [][2]int{{2, 4}, {7, 11}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if stripped := StripRegions(target, got); stripped != "x  y  z" {
		t.Fatalf("StripRegions want %q, got %q", "x  y  z", stripped)
	}
}

// 同起点前缀关系（如 <br> 与 <br/>）：较长区域必须胜出，否则会泄漏 "/>"。
func TestProtectedRegions_SameStartPrefix(t *testing.T) {
	target := `<br/>`
	protected := map[string]string{"a": "<br>", "b": "<br/>"}
	got := ProtectedRegions(target, protected)
	want := [][2]int{{0, 5}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if stripped := StripRegions(target, got); stripped != "" {
		t.Fatalf("StripRegions want empty, got %q", stripped)
	}
}

// LocateSpanExcludingRegions 应跳过保护区内的出现，指向保护区外的真实问题字符。
func TestLocateSpanExcludingRegions_OutsideProtected(t *testing.T) {
	tgt := `<a,x>你好,`
	protected := map[string]string{"p": `<a,x>`}
	regions := ProtectedRegions(tgt, protected) // [0,5]
	span := LocateSpanExcludingRegions(tgt, ",", regions)
	if span == nil || span.TargetStart == nil {
		t.Fatalf("want non-nil span with offsets, got %+v", span)
	}
	// 真实逗号在 rune 7（<a,x>=5 runes + 你好=2 runes）。
	if *span.TargetStart != 7 || *span.TargetEnd != 8 {
		t.Fatalf("want start=7 end=8, got start=%d end=%d", *span.TargetStart, *span.TargetEnd)
	}
}

// 所有出现都在保护区内时，回退到首次出现偏移，不丢失 issue。
func TestLocateSpanExcludingRegions_AllInsideFallback(t *testing.T) {
	tgt := `<a,x>`
	protected := map[string]string{"p": `<a,x>`}
	regions := ProtectedRegions(tgt, protected)
	span := LocateSpanExcludingRegions(tgt, ",", regions)
	if span == nil {
		t.Fatalf("want non-nil span, got nil")
	}
	// 逗号在 rune 2（保护区内部），回退仍返回偏移而非丢弃。
	if span.TargetStart == nil {
		t.Fatalf("want fallback offsets, got nil (issue would be dropped)")
	}
}

// 无保护区时，退化为普通 LocateSpan 语义。
func TestLocateSpanExcludingRegions_NoRegions(t *testing.T) {
	span := LocateSpanExcludingRegions("hello,world", ",", nil)
	if span == nil || span.TargetStart == nil {
		t.Fatalf("want span with offsets, got %+v", span)
	}
	if *span.TargetStart != 5 || *span.TargetEnd != 6 {
		t.Fatalf("want start=5 end=6, got start=%d end=%d", *span.TargetStart, *span.TargetEnd)
	}
}

// InlineMarkupRegions 在无 ruby 时等价于 ProtectedRegions。
func TestInlineMarkupRegions_EqualsProtectedWhenNoRuby(t *testing.T) {
	target := `hello <a href="x">world</a> end`
	protected := map[string]string{"tag": `<a href="x">world</a>`}
	got := InlineMarkupRegions(target, protected)
	want := [][2]int{{6, 27}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("want %v, got %v", want, got)
	}
}

// 纯 ruby：整个 <ruby> 元素（含基底）应被整体屏蔽。
func TestInlineMarkupRegions_PureRuby(t *testing.T) {
	ruby := "<ruby>呪<rt>じゅ</rt></ruby>"
	got := InlineMarkupRegions(ruby, nil)
	if stripped := StripRegions(ruby, got); stripped != "" {
		t.Fatalf("strip ruby want empty, got %q (regions=%v)", stripped, got)
	}
}

// ruby 与 protected 相邻：两段都被屏蔽。
func TestInlineMarkupRegions_RubyAndProtected(t *testing.T) {
	span := `a<b>x</b>`
	ruby := "<ruby>呪<rt>じゅ</rt></ruby>"
	target := span + ruby
	got := InlineMarkupRegions(target, map[string]string{"p": span})
	if len(got) != 2 {
		t.Fatalf("want 2 regions (span + ruby), got %d: %v", len(got), got)
	}
	if stripped := StripRegions(target, got); stripped != "" {
		t.Fatalf("strip want empty, got %q", stripped)
	}
}

// 空输入返回 nil（与 ProtectedRegions 一致；StripRegions(text,nil)==text）。
func TestInlineMarkupRegions_Empty(t *testing.T) {
	if got := InlineMarkupRegions("", nil); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
	if got := InlineMarkupRegions("abc", nil); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
	if got := InlineMarkupRegions("abc", map[string]string{}); got != nil {
		t.Fatalf("want nil, got %v", got)
	}
}

// CJK ruby 元素整体被删，保留 ruby 外的 CJK 文本。
func TestInlineMarkupRegions_CJKRuby(t *testing.T) {
	target := "見<ruby>呪<rt>じゅ</rt></ruby>術"
	got := InlineMarkupRegions(target, nil)
	if stripped := StripRegions(target, got); stripped != "見術" {
		t.Fatalf("strip want 見術, got %q (regions=%v)", stripped, got)
	}
}

// protected 与 ruby 区域有重叠/混合时仍正确并集屏蔽。
func TestInlineMarkupRegions_ProtectedInsideAroundRuby(t *testing.T) {
	span := `<a>1</a>`
	target := span + "<ruby>呪<rt>じゅ</rt></ruby>" + span
	got := InlineMarkupRegions(target, map[string]string{"p": span})
	if stripped := StripRegions(target, got); stripped != "" {
		t.Fatalf("strip want empty, got %q (regions=%v)", stripped, got)
	}
}
