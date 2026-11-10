package repair

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
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
		r, e := ParseMainMemberBounded(boundsForTest(t, 8192, 1<<20), text, textMode, []string{"1", "2"}, "1", Options{JSONStructural: true})
		if e != nil || r.Trans["1"] != "small" {
			t.Fatalf("sibling text=%v result=%v err=%v", textMode, r, e)
		}
		_, e = ParseMainMemberBounded(boundsForTest(t, 8192, 1<<20), text, textMode, nil, "2", Options{JSONStructural: true})
		if !errors.Is(e, backend.ErrBuildCapacity) {
			t.Fatalf("large selected: %v", e)
		}
		business, e := ParseMainMemberBounded(boundsForTest(t, 8192, 1<<20), text, textMode, nil, "", Options{JSONStructural: true})
		if e != nil || len(business.Glos) != 1 || business.Glos[0].Target != "sea" {
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
		for _, id := range []string{"1", "2"} {
			r, e := ParseMainMemberBounded(boundsForTest(t, 1<<20, 1<<20), text, false, nil, id, Options{JSONStructural: true, SchemaAliases: true})
			if old.Fatal {
				continue
			}
			if e != nil || r.Trans[id] != old.Trans[id] {
				t.Fatalf("member %s old=%q got=%v e=%v input=%q", id, old.Trans[id], r.Trans, e, text)
			}
		}
	}
	text := "[1] first\ncontinuation\n[2] second\n[ruby]\n1: first | reading | phonetic | #1\n[glossary]\n海 | sea"
	old := TryRepairText(text, []string{"1", "2"}, Options{})
	got := TryRepairText(text, []string{"1", "2"}, Options{Bounds: boundsForTest(t, 1<<20, 1<<20)})
	if !reflect.DeepEqual(old.Trans, got.Trans) || !reflect.DeepEqual(old.RubyOutput, got.RubyOutput) || !reflect.DeepEqual(old.Glos, got.Glos) {
		t.Fatalf("text old=%#v got=%#v", old, got)
	}
	r, e := ParseMainMemberBounded(boundsForTest(t, 1<<20, 1<<20), text, true, nil, "1", Options{})
	if e != nil || r.Trans["1"] != old.Trans["1"] || !reflect.DeepEqual(r.RubyOutput["1"], old.RubyOutput["1"]) {
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
		r, _, e := ParseReviseMemberBounded(boundsForTest(t, 8192, 1<<20), text, mode, "1", Options{JSONStructural: true})
		if e != nil || r == nil || r.Target != "first" {
			t.Fatalf("revision=%#v err=%v", r, e)
		}
		_, _, e = ParseReviseMemberBounded(boundsForTest(t, 8192, 1<<20), text, mode, "2", Options{JSONStructural: true})
		if !errors.Is(e, backend.ErrBuildCapacity) {
			t.Fatal(e)
		}
	}
	r, _, e := ParseReviseMemberBounded(boundsForTest(t, 8192, 1<<20), `[{"id":"1","target":"bare"}]`, false, "1", Options{JSONStructural: true})
	if e != nil || r == nil || r.Target != "bare" {
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
