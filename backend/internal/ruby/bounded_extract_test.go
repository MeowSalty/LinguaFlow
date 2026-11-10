package ruby

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBoundedExtractPreservesPairsAndMergedKanji(t *testing.T) {
	for _, s := range []string{"plain", "<ruby>海<rt>うみ</rt></ruby><ruby>風<rt>かぜ</rt></ruby>", "<ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>", "<ruby><rb>海</rb><rp>(</rp><rt>うみ</rt><rp>)</rp></ruby>", "<ruby>none</ruby>"} {
		old, text := Extract(s)
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		got, stripped, e := ExtractBounded(b, s)
		if e != nil || text != stripped || !reflect.DeepEqual(old, got) {
			t.Fatalf("extract %q old=%v/%q bounded=%v/%q err=%v", s, old, text, got, stripped, e)
		}
	}
}

func TestBoundedElementSpansPreservesCompleteRTPairs(t *testing.T) {
	for _, source := range []string{"", "plain", "<ruby>none</ruby>", "<ruby>海<rt>うみ</ruby>", "<ruby>何<rt>な</rt>故<rt>ぜ</rt></ruby>tail", "<ruby><rb>海</rb><rp>(</rp><rt>うみ</rt><rp>)</rp></ruby><ruby>風<rt>かぜ</rt></ruby>"} {
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		got, err := ElementSpansBounded(b, source)
		if err != nil || !reflect.DeepEqual(got, ElementSpans(source)) {
			t.Fatalf("source=%q spans=%v err=%v", source, got, err)
		}
	}
}
