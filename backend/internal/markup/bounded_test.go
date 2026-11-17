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
func TestBoundedMarkupRejectsDepthBeforeDecoder(t *testing.T) {
	s := strings.Repeat("<x>", 65) + strings.Repeat("</x>", 65)
	b, _ := backend.NewBuildBounds(8192, 8192)
	if e := ValidateFragmentBounded(b, s); !errors.Is(e, backend.ErrBuildCapacity) || b.Snapshot().TextBytes != 0 {
		t.Fatalf("capacity=%v snapshot=%+v", e, b.Snapshot())
	}
}

func TestBoundedMarkupAdjacentAttributesUseAllocationCapacity(t *testing.T) {
	s := "<x " + strings.Repeat(`a="1"`, 3000) + ">text</x>"
	limited, _ := backend.NewBuildBounds(8192, 8192, 4096)
	if e := ValidateFragmentBounded(limited, s); e != nil || limited.Snapshot() != (backend.BuildSnapshot{AllocationLimit: 4096}) {
		t.Fatalf("borrowed fragment received an allocation fee: %v snapshot=%+v", e, limited.Snapshot())
	}
	// Decoder attribute objects are internal; only actual input and depth
	// limits constrain this borrowed validation.
	inputLimited, _ := backend.NewBuildBounds(int64(len(s)-1), 0, 4096)
	if e := ValidateFragmentBounded(inputLimited, s); !errors.Is(e, backend.ErrBuildCapacity) || inputLimited.Snapshot().LiveBytes != 0 {
		t.Fatalf("oversized borrowed input accepted: %v snapshot=%+v", e, inputLimited.Snapshot())
	}
	unlimited, _ := backend.NewBuildBounds(8192, 8192)
	if e := ValidateFragmentBounded(unlimited, s); e != nil {
		t.Fatalf("finite attributed fragment rejected: %v", e)
	}
}
