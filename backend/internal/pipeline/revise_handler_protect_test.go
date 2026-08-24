package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// protectReviseDoc 构造带 pending 语义问题的单段文档。
func protectReviseDoc(source, target string, snippet string) *Document {
	issue := qa.QualityIssue{Code: "naturalness", Message: "表达生硬"}
	if snippet != "" {
		issue.Span = &qa.Span{MatchedText: snippet}
	}
	return &Document{
		SourceLang: "ja", TargetLang: "zh", Vars: map[string]any{},
		Segments: []Segment{
			{ID: "0", Source: source, Target: target, Status: "translated", Translate: true,
				Issues: []qa.QualityIssue{issue}},
		},
	}
}

func soleMappingKey(t *testing.T, mapping map[string]string) string {
	t.Helper()
	if len(mapping) != 1 {
		t.Fatalf("mapping=%v want exactly one entry", mapping)
	}
	for k := range mapping {
		return k
	}
	return ""
}

// reviseUserPayload 解开 JSON 模式 user envelope（json.Marshal 会 HTML 转义标签，
// 直接 Contains 断言不可靠，统一走反序列化）。
func reviseUserPayload(t *testing.T, user string) (segments []map[string]any, rubyAnns map[string]any) {
	t.Helper()
	var env struct {
		Segments        []map[string]any `json:"segments"`
		RubyAnnotations map[string]any   `json:"ruby_annotations"`
	}
	if err := json.Unmarshal([]byte(user), &env); err != nil {
		t.Fatalf("unmarshal user envelope: %v", err)
	}
	return env.Segments, env.RubyAnnotations
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestReviseHandler_ProtectRoundTrip 验证 target 进 prompt 前被占位符化，
// LLM 保持占位符时 callback 收到还原后的最终译文，doc 全程不被污染。
func TestReviseHandler_ProtectRoundTrip(t *testing.T) {
	const target = "现在运行 `make build` 吧"
	doc := protectReviseDoc("Run `make build` now", target, "")

	prot := protect.FromRules([]string{"code", "link", "placeholder", "xml"})
	protected, mapping, err := protect.ProtectText(prot, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping) != 1 {
		t.Fatalf("mapping=%v want one code span", mapping)
	}
	revised := strings.Replace(protected, " 吧", "", 1)
	want := strings.Replace(target, " 吧", "", 1)

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{"revisions": []map[string]string{{"id": "0", "target": revised}}}),
	}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Protector: prot, Logger: discardLogger()}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v want one revision", result.callbackResult)
	}
	if got := result.callbackResult.Segments[0].TargetText; got != want {
		t.Fatalf("target=%q want restored %q", got, want)
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("unresolved=%v want none", result.unresolved)
	}

	key := soleMappingKey(t, mapping)
	if !strings.Contains(fb.requests[0].User, key) {
		t.Fatalf("prompt must carry placeholder %q for target", key)
	}
	// source 不做占位符化（无守恒约束），应保留原文形态。
	if !strings.Contains(fb.requests[0].User, "Run `make build` now") {
		t.Fatal("prompt must carry raw source for reference")
	}
	if doc.Segments[0].Target != target || doc.Segments[0].Source != "Run `make build` now" ||
		doc.Segments[0].Protected != nil {
		t.Fatalf("doc mutated: %+v", doc.Segments[0])
	}
}

// TestReviseHandler_PlaceholderViolationRejected 验证丢失与捏造占位符的修订
// 均被拒绝并计入 unresolved（与 translate 轮违规即拒语义一致）。
func TestReviseHandler_PlaceholderViolationRejected(t *testing.T) {
	const target = "现在运行 `make build` 吧"
	prot := protect.FromRules([]string{"code", "link", "placeholder", "xml"})
	protected, mapping, err := protect.ProtectText(prot, target)
	if err != nil {
		t.Fatal(err)
	}
	key := soleMappingKey(t, mapping)

	cases := []struct {
		name string
		// revised 为 LLM 返回的（占位符形态）修订译文
		revised string
	}{
		{"missing placeholder", strings.ReplaceAll(protected, key, "")},
		{"invented placeholder", protected + " __LF_000009__"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := protectReviseDoc("Run `make build` now", target, "")
			fb := &fakeBackend{name: "fake", responses: []string{
				mustJSON(t, map[string]any{"revisions": []map[string]string{{"id": "0", "target": tc.revised}}}),
			}}
			h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Protector: prot, Logger: discardLogger()}

			result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
			if !reflect.DeepEqual(result.unresolved, []int{0}) {
				t.Fatalf("unresolved=%v want [0]", result.unresolved)
			}
			if result.callbackResult != nil && len(result.callbackResult.Segments) != 0 {
				t.Fatalf("callback=%#v want empty", result.callbackResult)
			}
			if doc.Segments[0].Target != target {
				t.Fatalf("doc mutated: %q", doc.Segments[0].Target)
			}
		})
	}
}

// TestReviseHandler_RubyJSONRoundTrip 验证 JSON 协议注音往返：prompt 内 target
// 无 ruby 标签、ruby_annotations 带条目 id 下发，LLM 回填 ruby_output 后 callback
// 收到还原 <ruby> 标签的最终译文。
func TestReviseHandler_RubyJSONRoundTrip(t *testing.T) {
	doc := protectReviseDoc("漢語の本", "<ruby>漢<rt>かん</rt></ruby>語の本", "")

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{
			"revisions": []map[string]string{{"id": "0", "target": "漢語之書"}},
			"ruby_output": map[string][]map[string]string{
				"0": {{"id": "1", "base": "漢語", "text": "かんご", "kind": "phonetic"}},
			},
		}),
	}}
	h := &ReviseHandler{
		Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
		RubyEnabled: true, RubyMode: "json", RubyRestorer: ruby.NewRestorer(),
	}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v want one revision", result.callbackResult)
	}
	want := "<ruby>漢語<rt>かんご</rt></ruby>之書"
	if got := result.callbackResult.Segments[0].TargetText; got != want {
		t.Fatalf("target=%q want %q", got, want)
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("unresolved=%v want none", result.unresolved)
	}
	segs, anns := reviseUserPayload(t, fb.requests[0].User)
	if len(segs) != 1 || segs[0]["target"] != "漢語の本" {
		t.Fatalf("prompt target must be ruby-stripped, got segments=%v", segs)
	}
	if len(anns) != 1 {
		t.Fatalf("prompt must carry ruby_annotations for segment 0, got %v", anns)
	}
	if doc.Segments[0].Target != "<ruby>漢<rt>かん</rt></ruby>語の本" || doc.Segments[0].Source != "漢語の本" {
		t.Fatalf("doc mutated: %+v", doc.Segments[0])
	}
}

// TestReviseHandler_RubyTextModeSection 验证 text/section 协议注音往返：
// user 内段块带 ruby 输入行，响应 [ruby] 段被解析并还原；snippet 含 ruby 标签时
// 同步剥离为可定位形态。
func TestReviseHandler_RubyTextModeSection(t *testing.T) {
	doc := protectReviseDoc("漢語の本", "<ruby>漢<rt>かん</rt></ruby>語の本", "<ruby>漢<rt>かん</rt></ruby>語の本")

	fb := &fakeBackend{name: "fake", responses: []string{
		"[revisions]\n0 | 漢語之書\n[ruby]\n0: 漢語 | かんご | phonetic | 1\n",
	}}
	h := &ReviseHandler{
		Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
		ResponseMode: "text", RubyEnabled: true, RubyMode: "section", RubyRestorer: ruby.NewRestorer(),
	}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	want := "<ruby>漢語<rt>かんご</rt></ruby>之書"
	if result.callbackResult == nil || result.callbackResult.Segments[0].TargetText != want {
		t.Fatalf("callback=%#v want %q", result.callbackResult, want)
	}
	user := fb.requests[0].User
	if !strings.Contains(user, "ruby: 漢/かん#1") {
		t.Fatalf("text user must carry ruby input line, got:\n%s", user)
	}
	if !strings.Contains(user, "snippet: 漢語の本") {
		t.Fatalf("snippet must be ruby-stripped for locating, got:\n%s", user)
	}
	if strings.Contains(user, "<ruby>") {
		t.Fatal("text user must be ruby-stripped")
	}
}

// TestReviseHandler_RubyInlineMarkersRejected 验证协议禁止的 inline ⟦ruby:...⟧
// 标记被 fail-closed 拒绝：标记计数由 LLM 自证、无法与存量条目交叉核验，
// 容忍即开放守卫绕过（伪造标记凑数丢弃存量注音）——该段计入 unresolved，
// doc 保留原带标签译文。
func TestReviseHandler_RubyInlineMarkersRejected(t *testing.T) {
	const target = "<ruby>漢<rt>かん</rt></ruby>語の本"
	doc := protectReviseDoc("漢語の本", target, "")

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{"revisions": []map[string]string{
			{"id": "0", "target": "⟦ruby:漢語/かんご/phonetic⟧之書"},
		}}),
	}}
	h := &ReviseHandler{
		Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
		RubyEnabled: true, RubyMode: "json", RubyRestorer: ruby.NewRestorer(),
	}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
	if result.callbackResult != nil && len(result.callbackResult.Segments) != 0 {
		t.Fatalf("callback=%#v want empty", result.callbackResult)
	}
	if doc.Segments[0].Target != target {
		t.Fatalf("doc must keep annotated target, got %q", doc.Segments[0].Target)
	}
}

// TestReviseHandler_RubyRealignmentIncompleteRejected 验证注音守恒守卫：LLM 合法
// 返回修订但注音回填不完整时，剥离形态译文不得被采信——该段计入 unresolved，
// doc 保留原带标签译文（防止存量注音被静默剥离写回）。
func TestReviseHandler_RubyRealignmentIncompleteRejected(t *testing.T) {
	cases := []struct {
		name string
		// revised 为 LLM 返回的修订译文；rubyOutput 缺省/未命中的两种失败形态。
		revised   string
		rubyEntry map[string]string
	}{
		{
			// 漏返 ruby_output（text 模式 [ruby] 段可选、JSON 空数组合法）。
			name:    "missing ruby_output",
			revised: "漢語之書",
		},
		{
			// 回填 base 未命中修订译文，SourceBase（漢）亦不在简体译文中。
			name:      "ruby base misses revised text",
			revised:   "汉语之书",
			rubyEntry: map[string]string{"id": "1", "base": "和書", "text": "わしょ", "kind": "phonetic"},
		},
		{
			// 字面量 <ruby> 文本凑数：判据为还原器实际插入数而非子串计数，无法伪造。
			name:    "literal ruby text cannot pad count",
			revised: "漢語之書<ruby>偽</ruby>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const target = "<ruby>漢<rt>かん</rt></ruby>語の本"
			doc := protectReviseDoc("漢語の本", target, "")

			resp := map[string]any{
				"revisions": []map[string]string{{"id": "0", "target": tc.revised}},
			}
			if tc.rubyEntry != nil {
				resp["ruby_output"] = map[string][]map[string]string{"0": {tc.rubyEntry}}
			}
			fb := &fakeBackend{name: "fake", responses: []string{mustJSON(t, resp)}}
			h := &ReviseHandler{
				Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
				RubyEnabled: true, RubyMode: "json", RubyRestorer: ruby.NewRestorer(),
			}

			result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
			if !reflect.DeepEqual(result.unresolved, []int{0}) {
				t.Fatalf("unresolved=%v want [0]", result.unresolved)
			}
			if result.callbackResult != nil && len(result.callbackResult.Segments) != 0 {
				t.Fatalf("callback=%#v want empty", result.callbackResult)
			}
			if doc.Segments[0].Target != target {
				t.Fatalf("doc must keep annotated target, got %q", doc.Segments[0].Target)
			}
		})
	}
}

// TestReviseHandler_RubyKindRelabelRejected 验证守卫口径不依赖 LLM 事后回填的
// kind：preserve_kinds 为真子集时，LLM 把存量音注重标到集合外（即使 base 正确）
// 不得把条目挤出守恒口径——该段仍被拒绝（fail-closed），而非静默丢失注音。
func TestReviseHandler_RubyKindRelabelRejected(t *testing.T) {
	const target = "<ruby>漢<rt>かん</rt></ruby>語の本"
	doc := protectReviseDoc("漢語の本", target, "")

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{
			"revisions": []map[string]string{{"id": "0", "target": "漢語之書"}},
			"ruby_output": map[string][]map[string]string{
				"0": {{"id": "1", "base": "漢語", "text": "かんご", "kind": "semantic"}},
			},
		}),
	}}
	h := &ReviseHandler{
		Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
		RubyEnabled: true, RubyMode: "json", RubyRestorer: ruby.NewRestorer(),
		RubyPreserveKinds: []string{"phonetic"},
	}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if !reflect.DeepEqual(result.unresolved, []int{0}) {
		t.Fatalf("unresolved=%v want [0]", result.unresolved)
	}
	if result.callbackResult != nil && len(result.callbackResult.Segments) != 0 {
		t.Fatalf("callback=%#v want empty", result.callbackResult)
	}
	if doc.Segments[0].Target != target {
		t.Fatalf("doc must keep annotated target, got %q", doc.Segments[0].Target)
	}
}

// TestReviseHandler_RubyEmptyBaseItemAccepted 验证守卫与 RestoreItems 的 total
// 口径对齐：无基底文本的退化注音条目（<ruby><rt>…</rt></ruby>）永不可还原，
// 不计入 want——含此类条目的段修订不会被系统性误拒。
func TestReviseHandler_RubyEmptyBaseItemAccepted(t *testing.T) {
	doc := protectReviseDoc("語の本", "<ruby><rt>かん</rt></ruby>語の本", "")

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{"revisions": []map[string]string{{"id": "0", "target": "書"}}}),
	}}
	h := &ReviseHandler{
		Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
		RubyEnabled: true, RubyMode: "json", RubyRestorer: ruby.NewRestorer(),
	}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v want one revision", result.callbackResult)
	}
	if got := result.callbackResult.Segments[0].TargetText; got != "書" {
		t.Fatalf("target=%q want %q", got, "書")
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("unresolved=%v want none", result.unresolved)
	}
}

// TestReviseHandler_RubyPreserveKindsEmptyStrips 验证 keep 为空集（用户显式
// 全剥离）时 want=0：注音被有意剥离的修订正常接受。
func TestReviseHandler_RubyPreserveKindsEmptyStrips(t *testing.T) {
	doc := protectReviseDoc("漢語の本", "<ruby>漢<rt>かん</rt></ruby>語の本", "")

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{
			"revisions": []map[string]string{{"id": "0", "target": "漢語之書"}},
			"ruby_output": map[string][]map[string]string{
				"0": {{"id": "1", "base": "漢語", "text": "かんご", "kind": "phonetic"}},
			},
		}),
	}}
	h := &ReviseHandler{
		Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger(),
		RubyEnabled: true, RubyMode: "json", RubyRestorer: ruby.NewRestorer(),
		RubyPreserveKinds: []string{},
	}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v want one revision", result.callbackResult)
	}
	if got := result.callbackResult.Segments[0].TargetText; got != "漢語之書" || strings.Contains(got, "<ruby>") {
		t.Fatalf("target=%q want stripped %q", got, "漢語之書")
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("unresolved=%v want none", result.unresolved)
	}
}

// TestReviseHandler_SnippetMappedToProtectedForm 验证 snippet 中已被保护的
// 原文片段被替换回占位符，与 prompt 内 target 形态一致。
func TestReviseHandler_SnippetMappedToProtectedForm(t *testing.T) {
	const target = "现在运行 `make build` 吧"
	doc := protectReviseDoc("Run `make build` now", target, "运行 `make build`")

	prot := protect.FromRules([]string{"code", "link", "placeholder", "xml"})
	protected, mapping, err := protect.ProtectText(prot, target)
	if err != nil {
		t.Fatal(err)
	}
	key := soleMappingKey(t, mapping)

	fb := &fakeBackend{name: "fake", responses: []string{
		// LLM 判定问题不成立：原样回显保护形态（占位符守恒）。
		mustJSON(t, map[string]any{"revisions": []map[string]string{{"id": "0", "target": protected}}}),
	}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Protector: prot, Logger: discardLogger()}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || len(result.callbackResult.Segments) != 1 {
		t.Fatalf("callback=%#v want one revision (unchanged target keeps placeholders)", result.callbackResult)
	}
	segs, _ := reviseUserPayload(t, fb.requests[0].User)
	issues, _ := segs[0]["issues"].([]any)
	if len(issues) != 1 {
		t.Fatalf("segments[0].issues=%v want one issue", segs[0]["issues"])
	}
	issue, _ := issues[0].(map[string]any)
	if got := issue["snippet"]; got != "运行 "+key {
		t.Fatalf("snippet=%v want protected form %q", got, "运行 "+key)
	}
}

// TestReviseHandler_RubyDisabledSendsRaw 验证零值降级：无 Protector、ruby 关闭时
// 行为与历史一致——原文（含 ruby 标签）直发，修订结果原样返回。
func TestReviseHandler_RubyDisabledSendsRaw(t *testing.T) {
	const target = "<ruby>漢<rt>かん</rt></ruby>語の本"
	doc := protectReviseDoc("漢語の本", target, "")

	fb := &fakeBackend{name: "fake", responses: []string{
		mustJSON(t, map[string]any{"revisions": []map[string]string{{"id": "0", "target": target}}}),
	}}
	h := &ReviseHandler{Backend: fb, Renderer: newReviseRenderer(t), Logger: discardLogger()}

	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, discardLogger())
	if result.callbackResult == nil || result.callbackResult.Segments[0].TargetText != target {
		t.Fatalf("callback=%#v want raw passthrough", result.callbackResult)
	}
	// 降级路径直发原文：target 字段保留 <ruby> 标签（经反序列化断言，避开
	// json.Marshal 的 HTML 转义）。
	segs, _ := reviseUserPayload(t, fb.requests[0].User)
	if len(segs) != 1 || segs[0]["target"] != target {
		t.Fatalf("degraded mode must send raw target, got segments=%v", segs)
	}
}
