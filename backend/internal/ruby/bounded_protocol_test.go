package ruby

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func rubyBounds() *backend.BuildBounds { b, _ := backend.NewBuildBounds(1<<20, 1<<20); return b }
func TestBoundedBatchRequestPreservesPublicView(t *testing.T) {
	s, e := NewAlignmentState("海&amp;風", "html", []Item{{ID: "1", SourceBase: "海", SourceText: "うみ"}}, nil, 2)
	if e != nil {
		t.Fatal(e)
	}
	m := []AlignmentBatchMember{{WorkID: "w", CandidateID: "c", Source: "<ruby>海<rt>うみ</rt></ruby>", Alignment: s}}
	want, e := AlignmentBatchRequest(m)
	if e != nil {
		t.Fatal(e)
	}
	got, e := AlignmentBatchRequestBounded(rubyBounds(), m, 1<<20)
	if e != nil || want != got {
		t.Fatalf("batch=%s want=%s err=%v", got, want, e)
	}
	claimed := false
	user, e := AlignmentBatchRequestWithCreditsBounded(rubyBounds(), m, "system <&>", 1<<20, func(measure backend.PromptMeasure) error {
		claimed = true
		n, _ := backend.MeasureJSONString(want)
		if measure.UserBytes != int64(len(want)) || measure.UserJSONBytes != n {
			t.Fatalf("aux measure=%#v", measure)
		}
		return nil
	})
	if e != nil || !claimed || user != want {
		t.Fatalf("aux claim %v %v", claimed, e)
	}
}
func TestBoundedBatchParserPreservesMembersDuplicatesAndTruncation(t *testing.T) {
	for _, s := range []string{`{"alignments":[{"work_id":"w","candidate_id":"c","ruby_output":[]}]}`, `{"alignments":[{"work_id":"w","candidate_id":"c","ruby_output":[{"id":"1","base":"海","text":"うみ","kind":"phonetic","occurrence":1,"occurrence":2}]}]}`, `{"alignments":[{"work_id":"w","candidate_id":"c","ruby_output":[]},{"work_id":"w","candidate_id":"c","ruby_output":[]}]}`, `{"alignments":[{"work_id":"w","candidate_id":"c","ruby_output":[]},{"work_id":"other","candidate_id":"later","ruby_output":[`, `{"alignments":[{"work_id":"w","work_id":"conflict","candidate_id":"c","ruby_output":[]}]}`} {
		want := ParseAlignmentBatchJSON(s)
		got, e := ParseAlignmentBatchJSONBounded(rubyBounds(), s)
		if e != nil || !reflect.DeepEqual(want.Entries, got.Entries) || !reflect.DeepEqual(want.Invalid, got.Invalid) || want.EnvelopeInvalid != got.EnvelopeInvalid {
			t.Fatalf("want=%#v got=%#v err=%v input=%s", want, got, e, s)
		}
	}
	deep := `{"alignments":[` + strings.Repeat("[", 63) + "0" + strings.Repeat("]", 63) + "]}"
	_, e := ParseAlignmentBatchJSONBounded(rubyBounds(), deep)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("outer depth=%v", e)
	}
}
