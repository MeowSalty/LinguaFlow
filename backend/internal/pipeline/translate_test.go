package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

func TestParseBatchResponse_OK(t *testing.T) {
	resp := `{"translations":{"1":"hello","2":"world"}}`
	got, glos, rubyOut, err := parseBatchResponse(resp, []string{"1", "2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "hello" || got["2"] != "world" {
		t.Fatalf("unexpected parts: %#v", got)
	}
	if glos != nil {
		t.Errorf("glossary should be nil when field absent, got %#v", glos)
	}
	if rubyOut != nil {
		t.Errorf("ruby_output should be nil when field absent, got %#v", rubyOut)
	}
}

func TestParseBatchResponse_PreservesInternalNewlines(t *testing.T) {
	resp := `{"translations":{"1":"line1\nline2"}}`
	got, _, _, err := parseBatchResponse(resp, []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "line1\nline2" {
		t.Fatalf("internal newline lost: %q", got["1"])
	}
}

func TestParseBatchResponse_MissingID(t *testing.T) {
	resp := `{"translations":{"1":"a"}}`
	if _, _, _, err := parseBatchResponse(resp, []string{"1", "2"}); err == nil {
		t.Fatal("expected error for missing id")
	}
}

func TestParseBatchResponse_ExtraID(t *testing.T) {
	resp := `{"translations":{"1":"a","2":"b","3":"c"}}`
	_, _, _, err := parseBatchResponse(resp, []string{"1", "2"})
	if err == nil {
		t.Fatal("expected error for extra translation")
	}
}

func TestParseBatchResponse_IgnoresCodeFenceAndPreamble(t *testing.T) {
	resp := "Sure! Here you go:\n```json\n{\"translations\":{\"1\":\"a\",\"2\":\"b\"}}\n```\nDone."
	got, _, _, err := parseBatchResponse(resp, []string{"1", "2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "a" || got["2"] != "b" {
		t.Fatalf("unexpected parts: %#v", got)
	}
}

func TestParseBatchResponse_HandlesEscapedBraceInValue(t *testing.T) {
	resp := `{"translations":{"1":"value with } and \"quote\" inside"}}`
	got, _, _, err := parseBatchResponse(resp, []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `value with } and "quote" inside`
	if got["1"] != want {
		t.Fatalf("got %q want %q", got["1"], want)
	}
}

func TestParseBatchResponse_NotJSON(t *testing.T) {
	if _, _, _, err := parseBatchResponse("totally not json", []string{"1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseBatchResponse_ParsesInlineGlossary(t *testing.T) {
	resp := `{"translations":{"1":"你好"},"glossary":[{"source":"Hello","target":"你好","notes":""},{"source":"World","target":"世界","notes":"greeting suffix"}]}`
	got, glos, _, err := parseBatchResponse(resp, []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "你好" {
		t.Errorf("translation mismatch: %#v", got)
	}
	if len(glos) != 2 {
		t.Fatalf("want 2 inline glossary entries, got %d: %#v", len(glos), glos)
	}
	if glos[0].Source != "Hello" || glos[1].Notes != "greeting suffix" {
		t.Errorf("entry fields mismatch: %#v", glos)
	}
}

func TestParseBatchResponse_EmptyGlossaryArray(t *testing.T) {
	resp := `{"translations":{"1":"a"},"glossary":[]}`
	got, glos, _, err := parseBatchResponse(resp, []string{"1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["1"] != "a" {
		t.Errorf("translation mismatch: %#v", got)
	}
	if len(glos) != 0 {
		t.Errorf("want empty glossary slice, got %#v", glos)
	}
}

func TestTranslationsSchema_NoGlossary(t *testing.T) {
	schema := translationsSchema([]string{"1", "2", "3"}, false, false)
	if schema["additionalProperties"] != false {
		t.Errorf("outer additionalProperties should be false")
	}
	outerRequired, _ := schema["required"].([]string)
	if !reflect.DeepEqual(outerRequired, []string{"translations"}) {
		t.Errorf("outer required mismatch: %#v", outerRequired)
	}
	props := schema["properties"].(map[string]any)
	if _, ok := props["glossary"]; ok {
		t.Errorf("glossary should be absent when includeGlossary=false: %#v", props)
	}
	tr := props["translations"].(map[string]any)
	if tr["type"] != "object" || tr["additionalProperties"] != false {
		t.Errorf("translations object shape wrong: %#v", tr)
	}
	req, _ := tr["required"].([]string)
	if !reflect.DeepEqual(req, []string{"1", "2", "3"}) {
		t.Errorf("translations.required mismatch: %#v", req)
	}
	innerProps := tr["properties"].(map[string]any)
	for _, id := range []string{"1", "2", "3"} {
		p, ok := innerProps[id].(map[string]any)
		if !ok {
			t.Fatalf("missing property %q in schema: %#v", id, innerProps)
		}
		if p["type"] != "string" {
			t.Errorf("property %q type should be string, got %v", id, p["type"])
		}
	}
}

func TestTranslationsSchema_WithGlossary(t *testing.T) {
	schema := translationsSchema([]string{"1"}, true, false)
	outerRequired, _ := schema["required"].([]string)
	if !reflect.DeepEqual(outerRequired, []string{"translations", "glossary"}) {
		t.Errorf("outer required should list both fields, got %#v", outerRequired)
	}
	props := schema["properties"].(map[string]any)
	glos, ok := props["glossary"].(map[string]any)
	if !ok {
		t.Fatalf("glossary missing from properties: %#v", props)
	}
	if glos["type"] != "array" {
		t.Errorf("glossary should be array, got %v", glos["type"])
	}
	item := glos["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Error("item additionalProperties should be false")
	}
	itemReq, _ := item["required"].([]string)
	if !reflect.DeepEqual(itemReq, []string{"source", "target", "notes"}) {
		t.Errorf("item required mismatch: %#v", itemReq)
	}
}

func TestTranslationsSchema_WithRuby(t *testing.T) {
	schema := translationsSchema([]string{"1", "2"}, false, true)
	outerRequired, _ := schema["required"].([]string)
	if !reflect.DeepEqual(outerRequired, []string{"translations", "ruby_output"}) {
		t.Errorf("outer required should include ruby_output, got %#v", outerRequired)
	}
	props := schema["properties"].(map[string]any)
	ro, ok := props["ruby_output"].(map[string]any)
	if !ok {
		t.Fatalf("ruby_output missing from properties: %#v", props)
	}
	if ro["type"] != "object" {
		t.Errorf("ruby_output should be object, got %v", ro["type"])
	}
	roProps := ro["properties"].(map[string]any)
	for _, id := range []string{"1", "2"} {
		arr, ok := roProps[id].(map[string]any)
		if !ok {
			t.Fatalf("ruby_output missing property %q: %#v", id, roProps)
		}
		if arr["type"] != "array" {
			t.Errorf("ruby_output[%q] should be array, got %v", id, arr["type"])
		}
		item := arr["items"].(map[string]any)
		itemReq, _ := item["required"].([]string)
		if !reflect.DeepEqual(itemReq, []string{"base", "text", "kind"}) {
			t.Errorf("item required mismatch: %#v", itemReq)
		}
		itemProps := item["properties"].(map[string]any)
		kindProp, ok := itemProps["kind"].(map[string]any)
		if !ok {
			t.Fatalf("kind property missing from ruby_output item: %#v", itemProps)
		}
		if kindProp["type"] != "string" {
			t.Errorf("kind type should be string, got %v", kindProp["type"])
		}
		kindEnum, ok := kindProp["enum"].([]string)
		if !ok {
			t.Fatalf("kind enum should be []string, got %T", kindProp["enum"])
		}
		if !reflect.DeepEqual(kindEnum, []string{"phonetic", "semantic", "creative"}) {
			t.Errorf("kind enum mismatch: %#v", kindEnum)
		}
	}
}

func TestTranslationsSchema_WithGlossaryAndRuby(t *testing.T) {
	schema := translationsSchema([]string{"1"}, true, true)
	outerRequired, _ := schema["required"].([]string)
	want := []string{"translations", "glossary", "ruby_output"}
	if !reflect.DeepEqual(outerRequired, want) {
		t.Errorf("outer required mismatch: %#v, want %#v", outerRequired, want)
	}
}

func TestJSONObjectSlice_FindsNested(t *testing.T) {
	in := `noise {"a":{"b":1}} trailing`
	got := jsonObjectSlice(in)
	if !strings.HasPrefix(got, "{") || !strings.HasSuffix(got, "}") {
		t.Fatalf("not bracketed: %q", got)
	}
	if got != `{"a":{"b":1}}` {
		t.Fatalf("unexpected slice: %q", got)
	}
}

func TestCountWords(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"empty", "", 0},
		{"cjk_two_chars", "你好", 2},
		{"latin_one_word", "hello", 1},
		{"latin_two_words", "hello world", 2},
		{"mixed_cjk_latin", "你好world", 3},
		{"mixed_full", "Hello, 你好世界!", 6},
		{"numbers_and_cjk", "123 你好", 3},
		{"whitespace_only", "   ", 0},
		{"cjk_hiragana", "あいう", 3},
		{"cjk_katakana", "アイウ", 3},
		{"cjk_hangul", "한글", 2},
		{"punctuation_only", ".,;!", 1},
		{"mixed_spaces", " a  b  c ", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CountWords(tc.text)
			if got != tc.want {
				t.Errorf("CountWords(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestCalcMaxBootstrapTerms_UsesCountWords(t *testing.T) {
	h := &TranslateHandler{MaxTermsPer1000Chars: 3.0}
	// CJK: 4 字 → 4 words → ceil(4/1000*3) = 1
	got := h.calcMaxBootstrapTerms([]string{"你好世界"})
	if got != 1 {
		t.Errorf("CJK 4 chars: got %d want 1", got)
	}
	// Latin: "hello world" = 2 words → ceil(2/1000*3) = 1
	got = h.calcMaxBootstrapTerms([]string{"hello world"})
	if got != 1 {
		t.Errorf("Latin 2 words: got %d want 1", got)
	}
	// Large: 500 CJK chars → 500 words → ceil(500/1000*3) = 2
	big := ""
	for i := 0; i < 500; i++ {
		big += "字"
	}
	got = h.calcMaxBootstrapTerms([]string{big})
	if got != 2 {
		t.Errorf("500 CJK chars: got %d want 2", got)
	}
}

func TestBuildContinuousPendingBatches(t *testing.T) {
	doc := testDoc(13)
	got := BuildContinuousPendingBatches(doc, []int{0, 1, 2, 5, 6, 10}, segConstraint(4))
	want := [][]int{{0, 1, 2}, {5, 6}, {10}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batches=%v want %v", got, want)
	}

	got = BuildContinuousPendingBatches(doc, []int{0, 1, 2, 3, 8, 9, 12}, segConstraint(2))
	want = [][]int{{0, 1}, {2, 3}, {8, 9}, {12}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batches=%v want %v", got, want)
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestAbsorbInlineGlossary_RewritesConflictInBatch 验证并发冲突场景下的核心修复。
func TestAbsorbInlineGlossary_RewritesConflictInBatch(t *testing.T) {
	g := glossary.NewMemory()
	if _, err := g.Add(context.Background(), glossary.Entry{Source: "thread pool", Target: "线程池"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	h := &TranslateHandler{
		Glossary:               g,
		InlineBootstrap:        true,
		MinBootstrapSourceLen:  2,
		MaxTermsPer1000Chars:   3.0,
		InlineConflictStrategy: InlineConflictRewriteLocal,
	}
	entries := []prompt.BootstrapEntry{
		{Source: "thread pool", Target: "并发池"},
	}
	translations := map[string]string{
		"1": "并发池是一种常见模式。",
		"2": "另一个段提到并发池时也应同步。",
	}
	h.absorbInlineGlossary(context.Background(), entries, translations, "zh", quietLogger())

	for id, want := range map[string]string{
		"1": "线程池是一种常见模式。",
		"2": "另一个段提到线程池时也应同步。",
	} {
		if got := translations[id]; got != want {
			t.Errorf("translations[%s] = %q, want %q", id, got, want)
		}
	}
	hits, _ := g.Lookup(context.Background(), "thread pool here", "", "")
	if len(hits) != 1 || hits[0].Target != "线程池" {
		t.Errorf("authoritative target should remain 线程池，got %#v", hits)
	}
}

// TestAbsorbInlineGlossary_StrategyOffKeepsConflict 验证 off 策略保留旧行为。
func TestAbsorbInlineGlossary_StrategyOffKeepsConflict(t *testing.T) {
	g := glossary.NewMemory()
	_, _ = g.Add(context.Background(), glossary.Entry{Source: "thread pool", Target: "线程池"})
	h := &TranslateHandler{
		Glossary:               g,
		InlineBootstrap:        true,
		MinBootstrapSourceLen:  2,
		MaxTermsPer1000Chars:   3.0,
		InlineConflictStrategy: InlineConflictOff,
	}
	entries := []prompt.BootstrapEntry{
		{Source: "thread pool", Target: "并发池"},
	}
	translations := map[string]string{"1": "并发池保留原样。"}
	h.absorbInlineGlossary(context.Background(), entries, translations, "zh", quietLogger())
	if translations["1"] != "并发池保留原样。" {
		t.Errorf("strategy=off should not rewrite, got %q", translations["1"])
	}
}

// TestAbsorbInlineGlossary_NoConflictNoChange 没有冲突时 translations 不应被动。
func TestAbsorbInlineGlossary_NoConflictNoChange(t *testing.T) {
	g := glossary.NewMemory()
	h := &TranslateHandler{
		Glossary:               g,
		InlineBootstrap:        true,
		MinBootstrapSourceLen:  2,
		MaxTermsPer1000Chars:   3.0,
		InlineConflictStrategy: InlineConflictRewriteLocal,
	}
	entries := []prompt.BootstrapEntry{
		{Source: "thread pool", Target: "线程池"},
	}
	translations := map[string]string{"1": "线程池入门。"}
	h.absorbInlineGlossary(context.Background(), entries, translations, "zh", quietLogger())
	if translations["1"] != "线程池入门。" {
		t.Errorf("no conflict should not rewrite, got %q", translations["1"])
	}
}

// TestAbsorbInlineGlossary_SameTargetIsNoop 验证同 target 不进 Skipped，不会误改译文。
func TestAbsorbInlineGlossary_SameTargetIsNoop(t *testing.T) {
	g := glossary.NewMemory()
	_, _ = g.Add(context.Background(), glossary.Entry{Source: "thread pool", Target: "线程池"})
	h := &TranslateHandler{
		Glossary:               g,
		InlineBootstrap:        true,
		MinBootstrapSourceLen:  2,
		MaxTermsPer1000Chars:   3.0,
		InlineConflictStrategy: InlineConflictRewriteLocal,
	}
	entries := []prompt.BootstrapEntry{
		{Source: "thread pool", Target: "线程池"},
	}
	translations := map[string]string{"1": "线程池上线。"}
	h.absorbInlineGlossary(context.Background(), entries, translations, "zh", quietLogger())
	if translations["1"] != "线程池上线。" {
		t.Errorf("identical target should noop, got %q", translations["1"])
	}
}

// TestAbsorbInlineGlossary_ProposedTargetMissingInTranslations 译文里找不到冲突 target 时不 panic 也不报错。
func TestAbsorbInlineGlossary_ProposedTargetMissingInTranslations(t *testing.T) {
	g := glossary.NewMemory()
	_, _ = g.Add(context.Background(), glossary.Entry{Source: "thread pool", Target: "线程池"})
	h := &TranslateHandler{
		Glossary:               g,
		InlineBootstrap:        true,
		MinBootstrapSourceLen:  2,
		MaxTermsPer1000Chars:   3.0,
		InlineConflictStrategy: InlineConflictRewriteLocal,
	}
	entries := []prompt.BootstrapEntry{
		{Source: "thread pool", Target: "并发池"},
	}
	translations := map[string]string{"1": "本段未提到该术语。"}
	h.absorbInlineGlossary(context.Background(), entries, translations, "zh", quietLogger())
	if translations["1"] != "本段未提到该术语。" {
		t.Errorf("text without target should be unchanged, got %q", translations["1"])
	}
}

// ---------- 集成测试：partial recovery / normalize 救回 / L4 升级重试 ----------

// countingReporter 计算 SegmentDone 调用次数。
type countingReporter struct {
	stageStartCalls int32
	segmentDones    int32
	stageDoneCalls  int32
	batchCompletes  int32
}

func (r *countingReporter) StageStart(string, int) { atomic.AddInt32(&r.stageStartCalls, 1) }
func (r *countingReporter) SegmentDone()           { atomic.AddInt32(&r.segmentDones, 1) }
func (r *countingReporter) BatchComplete()         { atomic.AddInt32(&r.batchCompletes, 1) }
func (r *countingReporter) StageDone()             { atomic.AddInt32(&r.stageDoneCalls, 1) }
func (r *countingReporter) Close() error           { return nil }

// testSystemTmpl 是测试用的最小系统模板。
const testSystemTmpl = `你是 LinguaFlow，一个专业的翻译引擎。
将用户的文本从 {{.SourceLang}} 翻译为 {{.TargetLang}}。
协议：
- segments 中每个条目包含 "source" 和 "translate"。
- 你的回复必须是一个 JSON 对象：{"translations":{"<id>":"<翻译>", ...}}，仅输出 JSON。
- 仅翻译 translate=true 的段落。`

func newTestRenderer(t *testing.T) *prompt.Renderer {
	t.Helper()
	r, err := prompt.NewRenderer(testSystemTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	return r
}

func newTestDoc(n int) *Document {
	segs := make([]Segment, n)
	for i := 0; i < n; i++ {
		segs[i] = Segment{
			ID:        "seg-" + itoa(i),
			Source:    "source-" + itoa(i),
			Translate: true,
		}
	}
	return &Document{
		Segments:   segs,
		SourceLang: "en",
		TargetLang: "zh",
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func defaultRepairOpts() repair.Options {
	return repair.Options{
		JSONStructural:       true,
		SchemaAliases:        true,
		PlaceholderNormalize: true,
		PromptUpgrade:        true,
	}
}

// newTestTranslateHandler 创建测试用 TranslateHandler。
func newTestTranslateHandler(fb backend.Backend, batchSize, concurrency int, opts ...func(*TranslateHandler)) *TranslateHandler {
	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: batchSize,
		Renderer:  nil, // 由调用方设置
		Logger:    quietLogger(),
		Repair:    defaultRepairOpts(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// runTestTranslateRound 使用 TranslateHandler + RunRound 执行翻译。
func runTestTranslateRound(t *testing.T, h *TranslateHandler, doc *Document, concurrency ...int) error {
	t.Helper()
	if h.Renderer == nil {
		h.Renderer = newTestRenderer(t)
	}
	conc := 1
	if len(concurrency) > 0 {
		conc = concurrency[0]
	}
	round := Round{
		Concurrency: conc,
		Retry:       h.Retry,
		Shrink:      h.FallbackShrink,
		Handler:     h,
	}
	_, err := RunRound(context.Background(), round, doc, nil, h.Logger, h.Reporter)
	return err
}

// TestProcessBatch_MissingIDsAdvanceToNextPool 缺失 ID 写回成功段后，
// 缺失段进下一池按缩放大小重切补救（不再走 round 级 missing 重试）。
func TestProcessBatch_MissingIDsAdvanceToNextPool(t *testing.T) {
	doc := newTestDoc(4)
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"a","2":"b","3":"c"}}`,
			`{"translations":{"1":"d"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 4, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	for i, want := range []string{"a", "b", "c", "d"} {
		if got := doc.Segments[i].Target; got != want {
			t.Errorf("seg %d: target=%q want %q", i, got, want)
		}
	}
	if got := int(fb.idx.Load()); got != 2 {
		t.Errorf("backend calls: %d want 2 (pool0 batch + pool1 missing)", got)
	}
	if got := atomic.LoadInt32(&rep.segmentDones); got != 4 {
		t.Errorf("SegmentDone calls=%d want 4 (no double-count)", got)
	}
	if got := atomic.LoadInt32(&rep.stageStartCalls); got != 1 {
		t.Errorf("StageStart calls=%d want 1", got)
	}
	if got := atomic.LoadInt32(&rep.stageDoneCalls); got != 1 {
		t.Errorf("StageDone calls=%d want 1", got)
	}
}

// TestProcessBatch_HighMissingRateStillUsesBestPartial 高缺失率时仍保留已成功段，
// 缺失段进下一池补救。
func TestProcessBatch_HighMissingRateStillUsesBestPartial(t *testing.T) {
	doc := newTestDoc(4)
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"a"}}`,
			// pool1 MaxSegments=floor(4*0.5)=2 → batches [1,2] and [3]
			`{"translations":{"1":"x1","2":"x2"}}`,
			`{"translations":{"1":"x3"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 4, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := int(fb.idx.Load()); got != 3 {
		t.Errorf("backend calls: %d want 3 (pool0 + 2 pool1 batches)", got)
	}
	for i, want := range []string{"a", "x1", "x2", "x3"} {
		if got := doc.Segments[i].Target; got != want {
			t.Errorf("seg %d: target=%q want %q", i, got, want)
		}
	}
	if got := atomic.LoadInt32(&rep.segmentDones); got != 4 {
		t.Errorf("SegmentDone calls=%d want 4", got)
	}
}

// TestProcessBatch_PlaceholderNormalizeAvoidsRetry 占位符变体被 normalize 修复后，
// 不应触发占位符补救重试（不应新增 LLM 调用）。
func TestProcessBatch_PlaceholderNormalizeAvoidsRetry(t *testing.T) {
	doc := newTestDoc(1)
	doc.Segments[0].Protected = map[string]string{"__LF_000001__": "<code>"}
	doc.Segments[0].Source = "hello __LF_000001__"

	rep := &countingReporter{}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"你好 __lf_000001__"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 1, 1, func(h *TranslateHandler) {
		h.Reporter = rep
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := int(fb.idx.Load()); got != 1 {
		t.Errorf("backend calls: %d want 1 (normalize should avoid second call)", got)
	}
	if doc.Segments[0].Target != "你好 __LF_000001__" {
		t.Errorf("target normalize failed: %q", doc.Segments[0].Target)
	}
}

// TestProcessBatch_PromptUpgradeRecovers 第一次返回 fatal JSON，第二次返回合法。
func TestProcessBatch_PromptUpgradeRecovers(t *testing.T) {
	doc := newTestDoc(2)
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"I don't want to follow JSON schema today",
			`{"translations":{"1":"a","2":"b"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc.Segments[0].Target != "a" || doc.Segments[1].Target != "b" {
		t.Errorf("targets: %q, %q", doc.Segments[0].Target, doc.Segments[1].Target)
	}
	if got := int(fb.idx.Load()); got != 2 {
		t.Errorf("backend calls: %d want 2 (1 fatal + 1 upgrade-retry)", got)
	}
}

// TestProcessBatch_PromptUpgradeDisabledAdvancesPool 升级重试关闭时，fatal JSON 整批进下一池。
func TestProcessBatch_PromptUpgradeDisabledAdvancesPool(t *testing.T) {
	doc := newTestDoc(2)
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"not json",
			`{"translations":{"1":"x0"}}`, // pool1 batch size floor(2*0.5)=1
			`{"translations":{"1":"x1"}}`,
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := int(fb.idx.Load()); got < 2 {
		t.Errorf("backend calls: %d want >= 2 (parse fail + next pool)", got)
	}
	if doc.Segments[0].Target != "x0" {
		t.Errorf("seg0=%q want x0", doc.Segments[0].Target)
	}
	if doc.Segments[1].Target != "x1" {
		t.Errorf("seg1=%q want x1", doc.Segments[1].Target)
	}
}

func TestTranslatePlan_UsesLongestContinuousRunsAndPoolRetry(t *testing.T) {
	doc := newTestDoc(7)
	doc.Segments[3].Skip = true
	doc.Segments[3].Source = "skipped"

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"a0","2":"a1","3":"a2"}}`,
			`{"translations":{"1":"b4","2":"b5"}}`, // missing id 3 → seg 6 unresolved
			`{"translations":{"1":"c6"}}`,          // pool1 rebatch of missing
		},
	}
	h := newTestTranslateHandler(fb, 3, 1, func(h *TranslateHandler) {
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	for i, want := range []string{"a0", "a1", "a2", "skipped", "b4", "b5", "c6"} {
		if got := doc.Segments[i].Target; got != want {
			t.Fatalf("seg %d target=%q want %q", i, got, want)
		}
	}
	if got := int(fb.idx.Load()); got != 3 {
		t.Fatalf("backend calls=%d want 3", got)
	}
	if len(fb.requests) < 3 {
		t.Fatalf("requests=%d want >=3", len(fb.requests))
	}
	if !strings.Contains(fb.requests[0].User, "source-0") || !strings.Contains(fb.requests[0].User, "source-2") {
		t.Fatalf("first request should contain first continuous run, got %q", fb.requests[0].User)
	}
	if strings.Contains(fb.requests[0].User, "source-4") {
		t.Fatalf("first request should not mix separated runs, got %q", fb.requests[0].User)
	}
	if !strings.Contains(fb.requests[2].User, "source-6") {
		t.Fatalf("third request should be pool1 missing rebatch, got %q", fb.requests[2].User)
	}
}

func TestTranslatePlan_ExhaustedRoundsKeepSource(t *testing.T) {
	doc := newTestDoc(2)
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"ok"}}`,
			"",
		},
	}
	h := newTestTranslateHandler(fb, 2, 1)
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc.Segments[0].Target != "ok" {
		t.Fatalf("seg0=%q want ok", doc.Segments[0].Target)
	}
	if doc.Segments[1].Target != "" {
		t.Fatalf("seg1=%q want empty (failed segment keeps empty target)", doc.Segments[1].Target)
	}
	if v, ok := doc.Vars["_translate_failed_indices"]; !ok {
		t.Fatal("expected _translate_failed_indices to be set")
	} else if s, ok := v.(string); !ok || s != "1" {
		t.Fatalf("_translate_failed_indices=%v want \"1\"", v)
	}
}

func TestIsPlaceholderOnly(t *testing.T) {
	tests := []struct {
		name string
		seg  Segment
		want bool
	}{
		{
			name: "single placeholder only",
			seg: Segment{
				Source:    "__LF_000001__",
				Protected: map[string]string{"__LF_000001__": "<br/>"},
			},
			want: true,
		},
		{
			name: "multiple placeholders with whitespace",
			seg: Segment{
				Source: "__LF_000001__ \n __LF_000002__",
				Protected: map[string]string{
					"__LF_000001__": "<br/>",
					"__LF_000002__": "<br/>",
				},
			},
			want: true,
		},
		{
			name: "empty source",
			seg: Segment{
				Source:    "",
				Protected: map[string]string{"__LF_000001__": "<br/>"},
			},
			want: true,
		},
		{
			name: "whitespace-only source with placeholder in protected",
			seg: Segment{
				Source:    "   ",
				Protected: map[string]string{"__LF_000001__": "<br/>"},
			},
			want: true,
		},
		{
			name: "placeholder mixed with text",
			seg: Segment{
				Source:    "Hello __LF_000001__",
				Protected: map[string]string{"__LF_000001__": "<br/>"},
			},
			want: false,
		},
		{
			name: "plain text without placeholders",
			seg: Segment{
				Source:    "Hello World",
				Protected: map[string]string{},
			},
			want: false,
		},
		{
			name: "nil protected map",
			seg: Segment{
				Source: "__LF_000001__",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPlaceholderOnly(&tt.seg); got != tt.want {
				t.Errorf("IsPlaceholderOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsDecorativeSeparator(t *testing.T) {
	tests := []struct {
		name string
		seg  Segment
		want bool
	}{
		{
			name: "decorative diamond separators",
			seg:  Segment{Source: "◇ ◇ ◇ ◇"},
			want: true,
		},
		{
			name: "decorative asterisk separators",
			seg:  Segment{Source: "* * *"},
			want: true,
		},
		{
			name: "decorative em-dash separators",
			seg:  Segment{Source: "— — —"},
			want: true,
		},
		{
			name: "decorative star separators",
			seg:  Segment{Source: "★ ★ ★"},
			want: true,
		},
		{
			name: "decorative circle separators",
			seg:  Segment{Source: "● ● ●"},
			want: true,
		},
		{
			name: "decorative tilde separators",
			seg:  Segment{Source: "~ ~ ~"},
			want: true,
		},
		{
			name: "decorative reference mark separators",
			seg:  Segment{Source: "※ ※ ※"},
			want: true,
		},
		{
			name: "plain text not separator",
			seg:  Segment{Source: "Hello"},
			want: false,
		},
		{
			name: "japanese text not separator",
			seg:  Segment{Source: "名前の呼び方と。"},
			want: false,
		},
		{
			name: "chapter with digit not separator",
			seg:  Segment{Source: "第1章"},
			want: false,
		},
		{
			name: "empty string not separator",
			seg:  Segment{Source: ""},
			want: false,
		},
		{
			name: "whitespace only not separator",
			seg:  Segment{Source: "   "},
			want: false,
		},
		{
			name: "mixed text and symbols not separator",
			seg:  Segment{Source: "Hello ◇ ◇"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDecorativeSeparator(&tt.seg); got != tt.want {
				t.Errorf("IsDecorativeSeparator() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShrinkConstraint_Curve(t *testing.T) {
	orig := BatchConstraint{MaxSegments: 100, MaxWords: 1000}
	// pool 0
	c0 := shrinkConstraint(orig, 0.9, 0)
	if c0.MaxSegments != 100 || c0.MaxWords != 1000 {
		t.Fatalf("pool0: %+v", c0)
	}
	// pool 1: floor(100*0.9)=90, floor(1000*0.9)=900
	c1 := shrinkConstraint(orig, 0.9, 1)
	if c1.MaxSegments != 90 || c1.MaxWords != 900 {
		t.Fatalf("pool1: %+v want 90/900", c1)
	}
	// pool 2: floor(100*0.81)=81
	c2 := shrinkConstraint(orig, 0.9, 2)
	if c2.MaxSegments != 81 {
		t.Fatalf("pool2 MaxSegments=%d want 81", c2.MaxSegments)
	}
	// pool 3: floor(100*0.729)=72
	c3 := shrinkConstraint(orig, 0.9, 3)
	if c3.MaxSegments != 72 {
		t.Fatalf("pool3 MaxSegments=%d want 72", c3.MaxSegments)
	}
	// clamp to 1
	tiny := shrinkConstraint(BatchConstraint{MaxSegments: 2}, 0.5, 5)
	if tiny.MaxSegments != 1 {
		t.Fatalf("clamp: %d want 1", tiny.MaxSegments)
	}
	// shrink disabled
	off := shrinkConstraint(orig, 0, 2)
	if off.MaxSegments != 100 {
		t.Fatalf("shrink=0 should keep orig: %+v", off)
	}
}

// TestPoolModel_ParseFailureAdvancesAndReleasesSeat 解析失败整批进下一池，不在原席位滚退化链。
func TestPoolModel_ParseFailureAdvancesAndReleasesSeat(t *testing.T) {
	doc := newTestDoc(4)
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"not-json-at-all", // pool0: parse fail → unresolved whole batch
			// pool1 MaxSegments=2；concurrency=1 保证响应顺序确定
			`{"translations":{"1":"a","2":"b"}}`,
			`{"translations":{"1":"c","2":"d"}}`,
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 4, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 2}
	})
	if err := runTestTranslateRound(t, h, doc, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	for i, want := range []string{"a", "b", "c", "d"} {
		if got := doc.Segments[i].Target; got != want {
			t.Errorf("seg %d: target=%q want %q", i, got, want)
		}
	}
	if got := int(fb.idx.Load()); got != 3 {
		t.Errorf("backend calls=%d want 3 (1 fail + 2 pool1 batches)", got)
	}
	if got := atomic.LoadInt32(&rep.segmentDones); got != 4 {
		t.Errorf("SegmentDone=%d want 4", got)
	}
	if got := atomic.LoadInt32(&rep.stageStartCalls); got != 1 {
		t.Errorf("StageStart=%d want 1", got)
	}
}

// TestPoolModel_5xxInFlightRetry 5xx 走池内 in-flight backoff，不进下一池缩批。
func TestPoolModel_5xxInFlightRetry(t *testing.T) {
	doc := newTestDoc(2)
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		errs: []error{
			&backend.StatusError{StatusCode: 500, Err: errors.New("internal")},
			nil,
		},
		responses: []string{
			"", // first call fails via errs
			`{"translations":{"1":"a","2":"b"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 2, Backoff: 0} // backoff floored to minRateLimitBackoff
	})

	// Override min backoff path: use short context... actually backoffDuration enforces 5s min.
	// Speed up by setting MaxAttempts and accepting wait — too slow for unit test.
	// Instead call ProcessBatch directly and assert retry path without full RunRound wait.
	h.Renderer = newTestRenderer(t)
	result := h.ProcessBatch(context.Background(), doc, []int{0, 1}, 0, quietLogger())
	if result.retry == nil {
		t.Fatal("expected in-flight retry for 500, got no retry")
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("5xx must not unresolved/shrink, got unresolved=%v", result.unresolved)
	}
	if result.retry.attempt != 1 {
		t.Fatalf("retry.attempt=%d want 1", result.retry.attempt)
	}
	if len(result.retry.idxs) != 2 {
		t.Fatalf("retry must keep same batch size, got %d", len(result.retry.idxs))
	}
}

// TestPoolModel_429InFlightRetry 429 行为不变：in-flight backoff。
func TestPoolModel_429InFlightRetry(t *testing.T) {
	doc := newTestDoc(2)
	h := newTestTranslateHandler(&fakeBackend{
		name: "fake",
		errs: []error{&backend.StatusError{StatusCode: 429, Err: errors.New("rate limited")}},
	}, 2, 1, func(h *TranslateHandler) {
		h.Retry = backend.RetryPolicy{MaxAttempts: 2, Backoff: 0}
	})
	h.Renderer = newTestRenderer(t)
	result := h.ProcessBatch(context.Background(), doc, []int{0, 1}, 0, quietLogger())
	if result.retry == nil {
		t.Fatal("expected in-flight retry for 429")
	}
	if len(result.unresolved) != 0 {
		t.Fatalf("429 must not unresolved, got %v", result.unresolved)
	}
}

// TestPoolModel_NetworkErrorInFlightRetry 无 HTTPStatus 的网络错误走 IsRetryable in-flight。
func TestPoolModel_NetworkErrorInFlightRetry(t *testing.T) {
	doc := newTestDoc(1)
	h := newTestTranslateHandler(&fakeBackend{
		name: "fake",
		errs: []error{errors.New("dial tcp: i/o timeout")},
	}, 1, 1, func(h *TranslateHandler) {
		h.Retry = backend.RetryPolicy{MaxAttempts: 2, Backoff: 0}
	})
	h.Renderer = newTestRenderer(t)
	result := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if result.retry == nil {
		t.Fatal("expected in-flight retry for network error")
	}
}

// TestPoolModel_FinalUnresolvedAfterPoolsExhausted 池耗尽后 Finalize 写 failed indices。
func TestPoolModel_FinalUnresolvedAfterPoolsExhausted(t *testing.T) {
	doc := newTestDoc(2)
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"bad", // pool0 parse fail
			"bad", // pool1
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Repair = opts
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 1} // maxPools=2
	})
	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc.Segments[0].Target != "" || doc.Segments[1].Target != "" {
		t.Fatalf("targets should stay empty, got %q %q", doc.Segments[0].Target, doc.Segments[1].Target)
	}
	v, ok := doc.Vars["_translate_failed_indices"]
	if !ok {
		t.Fatal("expected _translate_failed_indices")
	}
	s, _ := v.(string)
	if s != "0,1" && s != "0, 1" {
		// Finalize joins with comma no space
		if s != "0,1" {
			t.Fatalf("_translate_failed_indices=%q want 0,1", s)
		}
	}
}
