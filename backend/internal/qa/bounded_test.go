package qa

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func qaTestBounds(t *testing.T, output int64) *backend.BuildBounds {
	t.Helper()
	b, err := backend.NewBuildBounds(output, output)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRunBoundedDefaultCheckerParity(t *testing.T) {
	ctx := context.Background()
	g := glossary.NewMemory()
	if _, err := g.Add(ctx, glossary.Entry{Source: "Cloud", Target: "雲", Mandatory: true}, glossary.Entry{Source: "API", Target: "bad", Forbidden: true}, glossary.Entry{Source: "Σ", Target: "sum", CaseSensitive: false}); err != nil {
		t.Fatal(err)
	}
	inputs := []CheckInput{
		{Index: 0, SourceText: "Original sentence", TargetText: "Original sentence"},
		{Index: 1, SourceText: "源文「語」(test)", TargetText: "译文"},
		{Index: 2, SourceText: "source", TargetText: "target ("},
		{Index: 3, SourceText: "source", TargetText: "target )"},
		{Index: 4, SourceText: "source 1,234.50 http://example.org x@example.org", TargetText: "target 1234,50 http://other.org y@example.org"},
		{Index: 5, SourceText: "source", TargetText: "中 文  文字?\tＸ __LF_A __LF_A"},
		{Index: 6, SourceText: "<b>source</b>", TargetText: "<b>target"},
		{Index: 7, SourceText: "<b>source</b>", TargetText: "<i>target</i>"},
		{Index: 8, SourceText: "<ruby>日本<rt>にほん</rt></ruby>", TargetText: "日本"},
		{Index: 9, SourceText: "source あいう words", TargetText: "target あいう!"},
		{Index: 10, SourceText: "one\r\ntwo", TargetText: "one\rtwo\rthree"},
		{Index: 11, SourceText: "Cloud API Σ", TargetText: "bad ς"},
		{Index: 12, SourceText: "different", TargetText: "same"},
		{Index: 13, SourceText: "other", TargetText: "same"},
		{Index: 14, SourceText: "<b>safe</b> (source)", TargetText: "<b>safe</b> target", Protected: map[string]string{"a": "<b>", "b": "<b>", "c": "</b>"}},
		{Index: 15, SourceText: "source", TargetText: "繁體漢字這裡後臺"},
	}
	for _, pair := range [][2]string{{"ja", "zh"}, {"en", "zh-Hans"}, {"en", "en"}, {"ru", "en"}, {"en", "sr-Latn"}} {
		t.Run(pair[0]+"_"+pair[1], func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Enabled = true
			cfg.SourceLang, cfg.TargetLang = pair[0], pair[1]
			cfg.Format = "srt"
			cfg.Glossary = g
			e := NewEngine(cfg, nil)
			for _, code := range AllCheckerNames() {
				t.Run(code, func(t *testing.T) {
					single := cfg
					single.Checks = []string{code}
					checker := NewEngine(single, nil)
					want := checker.Run(ctx, inputs)
					got, err := checker.RunBounded(ctx, qaTestBounds(t, 1<<20), inputs)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("bounded checker changed semantics\nwant=%#v\ngot=%#v", want, got)
					}
				})
			}
			want := e.Run(ctx, inputs)
			got, err := e.RunBounded(ctx, qaTestBounds(t, 1<<20), inputs)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("default ordered aggregation changed")
			}
		})
	}
}

func TestRunBoundedCapacityBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		in         CheckInput
	}{
		{"tokens", CheckNumberMismatch, CheckInput{SourceText: strings.Repeat("1 ", 4000), TargetText: "2"}},
		{"deep_pair", CheckPunctuationPairing, CheckInput{SourceText: "source", TargetText: strings.Repeat("(", 65)}},
		{"protected_hits", CheckWidthMix, CheckInput{SourceText: "source", TargetText: strings.Repeat("a ", 4000), Protected: map[string]string{"a": "a", "b": "a"}}},
		{"long_placeholder", CheckLeftoverPlaceholder, CheckInput{SourceText: "source", TargetText: "__LF_" + strings.Repeat("x", 32<<10)}},
		{"protected_product", CheckWidthMix, CheckInput{SourceText: "source", TargetText: strings.Repeat("x", 32<<10), Protected: map[string]string{"a": "absent", "b": "different"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Enabled = true
			cfg.Checks = []string{tc.code}
			e := NewEngine(cfg, nil)
			b := qaTestBounds(t, 1024)
			got, err := e.RunBounded(context.Background(), b, []CheckInput{tc.in})
			if !errors.Is(err, backend.ErrBuildCapacity) || got != nil {
				t.Fatalf("capacity silently returned partial checks: issues=%d error=%v", len(got), err)
			}
			s := b.Snapshot()
			if s.Nodes > s.NodeLimit || s.TextBytes > s.TextLimit {
				t.Fatalf("overspent before rejection: %+v", s)
			}
		})
	}
}

func TestRunBoundedUnicodeAndTokenParity(t *testing.T) {
	texts := []string{"", "word", "İKΣςẞ bad", "1,234.50 １，２３４．５０ 10.20 1.2.3", "<b a='one'>Cloud</b><ruby>語<rt>ご</rt></ruby>", "  中 文\t\r\n__LF_Z__LF_Z", "『quoted』 \"it's fine\" (x)", "http://a.org/?x=1 a@b.org __LF_QA007", "あいう 漢字テスト!"}
	for _, lang := range []string{"zh", "en", "sr-Latn"} {
		cfg := DefaultConfig()
		cfg.Enabled, cfg.SourceLang, cfg.TargetLang, cfg.Format = true, "ja", lang, "srt"
		e := NewEngine(cfg, nil)
		for i, source := range texts {
			for j, target := range texts {
				inputs := []CheckInput{{Index: i, SourceText: source, TargetText: target, Protected: map[string]string{"same": "Cloud", "duplicate": "Cloud"}}}
				want := e.Run(context.Background(), inputs)
				got, err := e.RunBounded(context.Background(), qaTestBounds(t, 1<<20), inputs)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("language%s source%d target%d: bounded semantics differ\nwant%+v\ngot%+v\nerr%v", lang, i, j, want, got, err)
				}
			}
		}
	}
}

func TestRunBoundedGlossaryCapacityBeforeIssues(t *testing.T) {
	ctx := context.Background()
	g := glossary.NewMemory()
	var source strings.Builder
	for i := 0; i < 120; i++ {
		word := fmt.Sprintf("term%03d", i)
		source.WriteString(word + " ")
		if _, err := g.Add(ctx, glossary.Entry{Source: word, Target: "recommended", Notes: strings.Repeat("M", 2048), Mandatory: true}); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultConfig()
	cfg.Enabled, cfg.SourceLang, cfg.TargetLang, cfg.Glossary = true, "en", "zh", g
	cfg.Checks = []string{CheckTermInconsistency}
	b := qaTestBounds(t, 1024)
	got, err := NewEngine(cfg, nil).RunBounded(ctx, b, []CheckInput{{SourceText: source.String(), TargetText: "different"}})
	if !errors.Is(err, backend.ErrBuildCapacity) || got != nil {
		t.Fatalf("glossary silently skipped capacity checks: issues%d err%v", len(got), err)
	}
	s := b.Snapshot()
	if s.Nodes > s.NodeLimit || s.TextBytes > s.TextLimit {
		t.Fatalf("glossary overspent: %+v", s)
	}
}

func TestBoundedIssueReconciliationParityAndCapacity(t *testing.T) {
	now := time.Date(2026, 11, 9, 1, 2, 3, 0, time.FixedZone("fixture", 3600))
	actor := 7
	existing := []QualityIssue{{Code: CheckNumberMismatch, Span: &Span{MatchedText: "12"}, Disposition: DispositionDismissed, DecidedBy: &actor, DecidedAt: &now, Note: "retained"}, {Code: IssueCodeGrammar, Message: "keep semantic"}, {Code: CheckWidthMix, Message: "untargeted"}}
	fresh := []QualityIssue{{Code: CheckNumberMismatch, Span: &Span{MatchedText: "12"}, Message: "fresh message"}, {Code: CheckDuplicate}}
	got, err := ReconcileIssuesBounded(qaTestBounds(t, 1<<20), fresh, existing)
	if err != nil || !reflect.DeepEqual(got, ReconcileIssues(fresh, existing)) {
		t.Fatalf("reconcile parity: %+v %v", got, err)
	}
	for _, ran := range []bool{false, true} {
		got, err := ReviseFinalIssuesBounded(qaTestBounds(t, 1<<20), existing, fresh, []string{CheckWidthMix}, ran)
		if err != nil || !reflect.DeepEqual(got, ReviseFinalIssues(existing, fresh, []string{CheckWidthMix}, ran)) {
			t.Fatalf("revise ran%v: %+v %v", ran, got, err)
		}
	}
	long := []QualityIssue{{Code: "test", Span: &Span{MatchedText: strings.Repeat("x", 32<<10)}}}
	if _, err := ReconcileIssuesBounded(qaTestBounds(t, 1024), long, long); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatal(fmt.Sprint("long fingerprint copied without capacity: ", err))
	}
}

func TestBoundedEmptyResultsPreserveNilSemantics(t *testing.T) {
	inputs := []CheckInput{}
	cfg := DefaultConfig()
	cfg.Enabled = true
	if got, err := NewEngine(cfg, nil).RunBounded(context.Background(), nil, inputs); got != nil || err != nil {
		t.Fatalf("empty work allocated or required bounds: %v %v", got, err)
	}
	values := [][]QualityIssue{nil, {}, {{Code: CheckDuplicate}}}
	for i, existing := range values {
		for j, fresh := range values {
			t.Run(fmt.Sprintf("existing%d_fresh%d", i, j), func(t *testing.T) {
				got, err := ReconcileIssuesBounded(qaTestBounds(t, 1<<20), fresh, existing)
				if err != nil || !reflect.DeepEqual(got, ReconcileIssues(fresh, existing)) {
					t.Fatalf("reconcile nil/empty distinction: %#v %v", got, err)
				}
				for _, ran := range []bool{false, true} {
					got, err = ReviseFinalIssuesBounded(qaTestBounds(t, 1<<20), existing, fresh, nil, ran)
					if err != nil || !reflect.DeepEqual(got, ReviseFinalIssues(existing, fresh, nil, ran)) {
						t.Fatalf("revise ran%v nil/empty distinction: %#v %v", ran, got, err)
					}
				}
			})
		}
	}
}

func TestBoundedLocateCoveredMatchesUseFixedOffsetAllocations(t *testing.T) {
	ctx := context.Background()
	target := strings.Repeat("hit ", 1000) + "hit"
	regions := [][2]int{{0, 4000}}
	var span *Span
	allocations := testing.AllocsPerRun(10, func() {
		b, err := backend.NewBuildBounds(1<<20, 1<<20)
		if err != nil {
			panic(err)
		}
		q := boundedQA{ctx: ctx, b: b}
		span = q.locate(target, "hit", regions)
		if q.err != nil {
			panic(q.err)
		}
	})
	if span == nil || span.TargetStart == nil || *span.TargetStart != 4000 || *span.TargetEnd != 4003 || allocations > 10 {
		t.Fatalf("covered occurrences created per-match pointer allocations: allocations%.0f span%+v", allocations, span)
	}
	t.Logf("actual locate allocations=%.0f covered_occurrences=1000", allocations)
}

func TestBoundedRepeatedSpaceRejectsBeforeOffsetAllocation(t *testing.T) {
	var b backend.BuildBounds
	q := boundedQA{b: &b}
	baseline := testing.AllocsPerRun(10, func() {
		q.err = nil
		if q.nodes(4) {
			panic("node capacity rejection failed")
		}
	})
	allocations := testing.AllocsPerRun(10, func() {
		q.err = nil
		q.repeatedIssue(0, "  ", 3, 5)
		if !errors.Is(q.err, backend.ErrBuildCapacity) || q.issues != nil {
			panic("repeated-space capacity rejection failed")
		}
	})
	if allocations != baseline {
		t.Fatalf("offsets allocated before node ticket rejection: %.0f vs base rejection%.0f", allocations, baseline)
	}
	t.Logf("actual repeated-space rejected allocations=%.0f baseline error allocations=%.0f additional offset allocations=%.0f", allocations, baseline, allocations-baseline)
}
