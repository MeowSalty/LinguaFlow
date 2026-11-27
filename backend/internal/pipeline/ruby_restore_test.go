package pipeline

import (
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestStagedRubyUsesFrozenTemplatesAndExplicitAttempts(t *testing.T) {
	for _, mode := range []string{"json", "text"} {
		for _, attempts := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s_%d", mode, attempts), func(t *testing.T) {
				seg := newRubyTestSeg("我", "", []ruby.Item{{ID: "1", SourceBase: "我", SourceText: "wǒ"}})
				seg.Translate = true
				doc := &Document{Format: "text", SourceLang: "zh", TargetLang: "en", Segments: []Segment{*seg}}
				mainResponse := `{"translations":{"1":"I"}}`
				alignResponse := `{"ruby_output":[{"id":"1","base":"I","text":"wǒ","kind":"phonetic","occurrence":1}]}`
				mainMode := ""
				if mode == "text" {
					mainMode = "text"
					mainResponse = "[1] I"
					alignResponse = `"I" | "wǒ" | "phonetic" | "1" | 1`
				}
				align := &fakeBackend{name: "frozen", responses: []string{alignResponse}}
				h := &TranslateHandler{
					Backend:   &fakeBackend{name: "main", responses: []string{mainResponse}},
					BatchSize: 1, Renderer: newTestRenderer(t), ResponseMode: mainMode,
					RubyEnabled: true, RubyProtocolVersion: ruby.ProtocolV2, RubyRetryAttempts: attempts,
					RubyRetryBackends: []backend.Backend{align},
					RubyTemplates:     prompt.RubyTemplates{JSON: "archived JSON alignment template", Text: "archived text alignment template"},
				}
				result, err := runMemoryTestRound(t, t.Context(), Round{Handler: h, Concurrency: 1}, doc, nil, quietLogger(), nil)
				if err != nil || len(result.Unresolved) != 0 || len(align.requests) != attempts {
					t.Fatalf("result=%+v calls=%d err=%v", result, len(align.requests), err)
				}
				if attempts == 0 {
					if got := doc.Segments[0].Target; got != "I" {
						t.Fatalf("unannotated target=%q", got)
					}
					return
				}
				want := h.RubyTemplates.JSON
				if mode == "text" {
					want = h.RubyTemplates.Text
				}
				if align.requests[0].System != want || doc.Segments[0].Target != "<ruby>I<rt>wǒ</rt></ruby>" {
					t.Fatalf("system=%q target=%q", align.requests[0].System, doc.Segments[0].Target)
				}
			})
		}
	}
}

func testRubyTemplates() prompt.RubyTemplates {
	return prompt.RubyTemplates{JSON: prompt.RubyAlignmentJSONTemplate, Text: prompt.RubyAlignmentTextTemplate}
}

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

// TestExtractRubyItemsFromSeg_NoLegacyFallback legacy key ruby_output 不再回退：
// 仅识别 ruby_items 主 key。
func TestExtractRubyItemsFromSeg_NoLegacyFallback(t *testing.T) {
	seg := &Segment{Meta: map[string]any{
		"ruby_output": []ruby.OutputEntry{{ID: "1", Base: "I", Text: "aɪ", Kind: "phonetic"}},
	}}
	if items := extractRubyItemsFromSeg(seg); items != nil {
		t.Errorf("legacy ruby_output must be ignored, got %+v", items)
	}
}
