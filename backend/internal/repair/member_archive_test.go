package repair

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestMemberResponseArchiveLinear(t *testing.T) {
	for _, n := range []int{32, 64, 128, 256} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var source strings.Builder
			source.WriteString(`{"translations":{`)
			ids := make([]string, n)
			for i := range ids {
				ids[i] = fmt.Sprintf("%03d", i)
				if i > 0 {
					source.WriteByte(',')
				}
				fmt.Fprintf(&source, `"%s":{"target":"same target","source":"unused"}`, ids[i])
			}
			source.WriteString(`},"glossary":[{"source":"海","target":"sea"}]}`)
			text := source.String()
			prepare := boundsForTest(t, 1<<20, 1<<20)
			a, err := PrepareArchive(prepare, text, false, false, ids, Options{JSONStructural: true})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Release()
			before := a.Work()
			var decoded int64
			for repeat := 0; repeat < 2; repeat++ {
				for ordinal := range ids {
					b := boundsForTest(t, 1024, 1024)
					m, err := a.Member(b, ordinal)
					if err != nil || m.Target != "same target" || m.Missing || m.Failure != "" {
						t.Fatalf("member=%#v err=%v", m, err)
					}
					decoded += int64(len(m.Target))
				}
			}
			if a.Work() != before {
				t.Fatal("member extraction rescanned or rebuilt preparation")
			}
			if before.JSONScans != 2 || before.JSONScanBytes != 2*int64(len(text)) {
				t.Fatalf("JSONView scan count: %+v", before)
			}
			if before.IDDecodeBytes > int64(len(text)) || decoded != int64(2*n*len("same target")) {
				t.Fatalf("nonlinear decoding: %+v decoded=%d", before, decoded)
			}
			glossary, err := a.Glossary(boundsForTest(t, 8192, 8192))
			if err != nil || len(glossary) != 1 || glossary[0].Target != "sea" {
				t.Fatalf("glossary=%v err=%v", glossary, err)
			}
			t.Logf("members=%d source=%d source_hash=0 JSON_scans=%d JSON_scan_input=%d selection=%d id_decode=%d member_decoded=%d", n, len(text), before.JSONScans, before.JSONScanBytes, before.SelectionBytes, before.IDDecodeBytes, decoded)
		})
	}
}

func TestMemberResponseArchiveSemantics(t *testing.T) {
	tests := []struct {
		name, text       string
		textMode, revise bool
		want             [3]string
		missing          [3]bool
		failure          [3]bool
	}{
		{name: "object last-match", text: `{"translations":{"1":"first","1":{"translation":"last","target":""},"2":{"text":"second"}}}`, want: [3]string{"last", "second", ""}, missing: [3]bool{false, false, true}},
		{name: "array last-match", text: `{"translations":[{"id":"1","target":"first"},{"id":"1","target":"last"},{"id":"2","target":3}]}`, want: [3]string{"last", "", ""}, missing: [3]bool{false, false, true}, failure: [3]bool{false, true, false}},
		{name: "alias priority", text: `{"result":{"1":"result"},"translation":{"1":"alias"}}`, want: [3]string{"alias", "", ""}, missing: [3]bool{false, true, true}},
		{name: "data alias", text: `{"data":{"translations":{"1":"nested"}}}`, want: [3]string{"nested", "", ""}, missing: [3]bool{false, true, true}},
		{name: "ordinary empty string", text: `{"translations":{"1":"","2":{"target":"","text":"yes"}}}`, want: [3]string{"", "yes", ""}, missing: [3]bool{false, false, true}},
		{name: "trailing comma", text: `{"translations":{"1":"yes",}}`, want: [3]string{"yes", "", ""}, missing: [3]bool{false, true, true}},
		{name: "truncated sibling", text: `{"translations":{"1":"yes","2":"bro`, want: [3]string{"yes", "", ""}, missing: [3]bool{false, true, true}},
		{name: "text sections", text: "[1] first\ncontinuation\n[2] second\n[*] ignored\n[glossary]\n海 | sea\n[1] last\nlast continuation", textMode: true, want: [3]string{"last\nlast continuation", "second", ""}, missing: [3]bool{false, false, true}},
		{name: "revise first-valid", text: `{"revisions":[{"id":"1","target":"\u0020\t"},{"id":" 1 ","target":" first "},{"id":"1","target":"last"}]}`, revise: true, want: [3]string{"first", "", ""}, missing: [3]bool{false, true, true}},
		{name: "empty revise", text: `{"revisions":[]}`, revise: true, missing: [3]bool{true, true, true}},
		{name: "revise bare", text: `[{"id":"1","target":" bare "}]`, revise: true, want: [3]string{"bare", "", ""}, missing: [3]bool{false, true, true}},
		{name: "revise text", text: "[revisions]\n1 | first\n1 | last\n[glossary]\n2 | no", textMode: true, revise: true, want: [3]string{"first", "", ""}, missing: [3]bool{false, true, true}},
		{name: "revise text JSON fallback", text: `{"revisions":[{"id":"1","target":"json"}]}`, textMode: true, revise: true, want: [3]string{"json", "", ""}, missing: [3]bool{false, true, true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), tc.text, tc.textMode, tc.revise, []string{"1", "2", "3"}, Options{JSONStructural: true, SchemaAliases: true})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Release()
			for ordinal := range 3 {
				m, err := a.Member(boundsForTest(t, 8192, 1<<20), ordinal)
				if err != nil {
					t.Fatal(err)
				}
				target := m.Target
				if m.Revision != nil {
					target = m.Revision.Target
				}
				if target != tc.want[ordinal] || m.Missing != tc.missing[ordinal] || (m.Failure != "") != tc.failure[ordinal] {
					t.Fatalf("ordinal=%d member=%#v expected=%q missing=%v failure=%v", ordinal, m, tc.want[ordinal], tc.missing[ordinal], tc.failure[ordinal])
				}
			}
		})
	}
}

func TestMemberResponseArchiveRubyAndLimits(t *testing.T) {
	const rows = `[{"base":"word","text":"read","kind":"phonetic","occurrence":1}]`
	text := `{"translations":{"1":{"target":"one","ruby_output":` + rows + `},"2":"two","3":{"target":"three","ruby_output":` + rows + `,"ruby_output":` + rows + `}},"ruby_output":{"1":` + rows + `,"1":` + rows + `,"2":` + rows + `}}`
	a, err := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), text, false, false, []string{"1", "2", "3"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for ordinal, wantRuby := range []int{1, 1, 0} {
		m, err := a.Member(boundsForTest(t, 8192, 8192), ordinal)
		if err != nil || len(m.RubyOutput) != wantRuby {
			t.Fatalf("ordinal=%d ruby=%#v err=%v", ordinal, m.RubyOutput, err)
		}
	}
	a.Release()
	a.Release()
	if _, err := a.Member(boundsForTest(t, 8192, 8192), 0); err == nil {
		t.Fatal("released archive remained readable")
	}
	big := strings.Repeat("x", 12288)
	for _, textMode := range []bool{false, true} {
		body := `{"translations":{"1":"small","2":"` + big + `"}}`
		if textMode {
			body = "[1] small\n[2] " + big
		}
		a, err := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), body, textMode, false, []string{"1", "2"}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		m, err := a.Member(boundsForTest(t, 8192, 1<<20), 0)
		if err != nil || m.Target != "small" {
			t.Fatalf("small sibling=%v err=%v", m, err)
		}
		if _, err = a.Member(boundsForTest(t, 8192, 1<<20), 1); !errors.Is(err, backend.ErrBuildCapacity) {
			t.Fatalf("large selected error=%v", err)
		}
		a.Release()
	}
}

func TestMemberResponseArchiveConcurrentAndDamage(t *testing.T) {
	a, err := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), `{"translations":{"1":"one","2":"two"}}`, false, false, []string{"1", "2"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	before := a.Work()
	var wg sync.WaitGroup
	for ordinal := range 2 {
		wg.Go(func() {
			m, e := a.Member(boundsForTest(t, 1024, 1024), ordinal)
			if e != nil || m.Missing {
				t.Errorf("member=%#v err=%v", m, e)
			}
		})
	}
	wg.Wait()
	if !reflect.DeepEqual(before, a.Work()) {
		t.Fatal("concurrent extraction mutated preparation")
	}
	if _, err := a.Member(boundsForTest(t, 8192, 8192), 2); err == nil {
		t.Fatal("wrong ordinal accepted")
	}
	for _, body := range []string{`not JSON`, `{"translations":3}`, `{"wrong":{"1":"one"}}`} {
		if a, err := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), body, false, false, []string{"1"}, Options{}); err == nil {
			a.Release()
			t.Fatalf("malformed body accepted: %s", body)
		}
	}
	if a, err := PrepareArchive(boundsForTest(t, 1<<20, 1<<20), `{"translations":{}}`, false, false, []string{"1", "1"}, Options{}); err == nil {
		a.Release()
		t.Fatal("duplicate frozen identity accepted")
	}
}
