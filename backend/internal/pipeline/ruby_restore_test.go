package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// newRubyTestSeg 构造带 ruby_items 元数据的测试段落。
func newRubyTestSeg(source, target string, items []ruby.Item) *Segment {
	return &Segment{
		ID:             "s1",
		Source:         source,
		OriginalSource: source,
		Target:         target,
		Meta:           map[string]any{"ruby_items": items},
	}
}

func TestKindSet(t *testing.T) {
	cases := []struct {
		name  string
		kinds []string
		want  map[string]bool
	}{
		{
			name:  "all kinds",
			kinds: []string{"phonetic", "semantic", "creative"},
			want:  map[string]bool{"phonetic": true, "semantic": true, "creative": true},
		},
		{
			name:  "single kind",
			kinds: []string{"creative"},
			want:  map[string]bool{"creative": true},
		},
		{
			name:  "empty non-nil list returns empty set (user opts out)",
			kinds: []string{},
			want:  map[string]bool{},
		},
		{
			name:  "nil list defaults to all kinds (backward compat)",
			kinds: nil,
			want:  map[string]bool{"phonetic": true, "semantic": true, "creative": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kindSet(tc.kinds)
			if len(got) != len(tc.want) {
				t.Fatalf("kindSet(%v) = %v, want %v", tc.kinds, got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("kindSet(%v)[%q] = %v, want %v", tc.kinds, k, got[k], v)
				}
			}
		})
	}
}

func TestFilterByKinds(t *testing.T) {
	allKinds := map[string]bool{"phonetic": true, "semantic": true, "creative": true}
	creativeOnly := map[string]bool{"creative": true}
	emptySet := map[string]bool{}

	output := []ruby.OutputEntry{
		{Base: "呪", Text: "じゅ", Kind: "phonetic"},
		{Base: "地球", Text: "世界", Kind: "semantic"},
		{Base: "白焉", Text: "びゃくえん", Kind: "creative"},
	}

	cases := []struct {
		name      string
		output    []ruby.OutputEntry
		keep      map[string]bool
		wantLen   int
		wantKinds []string
	}{
		{
			name:      "keep all",
			output:    output,
			keep:      allKinds,
			wantLen:   3,
			wantKinds: []string{"phonetic", "semantic", "creative"},
		},
		{
			name:      "keep creative only",
			output:    output,
			keep:      creativeOnly,
			wantLen:   1,
			wantKinds: []string{"creative"},
		},
		{
			name:    "keep none (empty set)",
			output:  output,
			keep:    emptySet,
			wantLen: 0,
		},
		{
			name:    "nil output",
			output:  nil,
			keep:    allKinds,
			wantLen: 0,
		},
		{
			name: "no matching kinds",
			output: []ruby.OutputEntry{
				{Base: "呪", Text: "じゅ", Kind: "phonetic"},
			},
			keep:    creativeOnly,
			wantLen: 0,
		},
		{
			name: "empty kind is wildcard (preserved)",
			output: []ruby.OutputEntry{
				{Base: "呪", Text: "じゅ", Kind: ""},
			},
			keep:      creativeOnly,
			wantLen:   1,
			wantKinds: []string{""},
		},
		{
			name: "user opts out: kindSet([]) filters all typed entries",
			output: []ruby.OutputEntry{
				{Base: "呪", Text: "じゅ", Kind: "phonetic"},
				{Base: "白焉", Text: "びゃくえん", Kind: "creative"},
			},
			keep:    kindSet([]string{}),
			wantLen: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := filterByKinds(tc.output, tc.keep)
			if len(result) != tc.wantLen {
				t.Fatalf("filterByKinds() returned %d entries, want %d", len(result), tc.wantLen)
			}
			for i, kind := range tc.wantKinds {
				if result[i].Kind != kind {
					t.Errorf("result[%d].Kind = %q, want %q", i, result[i].Kind, kind)
				}
			}
		})
	}
}

// restoreRubySeg 执行 restoreSegmentRuby 的测试壳：全量保留 kinds、无重试后端。
func restoreRubySeg(t *testing.T, seg *Segment, backends []backend.Backend, attempts int) {
	t.Helper()
	restoreSegmentRuby(context.Background(), seg, ruby.NewRestorer(), kindSet(nil),
		backends, backend.RetryPolicy{}, slog.Default(), nil, false, 0, repair.Options{}, attempts)
}

// TestRestoreSegmentRuby_DirectedRetry_DisabledNoTrigger rubyRetryAttempts=0 且 backends 为空：
// 不触发定向重试，既有还原行为不变（已对齐条目还原，未对齐条目不还原）。
func TestRestoreSegmentRuby_DirectedRetry_DisabledNoTrigger(t *testing.T) {
	items := []ruby.Item{
		{ID: "1", SourceBase: "我", SourceText: "wǒ", TargetBase: "I", TargetText: "wǒ", Kind: "phonetic", Aligned: true},
		{ID: "2", SourceBase: "想", SourceText: "xiǎng", TargetBase: "want", TargetText: "xiǎng", Kind: "phonetic", Aligned: true},
		{ID: "3", SourceBase: "要", SourceText: "yào", TargetBase: "hot", TargetText: "yào", Kind: "phonetic", Aligned: true},
		{ID: "4", SourceBase: "一", SourceText: "yī"},
		{ID: "5", SourceBase: "杯", SourceText: "bēi"},
		{ID: "6", SourceBase: "水", SourceText: "shuǐ"},
	}
	seg := newRubyTestSeg("我想要一杯水", "I want hot coffee with water", items)
	restoreRubySeg(t, seg, nil, 0)

	if !strings.Contains(seg.Target, "<ruby>I<rt>wǒ</rt></ruby>") ||
		!strings.Contains(seg.Target, "<ruby>want<rt>xiǎng</rt></ruby>") ||
		!strings.Contains(seg.Target, "<ruby>hot<rt>yào</rt></ruby>") {
		t.Errorf("aligned items not restored: %q", seg.Target)
	}
	if strings.Contains(seg.Target, "<ruby>coffee") || strings.Contains(seg.Target, "<ruby>with") ||
		strings.Contains(seg.Target, "<ruby>water") {
		t.Errorf("unaligned items must not be restored: %q", seg.Target)
	}
	if got := len(ruby.Unaligned(items)); got != 3 {
		t.Errorf("expected 3 unaligned items, got %d", got)
	}
}

// TestRestoreSegmentRuby_DirectedRetry_OnlyMissing 定向重试只下发未对齐条目：
// prompt 的 missing 数组仅含 3 个缺失条目（id 4/5/6），重试后全部按 id 对齐。
func TestRestoreSegmentRuby_DirectedRetry_OnlyMissing(t *testing.T) {
	items := []ruby.Item{
		{ID: "1", SourceBase: "我", SourceText: "wǒ", TargetBase: "I", TargetText: "wǒ", Kind: "phonetic", Aligned: true},
		{ID: "2", SourceBase: "想", SourceText: "xiǎng", TargetBase: "want", TargetText: "xiǎng", Kind: "phonetic", Aligned: true},
		{ID: "3", SourceBase: "要", SourceText: "yào", TargetBase: "hot", TargetText: "yào", Kind: "phonetic", Aligned: true},
		{ID: "4", SourceBase: "一", SourceText: "yī"},
		{ID: "5", SourceBase: "杯", SourceText: "bēi"},
		{ID: "6", SourceBase: "水", SourceText: "shuǐ"},
	}
	seg := newRubyTestSeg("我想要一杯水", "I want hot coffee with water", items)
	// 故意打乱输出顺序（6/5/4），验证按 id 关联而非位置回退
	fb := &fakeBackend{name: "ruby-fake", responses: []string{
		`{"ruby_output":[{"id":"6","base":"water","text":"shuǐ","kind":"phonetic"},{"id":"5","base":"with","text":"bēi","kind":"phonetic"},{"id":"4","base":"coffee","text":"yī","kind":"phonetic"}]}`,
	}}
	restoreRubySeg(t, seg, []backend.Backend{fb}, 1)

	if got := len(fb.requests); got != 1 {
		t.Fatalf("expected 1 backend call, got %d", got)
	}

	var msg struct {
		Source      string `json:"source"`
		Translation string `json:"translation"`
		Missing     []struct {
			ID         string `json:"id"`
			SourceBase string `json:"source_base"`
			SourceText string `json:"source_text"`
		} `json:"missing"`
	}
	if err := json.Unmarshal([]byte(fb.requests[0].User), &msg); err != nil {
		t.Fatalf("directed retry user message is not JSON: %v", err)
	}
	if msg.Translation != "I want hot coffee with water" {
		t.Errorf("directed retry must see the clean translation, got %q", msg.Translation)
	}
	if len(msg.Missing) != 3 {
		t.Fatalf("expected 3 missing items in prompt, got %d", len(msg.Missing))
	}
	wantBase := map[string]string{"4": "一", "5": "杯", "6": "水"}
	for _, m := range msg.Missing {
		wb, ok := wantBase[m.ID]
		if !ok {
			t.Errorf("aligned item leaked into directed prompt: id %q", m.ID)
			continue
		}
		if m.SourceBase != wb {
			t.Errorf("missing item %s: source_base %q, want %q", m.ID, m.SourceBase, wb)
		}
	}

	if got := len(ruby.Unaligned(items)); got != 0 {
		t.Fatalf("expected all items aligned after retry, got %d unaligned", got)
	}
	byID := ruby.ItemsByID(items)
	if byID["4"].TargetBase != "coffee" || byID["5"].TargetBase != "with" || byID["6"].TargetBase != "water" {
		t.Errorf("id-based alignment mismatch: item4=%+v item5=%+v item6=%+v", byID["4"], byID["5"], byID["6"])
	}
	for _, wantTag := range []string{
		"<ruby>I<rt>wǒ</rt></ruby>",
		"<ruby>coffee<rt>yī</rt></ruby>",
		"<ruby>with<rt>bēi</rt></ruby>",
		"<ruby>water<rt>shuǐ</rt></ruby>",
	} {
		if !strings.Contains(seg.Target, wantTag) {
			t.Errorf("missing expected tag %s in %q", wantTag, seg.Target)
		}
	}
}

// TestRestoreSegmentRuby_DirectedRetry_EarlyStop 一轮无新增对齐（空输出）即提前停：
// max_attempts=2 时只发 1 次后端调用，译文保持干净。
func TestRestoreSegmentRuby_DirectedRetry_EarlyStop(t *testing.T) {
	items := []ruby.Item{
		{ID: "1", SourceBase: "我", SourceText: "wǒ"},
		{ID: "2", SourceBase: "想", SourceText: "xiǎng"},
		{ID: "3", SourceBase: "要", SourceText: "yào"},
	}
	seg := newRubyTestSeg("我想要一杯水", "I want", items)
	fb := &fakeBackend{name: "ruby-fake", responses: []string{`{"ruby_output":[]}`}}
	restoreRubySeg(t, seg, []backend.Backend{fb}, 2)

	if got := len(fb.requests); got != 1 {
		t.Fatalf("expected exactly 1 backend call (early stop), got %d", got)
	}
	if seg.Target != "I want" {
		t.Errorf("target should stay clean after early stop, got %q", seg.Target)
	}
}

// TestRestoreSegmentRuby_RestructureNoMismatch 结构收缩场景：
// 6 个源条目 → 3 条 LLM 输出（顺序打乱），按 id 关联，item 1 对 entry id 1（非位置 output[0]）。
func TestRestoreSegmentRuby_RestructureNoMismatch(t *testing.T) {
	items := []ruby.Item{
		{ID: "1", SourceBase: "我", SourceText: "wǒ"},
		{ID: "2", SourceBase: "想", SourceText: "xiǎng"},
		{ID: "3", SourceBase: "要", SourceText: "yào"},
		{ID: "4", SourceBase: "一", SourceText: "yī"},
		{ID: "5", SourceBase: "杯", SourceText: "bēi"},
		{ID: "6", SourceBase: "水", SourceText: "shuǐ"},
	}
	ruby.MergeByOutput(items, []ruby.OutputEntry{
		{ID: "5", Base: "water", Text: "bēi", Kind: "phonetic"},
		{ID: "3", Base: "want", Text: "yào", Kind: "phonetic"},
		{ID: "1", Base: "I", Text: "wǒ", Kind: "phonetic"},
	})
	seg := newRubyTestSeg("我想要一杯水", "I want water", items)
	restoreRubySeg(t, seg, nil, 0)

	want := "<ruby>I<rt>wǒ</rt></ruby> <ruby>want<rt>yào</rt></ruby> <ruby>water<rt>bēi</rt></ruby>"
	if seg.Target != want {
		t.Errorf("restructured restore mismatch:\n got %q\nwant %q", seg.Target, want)
	}
	unaligned := ruby.Unaligned(items)
	if len(unaligned) != 3 {
		t.Fatalf("expected 3 unaligned items (2,4,6), got %d", len(unaligned))
	}
	for _, it := range unaligned {
		if it.ID != "2" && it.ID != "4" && it.ID != "6" {
			t.Errorf("unexpected unaligned item id %q", it.ID)
		}
	}
}

// TestRestoreItems_OwnSourceBaseFallback 还原第二优先用条目自身 SourceBase：
// TargetBase 不在译文中时，<ruby> 用 source base + target text。
func TestRestoreItems_OwnSourceBaseFallback(t *testing.T) {
	items := []ruby.Item{
		{ID: "1", SourceBase: "汉字", SourceText: "かんじ", TargetBase: "Kanji", TargetText: "かんじ", Kind: "phonetic", Aligned: true},
	}
	seg := newRubyTestSeg("读 汉字", "读 汉字", items)
	restoreRubySeg(t, seg, nil, 0)

	want := "读 <ruby>汉字<rt>かんじ</rt></ruby>"
	if seg.Target != want {
		t.Errorf("own source-base fallback failed:\n got %q\nwant %q", seg.Target, want)
	}
}

// TestParseAlignmentResponseText_FourFieldID text 模式对齐响应支持可选的 4 字段 | id：
// 3 字段行 id 为空，4 字段行 id 从最右侧字段解析。
func TestParseAlignmentResponseText_FourFieldID(t *testing.T) {
	out := parseAlignmentResponseText("I | aɪ | phonetic | 1\nwant | wɒnt | phonetic | 3\nwater | wɔːtə | phonetic\n", 3)
	if len(out) != 3 {
		t.Fatalf("expected 3 entries, got %#v", out)
	}
	if out[0].ID != "1" || out[0].Base != "I" || out[0].Kind != "phonetic" {
		t.Errorf("wrong entry[0]: %+v", out[0])
	}
	if out[1].ID != "3" {
		t.Errorf("wrong entry[1]: %+v", out[1])
	}
	if out[2].ID != "" || out[2].Base != "water" {
		t.Errorf("3-field line should have empty id, got %+v", out[2])
	}
}
