package protect

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

func TestBoundedProtectionMatchesDefaultRules(t *testing.T) {
	p := FromRules([]string{"code", "link", "placeholder", "xml"})
	for _, text := range []string{"hello {name} https", "[name](https://example.test \"title\") <tag>ctx</tag>", "`code` ${ABC}", "<a><b>{x}</b></a>"} {
		old := &model.Segment{Source: text}
		if e := p.Protect(old); e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		seg := &model.Segment{Source: text}
		if e := ProtectBounded(b, p, seg); e != nil {
			t.Fatal(e)
		}
		if old.Source != seg.Source || !reflect.DeepEqual(old.Protected, seg.Protected) {
			t.Fatalf("protection %q old=%q %v new=%q %v", text, old.Source, old.Protected, seg.Source, seg.Protected)
		}
		restored, e := RestoreTextBounded(b, seg.Source, seg.Protected, 1<<20)
		if e != nil || restored != RestoreText(seg.Source, seg.Protected) {
			t.Fatalf("restore %q %v", restored, e)
		}
	}
}
func TestBoundedPlaceholderExpansionRefusedBeforeCopy(t *testing.T) {
	b, _ := backend.NewBuildBounds(1024, 1024)
	_, e := RestoreTextBounded(b, strings.Repeat("__LF_000001__", 100), map[string]string{"__LF_000001__": strings.Repeat("x", 1000)}, 1024)
	if !errors.Is(e, backend.ErrBuildCapacity) || b.Snapshot().TextBytes != 0 {
		t.Fatalf("expansion copied %v snapshot=%v", e, b.Snapshot())
	}
}
