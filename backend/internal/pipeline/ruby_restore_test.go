package pipeline

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

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
