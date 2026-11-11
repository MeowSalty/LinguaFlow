package markup

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"strings"
	"testing"
)

func TestBoundedMarkupPreservesFragmentAndRegression(t *testing.T) {
	for _, s := range []string{"", `plain`, `<x id="one">海&amp;wind</x>`, `<x a="1"b="2">text</x>`, `<x>broken`, `<![CDATA[a>b]]><x/>`, `<x xmlns:y="urn:test"><y:tag/></x>`} {
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		old := ValidateFragment(s)
		got := ValidateFragmentBounded(b, s)
		if (old == nil) != (got == nil) {
			t.Fatalf("%q old=%v bounded=%v", s, old, got)
		}
	}
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	if e := TargetRegressionBounded(b, `<x>old</x>`, `<x>broken`); e == nil {
		t.Fatal("target regression accepted")
	}
}
func TestBoundedMarkupRejectsDepthAndAdjacentAttributeNodesBeforeDecoder(t *testing.T) {
	for _, s := range []string{strings.Repeat("<x>", 65) + strings.Repeat("</x>", 65), "<x " + strings.Repeat(`a="1"`, 3000) + ">text</x>"} {
		b, _ := backend.NewBuildBounds(8192, 8192)
		e := ValidateFragmentBounded(b, s)
		if !errors.Is(e, backend.ErrBuildCapacity) || b.Snapshot().TextBytes != 0 {
			t.Fatalf("capacity=%v snapshot=%+v", e, b.Snapshot())
		}
	}
}
