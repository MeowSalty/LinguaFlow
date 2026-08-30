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
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
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

// structuralTestRules 是与 config 默认一致的四条保护规则。
var structuralTestRules = []string{"code", "link", "placeholder", "xml"}

// TestBuildBatches_StructuralMarkingAndContextExclusion 验证池 0 结构段标记与消费：
// 全部段落（含 Translate=false 的上下文候选段）被统一标记；结构段（纯占位符/纯标点/
// 占位符+标点混合）不送翻、原文透传、Source 保持原始且 Protected 为空；
// 上下文扩展排除所有结构段——包括 Translate=false 且从未被 Protect 的段
// （回归：旧实现依赖 seg.Protected 判定纯占位符，此类段会以裸文本混入上下文）。
func TestBuildBatches_StructuralMarkingAndContextExclusion(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{ID: "seg-0", Source: "Hello world", Translate: false},
			{ID: "seg-1", Source: "<br/>", Translate: false},
			{ID: "seg-2", Source: "……{{name}}", Translate: true},
			{ID: "seg-3", Source: "Translate me", Translate: true},
			{ID: "seg-4", Source: "◇◇◇", Translate: false},
		},
		SourceLang: "en",
		TargetLang: "zh",
	}
	h := &TranslateHandler{
		Logger:    quietLogger(),
		Protector: protect.FromRules(structuralTestRules),
	}

	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}

	// 标记覆盖全部段落：结构段为 true，普通文本段为 false。
	wantFlags := []bool{false, true, true, false, true}
	for i, want := range wantFlags {
		if got := doc.Segments[i].StructuralOnly; got != want {
			t.Errorf("seg[%d].StructuralOnly = %v, want %v", i, got, want)
		}
	}

	// 混合结构段（翻译目标）：原文透传，不进批次；Source 保持原始、Protected 为空
	// （旧实现会先 Protect 再回退，留下 key 化 Source 与已填充 Protected 的副作用状态）。
	seg2 := &doc.Segments[2]
	if seg2.Target != "……{{name}}" {
		t.Errorf("structural segment Target = %q, want raw passthrough", seg2.Target)
	}
	if seg2.Source != "……{{name}}" {
		t.Errorf("structural segment Source = %q, want unchanged raw text", seg2.Source)
	}
	if len(seg2.Protected) != 0 {
		t.Errorf("structural segment Protected = %v, want empty", seg2.Protected)
	}

	// 仅一个真实翻译段进批次；跳过计数只含 seg-2（Translate=false 的段不计数，与旧行为一致）。
	if len(batches) != 1 || len(batches[0]) != 1 || batches[0][0] != 3 {
		t.Fatalf("batches = %v, want single batch [3]", batches)
	}
	if got := doc.Vars["_skipped_count"]; got != 1 {
		t.Errorf("_skipped_count = %v, want 1", got)
	}

	// 上下文扩展（窗口 3 覆盖全文档）：普通邻段入选，全部结构段排除——
	// 关键是 seg-1（<br/>、Translate=false、从未 Protect）：旧实现会把它裸混入上下文。
	expanded := ExpandBatchWithContext(doc, batches[0], len(doc.Segments), 3, 0)
	if !reflect.DeepEqual(expanded.Idxs, []int{0, 3}) {
		t.Errorf("expanded idxs = %v, want [0 3] (structural neighbors excluded)", expanded.Idxs)
	}

	// 池 ≥1（pending!=nil）复用池 0 标记：不重扫不重算，标记不漂移。
	pool1, err := h.BuildBatches(context.Background(), doc, []int{3}, 1)
	if err != nil {
		t.Fatalf("BuildBatches pool 1: %v", err)
	}
	if len(pool1) != 1 || pool1[0][0] != 3 {
		t.Fatalf("pool 1 batches = %v, want single batch [3]", pool1)
	}
	pool1Expanded := ExpandBatchWithContext(doc, pool1[0], len(doc.Segments), 3, 0)
	if !reflect.DeepEqual(pool1Expanded.Idxs, expanded.Idxs) {
		t.Errorf("pool 1 expanded idxs = %v, want same as pool 0: %v", pool1Expanded.Idxs, expanded.Idxs)
	}
}

// TestTranslateRound_StructuralSegments_PassthroughAndPromptHygiene 全链路验证：
// 混合结构段既不出现在任何发往后端的请求中（无论批次段还是上下文段），又以原文透传；
// 批次段在 prompt 中保持 key 化保护形态、上下文段展示保护前原文
// （钉死 buildRequest 的 isCtx 分支语义，防止机械替换 rawSource 后裸文本直发 LLM）。
func TestTranslateRound_StructuralSegments_PassthroughAndPromptHygiene(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{ID: "seg-0", Source: "Hello {{name}}", Translate: true},
			{ID: "seg-1", Source: "plain", Translate: true},
			{ID: "seg-2", Source: "……{{name}}", Translate: true},
			{ID: "seg-3", Source: "Thanks a lot", Translate: true},
		},
		SourceLang: "en",
		TargetLang: "zh",
	}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"你好 __LF_000001__"}}`,
			`{"translations":{"2":"平淡"}}`,
			`{"translations":{"1":"多谢"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 1, 1, func(h *TranslateHandler) {
		h.Protector = protect.FromRules(structuralTestRules)
		h.Context = ContextConfig{Enabled: true, Before: 1, After: 1}
	})

	if err := runTestTranslateRound(t, h, doc); err != nil {
		t.Fatalf("runTestTranslateRound: %v", err)
	}

	if len(fb.requests) != 3 {
		t.Fatalf("backend requests = %d, want 3", len(fb.requests))
	}
	for i, req := range fb.requests {
		if strings.Contains(req.User, "……{{name}}") {
			t.Errorf("request[%d] contains structural segment text", i)
		}
	}
	// 批次 [0]（含上下文 seg-1）：批次段必须是 key 化保护形态，而非裸占位符语法。
	if !strings.Contains(fb.requests[0].User, "Hello __LF_000001__") {
		t.Errorf("request[0] should contain protected batch source, got: %s", fb.requests[0].User)
	}
	if strings.Contains(fb.requests[0].User, "Hello {{name}}") {
		t.Errorf("request[0] leaked raw placeholder syntax for batch segment")
	}
	// 批次 [1]（含上下文 seg-0）：上下文段必须是保护前原文。
	if !strings.Contains(fb.requests[1].User, "Hello {{name}}") {
		t.Errorf("request[1] should contain raw context source, got: %s", fb.requests[1].User)
	}
	if strings.Contains(fb.requests[1].User, "Hello __LF_") {
		t.Errorf("request[1] context segment should not be key-ified")
	}

	// 混合结构段原文透传；其余段完成翻译并还原占位符。
	if got := doc.Segments[2].Target; got != "……{{name}}" {
		t.Errorf("structural segment Target = %q, want raw passthrough", got)
	}
	if got := doc.Segments[0].Target; got != "你好 {{name}}" {
		t.Errorf("seg[0].Target = %q, want %q", got, "你好 {{name}}")
	}
	if got := doc.Vars["_skipped_count"]; got != 1 {
		t.Errorf("_skipped_count = %v, want 1", got)
	}
}

// countingProtector 统计 Protect 调用次数，钉死保护链对每段恰好执行一次。
type countingProtector struct {
	protect.Protector
	calls int
}

func (c *countingProtector) Protect(seg *Segment) error {
	c.calls++
	return c.Protector.Protect(seg)
}

// failingProtector 保护链恒失败，验证分析失败在落盘点以与直接 Protect 失败
// 相同的语义报错（fail-loud，不静默裸发未保护文本）。
type failingProtector struct{}

func (failingProtector) Name() string             { return "failing" }
func (failingProtector) Unprotect(*Segment) error { return nil }
func (failingProtector) Protect(*Segment) error   { return errors.New("protect failed") }

// TestBuildBatches_ProtectChainSingleExecution 验证保护链单次执行：
// 池 0 的结构段标记分析与 Protect 落盘是同一次链执行的缓存复用，
// 保护链对每个非 Skip 段恰好执行一次；Skip 段零执行（其标记从不被消费）。
func TestBuildBatches_ProtectChainSingleExecution(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{ID: "seg-0", Source: "Hello {{name}}", Translate: true},
			{ID: "seg-1", Source: "plain text", Translate: true},
			{ID: "seg-2", Source: "……{{name}}", Translate: true},
			{ID: "seg-3", Source: "ctx candidate", Translate: false},
			{ID: "seg-4", Source: "<br/>", Translate: false},
			{ID: "seg-5", Source: "[Events] big skip header", Translate: true, Skip: true},
		},
		SourceLang: "en",
		TargetLang: "zh",
	}
	counter := &countingProtector{Protector: protect.FromRules(structuralTestRules)}
	h := &TranslateHandler{
		Logger:    quietLogger(),
		Protector: counter,
		BatchSize: 10,
	}

	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}

	// 5 个非 Skip 段各恰好一次：翻译目标 seg-0/1/2 + 上下文候选 seg-3/4；Skip 段 seg-5 零次。
	if counter.calls != 5 {
		t.Errorf("protect chain executions = %d, want 5 (once per non-skip segment)", counter.calls)
	}

	// 落盘结果与直接 Protect 等价：seg-0 Source key 化、Protected 填充、OriginalSource 快照。
	seg0 := &doc.Segments[0]
	if seg0.Source == "Hello {{name}}" || !strings.Contains(seg0.Source, "__LF_") {
		t.Errorf("seg[0].Source = %q, want key-ified", seg0.Source)
	}
	if len(seg0.Protected) == 0 {
		t.Errorf("seg[0].Protected empty, want mapping applied")
	}
	if seg0.OriginalSource != "Hello {{name}}" {
		t.Errorf("seg[0].OriginalSource = %q, want raw snapshot", seg0.OriginalSource)
	}

	// 结构段不落盘：Source 保持原始、Protected 为空。
	seg2 := &doc.Segments[2]
	if seg2.Source != "……{{name}}" || len(seg2.Protected) != 0 {
		t.Errorf("structural seg[2] mutated: Source=%q Protected=%v", seg2.Source, seg2.Protected)
	}

	// Skip 段不标记（零值 fail-open 契约）；批内仅两个非结构翻译段。
	if doc.Segments[5].StructuralOnly {
		t.Errorf("skip segment should stay unmarked")
	}
	if len(batches) != 1 || !reflect.DeepEqual(batches[0], []int{0, 1}) {
		t.Fatalf("batches = %v, want single batch [0 1]", batches)
	}
}

// TestBuildBatches_ProtectErrorPropagates 验证标记分析阶段的保护链失败
// 在 Protect 落盘点报错（与旧"循环内直接 Protect 失败"同语义），不静默跳过保护。
func TestBuildBatches_ProtectErrorPropagates(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{ID: "seg-0", Source: "Hello {{name}}", Translate: true},
			{ID: "seg-1", Source: "ctx", Translate: false},
		},
		SourceLang: "en",
		TargetLang: "zh",
	}
	h := &TranslateHandler{
		Logger:    quietLogger(),
		Protector: failingProtector{},
		BatchSize: 10,
	}

	_, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err == nil {
		t.Fatalf("BuildBatches err = nil, want protect error")
	}
	if !strings.Contains(err.Error(), "protect segment 0") {
		t.Errorf("err = %v, want protect segment error", err)
	}
}

// TestBuildBatches_RubyChainMetaPreserved 验证保护链单次执行优化不丢链的段级
// 副作用（回归：启用 ruby 的链经 AnalyzeStructural 临时段执行时 ruby_items 被丢弃，
// prompt 注音标注与译后注音回填整体静默失效）。
func TestBuildBatches_RubyChainMetaPreserved(t *testing.T) {
	doc := &Document{
		Segments: []Segment{
			{ID: "seg-0", Source: "<ruby>漢<rt>かん</rt></ruby>です", Translate: true},
			{ID: "seg-1", Source: "<ruby>字<rt>じ</rt></ruby>と {{name}}", Translate: true},
			{ID: "seg-2", Source: "plain", Translate: true},
		},
		SourceLang: "ja",
		TargetLang: "zh",
	}
	h := &TranslateHandler{
		Logger:    quietLogger(),
		Protector: protect.Compose(protect.NewRubyProtector(), protect.FromRules(structuralTestRules)),
		BatchSize: 10,
	}

	// 与直接 Protect 的落盘终态逐一对照：Source 剥离/映射、Protected、Meta 副作用。
	wantDoc := &Document{
		Segments: []Segment{
			{ID: "seg-0", Source: "<ruby>漢<rt>かん</rt></ruby>です"},
			{ID: "seg-1", Source: "<ruby>字<rt>じ</rt></ruby>と {{name}}"},
			{ID: "seg-2", Source: "plain"},
		},
	}
	for i := range wantDoc.Segments {
		if err := h.Protector.Protect(&wantDoc.Segments[i]); err != nil {
			t.Fatalf("Protect want[%d]: %v", i, err)
		}
	}

	if _, err := h.BuildBatches(context.Background(), doc, nil, 0); err != nil {
		t.Fatalf("BuildBatches: %v", err)
	}

	for i := range doc.Segments {
		got, want := &doc.Segments[i], &wantDoc.Segments[i]
		if got.Source != want.Source {
			t.Errorf("seg[%d].Source = %q, want %q", i, got.Source, want.Source)
		}
		if !reflect.DeepEqual(got.Protected, want.Protected) {
			t.Errorf("seg[%d].Protected = %v, want %v", i, got.Protected, want.Protected)
		}
		if !reflect.DeepEqual(got.Meta, want.Meta) {
			t.Errorf("seg[%d].Meta = %v, want %v", i, got.Meta, want.Meta)
		}
	}
	// 仅含 ruby 标签的段有 ruby_items（无注音段不写 Meta 是 ruby protector 的既有约定）。
	for _, i := range []int{0, 1} {
		if _, ok := doc.Segments[i].Meta["ruby_items"]; !ok {
			t.Errorf("seg[%d].Meta missing ruby_items (ruby annotations would be lost)", i)
		}
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
