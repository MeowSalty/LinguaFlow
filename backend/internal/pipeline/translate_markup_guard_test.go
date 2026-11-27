package pipeline

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// recordingTM 记录 Add 调用的 TM 桩：用于验证结构守卫拦截的坏译文不会污染记忆库。
type recordingTM struct {
	tm.Nop
	adds []tm.Match
}

func (r *recordingTM) Add(_ context.Context, src, tgt, srcLang, tgtLang string) error {
	r.adds = append(r.adds, tm.Match{Source: src, Target: tgt, Score: float32(len(srcLang) + len(tgtLang))})
	return nil
}

// newMarkupGuardDoc 构造含单个翻译段的 EPUB 文档。
// OriginalSource 承载真实原文（含 ruby 标签），与生产路径 Protect 阶段落盘的快照一致。
func newMarkupGuardDoc(format, source string) *Document {
	return &Document{
		Format:     format,
		SourceLang: "ja",
		TargetLang: "zh",
		Segments: []Segment{{
			ID:             "seg-0",
			Source:         source,
			OriginalSource: source,
			Translate:      true,
			Issues:         []qa.QualityIssue{{Code: qa.CheckPunctuationMissing, Severity: qa.SeverityWarning}},
		}},
	}
}

func runMarkupGuard(t *testing.T, doc *Document, memory tm.TranslationMemory, targets map[string]string) ([]int, *Segment) {
	t.Helper()
	h := &TranslateHandler{
		Backend:   &fakeBackend{name: "markup", responses: []string{mustJSON(t, map[string]any{"translations": targets})}},
		BatchSize: len(targets), Renderer: newTestRenderer(t), TM: memory,
	}
	result, err := runMemoryTestRound(t, t.Context(), Round{Handler: h, Concurrency: 1}, doc, nil, quietLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return result.Unresolved, &doc.Segments[0]
}

const (
	// markupGuardGoodSource 是含平衡 ruby 的日文原文。
	markupGuardGoodSource = "「レベル６：<ruby>劣化雷神皇<rt>レツサーエフタル</rt></ruby>っ！」"
	// markupGuardBadTarget 是 LLM 违反协议直写裸 ruby 且缺 </ruby> 的坏译文。
	markupGuardBadTarget = "「等级６：<ruby>劣化雷神皇<rt>雷萨埃夫塔尔</rt>！」"
	// markupGuardGoodTarget 是结构合法的译文。
	markupGuardGoodTarget = "「等级６：<ruby>劣化雷神皇<rt>雷萨埃夫塔尔</rt></ruby>！」"
)

// 坏译文（缺 </ruby>）必须被拦截：段索引进 unresolved，已接受的内容保留，
// 使该段复用既有的池间/跨轮重试机制。
func TestTranslateMarkupGuard_BadRubyTargetGoesUnresolved(t *testing.T) {
	doc := newMarkupGuardDoc("epub", markupGuardGoodSource)
	unresolved, seg := runMarkupGuard(t, doc, nil, map[string]string{"1": markupGuardBadTarget})

	if len(unresolved) != 1 || unresolved[0] != 0 {
		t.Fatalf("unresolved=%v, want [0]", unresolved)
	}
	if seg.Target != "" {
		t.Errorf("Target=%q, want empty (坏译文不得落库)", seg.Target)
	}
	if len(seg.Issues) != 1 || seg.Issues[0].Code != qa.CheckPunctuationMissing {
		t.Errorf("prior issues changed: %+v", seg.Issues)
	}
}

// 合法译文照常通过：不进 unresolved，Target 保持 LLM 译文原样。
func TestTranslateMarkupGuard_WellFormedTargetPasses(t *testing.T) {
	doc := newMarkupGuardDoc("epub", markupGuardGoodSource)
	unresolved, seg := runMarkupGuard(t, doc, nil, map[string]string{"1": markupGuardGoodTarget})

	if len(unresolved) != 0 {
		t.Fatalf("unresolved=%v, want empty", unresolved)
	}
	if seg.Target != markupGuardGoodTarget {
		t.Errorf("Target=%q, want %q", seg.Target, markupGuardGoodTarget)
	}
}

// 格式门禁：非 epub 格式的译文不做结构校验，同样的坏译文不受影响
// （防止误伤纯文本等按字节直通格式的项目）。
func TestTranslateMarkupGuard_FormatGateSkipsNonEpub(t *testing.T) {
	doc := newMarkupGuardDoc("txt", markupGuardGoodSource)
	unresolved, seg := runMarkupGuard(t, doc, nil, map[string]string{"1": markupGuardBadTarget})

	if len(unresolved) != 0 {
		t.Fatalf("unresolved=%v, want empty (txt 格式不应判违规)", unresolved)
	}
	if seg.Target != markupGuardBadTarget {
		t.Errorf("Target=%q, want %q", seg.Target, markupGuardBadTarget)
	}
}

// 门禁 2（源文自身非法时不判违规）：遗留数据的 source_text 提取期就含裸 &，
// 译文含同样的裸 & 也不得判违规——否则每次重译都判违规，整轮任务被无解拖死。
func TestTranslateMarkupGuard_InvalidSourceNeverFlagsRegression(t *testing.T) {
	doc := newMarkupGuardDoc("epub", "A & B <ruby>基<rt>注</rt></ruby>")
	unresolved, seg := runMarkupGuard(t, doc, nil, map[string]string{"1": "A & B <ruby>基<rt>注</rt></ruby>"})

	if len(unresolved) != 0 {
		t.Fatalf("unresolved=%v, want empty (源文非法时不应判违规)", unresolved)
	}
	if seg.Target == "" {
		t.Error("Target should be kept, got empty")
	}
}

// 坏译文绝不能写入 TM：守卫位于 TM 之前，坏段跳过 TM 写入；合法段照常写入。
func TestTranslateMarkupGuard_BadTargetNeverWritesTM(t *testing.T) {
	doc := &Document{
		Format:     "epub",
		SourceLang: "ja",
		TargetLang: "zh",
		Segments: []Segment{
			{
				ID:             "seg-0",
				Source:         markupGuardGoodSource,
				OriginalSource: markupGuardGoodSource,
				Translate:      true,
			},
			{
				ID:             "seg-1",
				Source:         "プレーンな文",
				OriginalSource: "プレーンな文",
				Translate:      true,
			},
		},
	}
	memory := &recordingTM{}
	unresolved, _ := runMarkupGuard(t, doc, memory, map[string]string{"1": markupGuardBadTarget, "2": "普通的一句"})

	if len(unresolved) != 1 || unresolved[0] != 0 {
		t.Fatalf("unresolved=%v, want [0]", unresolved)
	}
	if len(memory.adds) != 1 {
		t.Fatalf("TM Add calls=%d, want 1 (坏段必须跳过 TM 写入)", len(memory.adds))
	}
	add := memory.adds[0]
	if add.Source != "プレーンな文" || add.Target != "普通的一句" {
		t.Errorf("TM Add=%+v, want 仅合法段 (プレーンな文 → 普通的一句)", add)
	}
}
