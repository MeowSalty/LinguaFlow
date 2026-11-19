package repair

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
)

func boundsForTest(t *testing.T, q, r int64) *backend.BuildBounds {
	t.Helper()
	b, e := backend.NewBuildBounds(q, r)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestBoundedMainMemberCapacityIsIndependent(t *testing.T) {
	big := strings.Repeat("x", 12288)
	for _, textMode := range []bool{false, true} {
		text := `{"translations":{"1":"small","2":"` + big + `"},"glossary":[{"source":"海","target":"sea"}]}`
		if textMode {
			text = "[1] small\n[2] " + big + "\n[glossary]\n海 | sea"
		}
		a, e := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), text, textMode, false, []string{"1", "2"}, Options{JSONStructural: true})
		if e != nil {
			t.Fatal(e)
		}
		defer a.Release()
		r, e := a.Member(boundsForTest(t, 8192, 1<<20), 0)
		if e != nil || r.Target != "small" {
			t.Fatalf("sibling text=%v result=%v err=%v", textMode, r, e)
		}
		_, e = a.Member(boundsForTest(t, 8192, 1<<20), 1)
		var content *responsecontract.MemberContentError
		if !errors.As(e, &content) || content.Kind != responsecontract.MemberTooLarge || content.Ordinal != 1 || content.Limit != 8192 || content.Observed != int64(len(big)) {
			t.Fatalf("large selected: %v", e)
		}
		business, e := a.Glossary(boundsForTest(t, 8192, 1<<20))
		if e != nil || len(business) != 1 || business[0].Target != "sea" {
			t.Fatalf("business: %#v %v", business, e)
		}
	}
}
func TestBoundedRepairSemanticParity(t *testing.T) {
	for _, text := range []string{`{"translations":{"1":"a","2":{"target":"b"}},"glossary":[{"source":"a","target":"A"}]}`, `{"translation":{"1":"a","2":"b"}}`, `{"translations":{"1":"a","2":"b",}}`, `{"translations":{"1":"a","2":"b`, "```json\n{\"translations\":{\"1\":\"a\",\"2\":\"b\"}}\n```"} {
		opt := Options{JSONStructural: true, SchemaAliases: true}
		old := TryRepair(text, []string{"1", "2"}, opt)
		opt.Bounds = boundsForTest(t, 1<<20, 1<<20)
		got := TryRepair(text, []string{"1", "2"}, opt)
		if old.Fatal != got.Fatal || !reflect.DeepEqual(old.Trans, got.Trans) || !slices.Equal(old.Missing, got.Missing) || !reflect.DeepEqual(old.Glos, got.Glos) {
			t.Fatalf("old=%#v got=%#v input=%q", old, got, text)
		}
		a, e := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), text, false, false, []string{"1", "2"}, Options{JSONStructural: true, SchemaAliases: true})
		if e != nil {
			if old.Fatal {
				continue
			}
			t.Fatal(e)
		}
		for ordinal, id := range []string{"1", "2"} {
			r, e := a.Member(boundsForTest(t, 1<<20, 1<<20), ordinal)
			if e != nil || r.Target != old.Trans[id] {
				t.Fatalf("member %s old=%q got=%v e=%v input=%q", id, old.Trans[id], r, e, text)
			}
		}
		a.Release()
	}
	text := "[1] first\ncontinuation\n[2] second\n[ruby]\n1: first | reading | phonetic | #1\n[glossary]\n海 | sea"
	old := TryRepairText(text, []string{"1", "2"}, Options{})
	got := TryRepairText(text, []string{"1", "2"}, Options{Bounds: boundsForTest(t, 1<<20, 1<<20)})
	if !reflect.DeepEqual(old.Trans, got.Trans) || !reflect.DeepEqual(old.RubyOutput, got.RubyOutput) || !reflect.DeepEqual(old.Glos, got.Glos) {
		t.Fatalf("text old=%#v got=%#v", old, got)
	}
	a, e := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), text, true, false, []string{"1", "2"}, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Release()
	r, e := a.Member(boundsForTest(t, 1<<20, 1<<20), 0)
	if e != nil || r.Target != old.Trans["1"] || !reflect.DeepEqual(r.RubyOutput, old.RubyOutput["1"]) {
		t.Fatalf("text selected=%#v err=%v", r, e)
	}
}
func TestBoundedReviseMemberCapacityAndDuplicates(t *testing.T) {
	big := strings.Repeat("x", 12288)
	for _, mode := range []bool{false, true} {
		text := `{"revisions":[{"id":"1","target":" first "},{"id":"1","target":"later"},{"id":"2","target":"` + big + `"}]}`
		if mode {
			text = "[revisions]\n1 | first\n1 | later\n2 | " + big
		}
		a, e := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), text, mode, true, []string{"1", "2"}, Options{JSONStructural: true})
		if e != nil {
			t.Fatal(e)
		}
		defer a.Release()
		r, e := a.Member(boundsForTest(t, 8192, 1<<20), 0)
		if e != nil || r.Revision == nil || r.Revision.Target != "first" {
			t.Fatalf("revision=%#v err=%v", r, e)
		}
		_, e = a.Member(boundsForTest(t, 8192, 1<<20), 1)
		var content *responsecontract.MemberContentError
		if !errors.As(e, &content) || content.Kind != responsecontract.MemberTooLarge || content.Ordinal != 1 || content.Limit != 8192 || content.Observed != int64(len(big)) {
			t.Fatal(e)
		}
	}
	a, e := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), `[{"id":"1","target":"bare"}]`, false, true, []string{"1"}, Options{JSONStructural: true})
	if e != nil {
		t.Fatal(e)
	}
	defer a.Release()
	r, e := a.Member(boundsForTest(t, 8192, 1<<20), 0)
	if e != nil || r.Revision == nil || r.Revision.Target != "bare" {
		t.Fatalf("bare=%v err=%v", r, e)
	}
}
func TestBoundedRetryReminderParity(t *testing.T) {
	text := "saved {{.MissingIDs}} {{.Reason}} {{.PreviousHead}}"
	ids := []string{"1", "3"}
	err := errors.New("bad JSON")
	head := strings.Repeat("x", 201)
	want, e := RenderRetryReminder(text, ids, err, head)
	if e != nil {
		t.Fatal(e)
	}
	got, e := RenderRetryReminderBounded(boundsForTest(t, 1<<20, 1<<20), text, ids, err, head, 1<<20)
	if e != nil || got != want {
		t.Fatalf("retry=%q expected=%q err=%v", got, want, e)
	}
}
