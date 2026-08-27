package segmatch

import (
	"errors"
	"reflect"
	"testing"
)

func boolPtr(value bool) *bool { return &value }

func TestSubstringFind(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		text string
		want []Match
	}{
		{
			name: "basic",
			opts: Options{Find: "cat"},
			text: "a cat",
			want: []Match{{Start: 2, End: 5}},
		},
		{
			name: "multiple non-overlapping matches",
			opts: Options{Find: "ana"},
			text: "bananana",
			want: []Match{{Start: 1, End: 4}, {Start: 5, End: 8}},
		},
		{
			name: "case sensitive",
			opts: Options{Find: "go", CaseSensitive: boolPtr(true)},
			text: "Go go GO",
			want: []Match{{Start: 3, End: 5}},
		},
		{
			name: "case insensitive",
			opts: Options{Find: "äPFEL", CaseSensitive: boolPtr(false)},
			text: "Äpfel äPFEL",
			want: []Match{{Start: 0, End: 6}, {Start: 7, End: 13}},
		},
		{
			name: "whole word mixed Chinese and English",
			opts: Options{Find: "cat", WholeWord: boolPtr(true)},
			text: "猫cat猫 cat，狗",
			want: []Match{{Start: 10, End: 13}},
		},
		{
			name: "whole word treats digits as word characters",
			opts: Options{Find: "cat", WholeWord: boolPtr(true)},
			text: "cat2 2cat cat_ _cat cat",
			want: []Match{{Start: 10, End: 13}, {Start: 16, End: 19}, {Start: 20, End: 23}},
		},
		{
			name: "empty find",
			opts: Options{Find: ""},
			text: "anything",
			want: nil,
		},
		{
			name: "percent and underscore are ordinary characters",
			opts: Options{Find: "%_"},
			text: "a%_b",
			want: []Match{{Start: 1, End: 3}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			if got := matcher.Find(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Find() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRegexFind(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		text string
		want []Match
	}{
		{
			name: "basic",
			opts: Options{Find: `\d+`, MatchMode: "regex"},
			text: "a12 b3",
			want: []Match{{Start: 1, End: 3}, {Start: 5, End: 6}},
		},
		{
			name: "case insensitive",
			opts: Options{Find: "cat", MatchMode: "regex", CaseSensitive: boolPtr(false)},
			text: "CAT cat",
			want: []Match{{Start: 0, End: 3}, {Start: 4, End: 7}},
		},
		{
			name: "whole word is ignored",
			opts: Options{Find: "cat", MatchMode: "regex", WholeWord: boolPtr(true)},
			text: "concatenate",
			want: []Match{{Start: 3, End: 6}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			if got := matcher.Find(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Find() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRegexInvalidPattern(t *testing.T) {
	_, err := NewMatcher(Options{Find: "[", MatchMode: "regex"})
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("NewMatcher error = %v, want errors.Is(..., ErrInvalidPattern)", err)
	}
}

func TestReplaceAll(t *testing.T) {
	tests := []struct {
		name        string
		opts        Options
		text        string
		replaceWith string
		wantText    string
		wantCount   int
	}{
		{
			name:        "substring replacement",
			opts:        Options{Find: "cat"},
			text:        "cat and cat",
			replaceWith: "dog",
			wantText:    "dog and dog",
			wantCount:   2,
		},
		{
			name:        "empty replacement deletes matches",
			opts:        Options{Find: "-"},
			text:        "a-b-c",
			replaceWith: "",
			wantText:    "abc",
			wantCount:   2,
		},
		{
			name:        "regex capture replacement",
			opts:        Options{Find: `(\w+)-(\w+)`, MatchMode: "regex"},
			text:        "first-last second-item",
			replaceWith: `${2},$1`,
			wantText:    "last,first item,second",
			wantCount:   2,
		},
		{
			name:        "empty substring has no replacements",
			opts:        Options{Find: ""},
			text:        "abc",
			replaceWith: "x",
			wantText:    "abc",
			wantCount:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher, err := NewMatcher(tt.opts)
			if err != nil {
				t.Fatalf("NewMatcher: %v", err)
			}
			gotText, gotCount := matcher.ReplaceAll(tt.text, tt.replaceWith)
			if gotText != tt.wantText || gotCount != tt.wantCount {
				t.Fatalf("ReplaceAll() = (%q, %d), want (%q, %d)", gotText, gotCount, tt.wantText, tt.wantCount)
			}
		})
	}
}

func TestNewMatcherDefaultsAndUnknownMode(t *testing.T) {
	matcher, err := NewMatcher(Options{Find: "x"})
	if err != nil {
		t.Fatalf("NewMatcher default mode: %v", err)
	}
	if got := matcher.Find("x"); !reflect.DeepEqual(got, []Match{{Start: 0, End: 1}}) {
		t.Fatalf("default mode Find() = %#v", got)
	}

	if _, err := NewMatcher(Options{Find: "x", MatchMode: "glob"}); err == nil {
		t.Fatal("unknown match mode should return an error")
	}
}
