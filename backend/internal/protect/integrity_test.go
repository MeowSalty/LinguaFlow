package protect

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

func TestPlaceholderViolations(t *testing.T) {
	cases := []struct {
		name       string
		seg        *model.Segment
		missing    []string
		duplicated []string
		invented   []string
	}{
		{
			name: "normal exact once each",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
					"__LF_000002__": "B",
				},
				Target: "keep __LF_000001__ and __LF_000002__",
			},
		},
		{
			name: "missing keys",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
					"__LF_000002__": "B",
					"__LF_000003__": "C",
				},
				Target: "kept __LF_000002__ only",
			},
			missing: []string{"__LF_000001__", "__LF_000003__"},
		},
		{
			name: "duplicated key",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
					"__LF_000002__": "B",
				},
				Target: "__LF_000001__ x __LF_000001__ y __LF_000002__",
			},
			duplicated: []string{"__LF_000001__"},
		},
		{
			name: "invented key",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
				},
				Target: "ok __LF_000001__ plus __LF_000099__",
			},
			invented: []string{"__LF_000099__"},
		},
		{
			name: "combination missing duplicate invented",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
					"__LF_000002__": "B",
					"__LF_000003__": "C",
				},
				// 000001 missing; 000002 twice; 000003 once; 000050 invented
				Target: "__LF_000002__ mid __LF_000002__ __LF_000003__ junk __LF_000050__",
			},
			missing:    []string{"__LF_000001__"},
			duplicated: []string{"__LF_000002__"},
			invented:   []string{"__LF_000050__"},
		},
		{
			name: "no protected map nil",
			seg: &model.Segment{
				Target: "plain with __LF_000007__",
			},
			invented: []string{"__LF_000007__"},
		},
		{
			name: "no protected map empty",
			seg: &model.Segment{
				Protected: map[string]string{},
				Target:    "plain text",
			},
		},
		{
			name: "duplicated known key not invented",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
				},
				Target: "__LF_000001____LF_000001__",
			},
			duplicated: []string{"__LF_000001__"},
		},
		{
			name: "deterministic sorting multi invented and missing",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000010__": "X",
					"__LF_000002__": "Y",
					"__LF_000001__": "Z",
				},
				Target: "__LF_000099__ __LF_000005__ __LF_000050__",
			},
			missing:  []string{"__LF_000001__", "__LF_000002__", "__LF_000010__"},
			invented: []string{"__LF_000005__", "__LF_000050__", "__LF_000099__"},
		},
		{
			name: "non-canonical lf tokens are invented",
			seg: &model.Segment{
				Protected: map[string]string{
					"__LF_000001__": "A",
				},
				Target: "ok __LF_000001__ bad __lf_000002__ and __LF_2__",
			},
			invented: []string{"__LF_2__", "__lf_000002__"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missing, duplicated, invented := PlaceholderViolations(tc.seg)
			assertSortedKeys(t, "missing", missing, tc.missing)
			assertSortedKeys(t, "duplicated", duplicated, tc.duplicated)
			assertSortedKeys(t, "invented", invented, tc.invented)
		})
	}
}

func TestMissingPlaceholders_Delegates(t *testing.T) {
	seg := &model.Segment{
		Protected: map[string]string{
			"__LF_000003__": "C",
			"__LF_000001__": "A",
			"__LF_000002__": "B",
		},
		Target: "only __LF_000002__",
	}
	got := MissingPlaceholders(seg)
	want := []string{"__LF_000001__", "__LF_000003__"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingPlaceholders: got %v, want %v", got, want)
	}
	// duplicated / invented path must not leak into MissingPlaceholders
	seg2 := &model.Segment{
		Protected: map[string]string{"__LF_000001__": "A"},
		Target:    "__LF_000001__ __LF_000001__ __LF_000099__",
	}
	if got := MissingPlaceholders(seg2); got != nil && len(got) != 0 {
		t.Fatalf("expected no missing, got %v", got)
	}
}

func assertSortedKeys(t *testing.T, label string, got, want []string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got == nil {
		got = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}
