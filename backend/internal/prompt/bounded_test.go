package prompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBoundedRenderPreservesTranslateAndRevise(t *testing.T) {
	for _, protocol := range []Protocol{ProtocolText, ProtocolJSONLoose, ProtocolJSONStrict} {
		for _, mode := range []string{RubyModeInline, RubyModeSection} {
			r, e := NewRenderer(`{{.SourceLang}}/{{.TargetLang}} {{if .HasRuby}}ruby{{end}} {{range .TMHints}}{{printf "%.0f" (mul .Score 100)}}{{end}}`)
			if e != nil {
				t.Fatal(e)
			}
			d := Data{SourceLang: "ja", TargetLang: "zh", Segments: []SegmentInput{{ID: "1", Source: "海风", Translate: true}, {ID: "x", Source: "ctx"}}, TMHints: []TMHint{{Score: .5}}, Protocol: protocol, RubyMode: mode, RubyAnnotations: map[string][]RubyAnnotation{"1": {{ID: "r1", Base: "海", Text: "うみ"}}}}
			s, u, e := r.Render(d)
			if e != nil {
				t.Fatal(e)
			}
			b, _ := backend.NewBuildBounds(1<<20, 1<<20)
			bs, bu, e := r.RenderBounded(b, d, 1<<20)
			if e != nil || s != bs || u != bu {
				t.Fatalf("render %s/%s old=%q %q bounded=%q %q err=%v", protocol, mode, s, u, bs, bu, e)
			}
		}
	}
}
func TestBoundedTemplatePureFunctionsAndClaim(t *testing.T) {
	for _, source := range []string{`{{define "x"}}{{.Source}}{{end}}{{template "x" .}}`, `{{printf "%[2]*[1]s" .Source 5}}/{{print 1 2}}/{{println 1 2}}`, `{{html .Source}}/{{js .Source}}/{{urlquery .Source}}`, `{{range .Vars}}{{.}}{{end}}`} {
		r, e := NewRenderer(source)
		if e != nil {
			t.Fatal(e)
		}
		d := Data{Source: "<&\"海", Vars: map[string]any{"a": "first", "b": "second"}, Protocol: ProtocolText}
		s, u, e := r.Render(d)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		claimed := false
		bs, bu, e := r.RenderWithCreditsBounded(b, d, 1<<20, func(m backend.PromptMeasure) error {
			claimed = true
			es, _ := backend.MeasureJSONString(s)
			eu, _ := backend.MeasureJSONString(u)
			if m.SystemBytes != int64(len(s)) || m.UserBytes != int64(len(u)) || m.SystemJSONBytes != es || m.UserJSONBytes != eu {
				return fmt.Errorf("measure mismatch %#v %q %q", m, s, u)
			}
			return nil
		})
		if e != nil || !claimed || bs != s || bu != u {
			t.Fatalf("template=%q old=%q %q got=%q %q err=%v", source, s, u, bs, bu, e)
		}
	}
	r, _ := NewRenderer(`{{.Source}}`)
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	stop := errors.New("claim denied")
	s, u, e := r.RenderWithCreditsBounded(b, Data{Source: "large prompt"}, 1<<20, func(backend.PromptMeasure) error { return stop })
	if !errors.Is(e, stop) || s != "" || u != "" {
		t.Fatalf("claim denial=%q %q %v", s, u, e)
	}
	r, _ = NewRenderer(`{{define "x"}}{{template "x" .}}{{end}}{{template "x" .}}`)
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	_, _, e = r.RenderBounded(b, Data{}, 1<<20)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("recursive template=%v", e)
	}
}

func TestBoundedRenderControlEscapesClaimParity(t *testing.T) {
	const template = "static\n\t\r\b\f {{.SourceLang}}\n"
	translate, err := NewRenderer(template)
	if err != nil {
		t.Fatal(err)
	}
	d := Data{SourceLang: "ja\n\t\r\b\f", TargetLang: "en", Source: "A|B\n\t\r\b\f", Protocol: ProtocolText}
	revise, err := NewReviseRenderer(template)
	if err != nil {
		t.Fatal(err)
	}
	rd := ReviseData{SourceLang: d.SourceLang, TargetLang: "en", Segments: []ReviseSegment{{ID: "1", Source: d.Source, Target: "old\n\t\r\b\f"}}, Protocol: ProtocolText}
	check := func(old func() (string, string, error), bounded func(*backend.BuildBounds, func(backend.PromptMeasure) error) (string, string, error)) {
		t.Helper()
		s, u, err := old()
		if err != nil {
			t.Fatal(err)
		}
		claimed := false
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		bs, bu, err := bounded(b, func(m backend.PromptMeasure) error {
			claimed = true
			systemJSON, _ := json.Marshal(s)
			userJSON, _ := json.Marshal(u)
			if m.SystemBytes != int64(len(s)) || m.UserBytes != int64(len(u)) || m.SystemJSONBytes != int64(len(systemJSON)) || m.UserJSONBytes != int64(len(userJSON)) {
				return fmt.Errorf("canonical control prompt claim mismatch: %#v", m)
			}
			return nil
		})
		if err != nil || !claimed || bs != s || bu != u {
			t.Fatalf("bounded control prompt changed: claimed=%t err=%v", claimed, err)
		}
	}
	check(func() (string, string, error) { return translate.Render(d) }, func(b *backend.BuildBounds, claim func(backend.PromptMeasure) error) (string, string, error) {
		return translate.RenderWithCreditsBounded(b, d, 1<<20, claim)
	})
	check(func() (string, string, error) { return revise.Render(rd) }, func(b *backend.BuildBounds, claim func(backend.PromptMeasure) error) (string, string, error) {
		return revise.RenderWithCreditsBounded(b, rd, 1<<20, claim)
	})
}
func TestBoundedTemplateRejectsExpansionBeforeFormatting(t *testing.T) {
	r, _ := NewRenderer(`{{printf "%1000000000s" .Source}}`)
	b, _ := backend.NewBuildBounds(1024, 1024)
	_, _, e := r.RenderBounded(b, Data{Source: "x"}, 1024)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("huge format: %v", e)
	}
	r, _ = NewRenderer(`{{.Source}}`)
	b, _ = backend.NewBuildBounds(1024, 1024)
	_, _, e = r.RenderBounded(b, Data{Source: strings.Repeat("x", 1025)}, 1024)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("huge source: %v", e)
	}
}
func TestBoundedTemplateConstructorChecksDepthBeforeParse(t *testing.T) {
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	_, e := NewRendererBounded(b, strings.Repeat("{{if\t.Source}}", 65)+"x"+strings.Repeat("{{end}}", 65))
	if !errors.Is(e, backend.ErrBuildCapacity) || b.Snapshot().TextBytes != 0 {
		t.Fatalf("constructor depth=%v snapshot=%+v", e, b.Snapshot())
	}
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	r, e := NewRendererBounded(b, `{{define "x"}}{{.Source}}{{end}}{{template "x" .}}`)
	if e != nil {
		t.Fatal(e)
	}
	_, _, e = r.RenderBounded(b, Data{Source: "sea"}, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
}

func TestBoundedTemplateFormatTracksWidthArguments(t *testing.T) {
	for _, width := range []any{uint(1000000000), uint64(1000000000), int64(-1000000000)} {
		r, _ := NewRenderer(`{{printf "%*s" .Vars.width .Source}}`)
		b, _ := backend.NewBuildBounds(1024, 1024)
		_, _, e := r.RenderBounded(b, Data{Source: "x", Vars: map[string]any{"width": width}}, 1024)
		if !errors.Is(e, backend.ErrBuildCapacity) {
			t.Fatalf("width %T: %v", width, e)
		}
	}
	for _, source := range []string{
		`{{printf "%*d" 2 .Vars.large}}`,
		`{{printf "%[2]*[1]d" .Vars.large 2}}`,
		`{{printf "%[3]*.[2]*[1]f" .Vars.number 2 .Vars.width}}`,
		`{{printf "%10.20f" .Vars.number}}`,
	} {
		r, _ := NewRenderer(source)
		d := Data{Vars: map[string]any{"large": int64(9999999), "number": 1.25, "width": uint(8)}}
		s, u, e := r.Render(d)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(8192, 8192)
		bs, bu, e := r.RenderBounded(b, d, 1024)
		if e != nil || bs != s || bu != u {
			t.Fatalf("format %q old=%q/%q got=%q/%q: %v", source, s, u, bs, bu, e)
		}
	}
}

type boundedIgnoredFormatField struct {
	Hidden string `json:"-"`
}

func TestBoundedFormattingUsesReflectiveOutputBound(t *testing.T) {
	for _, source := range []string{`{{.Vars.value}}`, `{{printf "%v" .Vars.value}}`, `{{print .Vars.value}}`, `{{printf "" .Vars.value}}`} {
		r, _ := NewRenderer(source)
		d := Data{Vars: map[string]any{"value": boundedIgnoredFormatField{Hidden: strings.Repeat("x", 2048)}}}
		b, _ := backend.NewBuildBounds(8192, 8192)
		_, _, e := r.RenderBounded(b, d, 1024)
		if !errors.Is(e, backend.ErrBuildCapacity) {
			t.Fatalf("hidden field %q: %v", source, e)
		}
	}
	for _, test := range []struct {
		source string
		value  any
	}{
		{`{{printf "%f" .Vars.value}}`, math.MaxFloat64},
		{`{{printf "%60v" .Vars.value}}`, []int{1, 2}},
		{`{{printf "%s" "ok" .Vars.value}}`, 123},
		{`{{printf "%f" 1e308+1e308i}}`, 123},
	} {
		r, _ := NewRenderer(test.source)
		d := Data{Vars: map[string]any{"value": test.value}}
		s, u, e := r.Render(d)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		bs, bu, e := r.RenderBounded(b, d, 1<<20)
		if e != nil || bs != s || bu != u {
			t.Fatalf("reflective fmt %q: %v", test.source, e)
		}
	}
	b, _ := backend.NewBuildBounds(8192, 8192)
	n, _, e := measureFmt(b, reflect.ValueOf(boundedIgnoredFormatField{Hidden: strings.Repeat("x", 2048)}), 0)
	if e != nil || n < 2048 || b.Snapshot().TextBytes != 0 {
		t.Fatalf("pre-format measurement n=%d err=%v credit=%+v", n, e, b.Snapshot())
	}
	r, _ := NewRenderer(`{{printf "%500v" .Vars.value}}`)
	b, _ = backend.NewBuildBounds(8192, 8192)
	_, _, e = r.RenderBounded(b, Data{Vars: map[string]any{"value": []int{1, 2}}}, 1500)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("composite per-member width: %v", e)
	}
}

func TestBoundedTemplateComparisonDoesNotRenderInvalidValues(t *testing.T) {
	r, _ := NewRenderer(`{{eq .Vars.value .Vars.value}}`)
	b, _ := backend.NewBuildBounds(1<<20, 1<<20)
	_, _, e := r.RenderBounded(b, Data{Vars: map[string]any{"value": []string{strings.Repeat("x", 128<<10)}}}, 1<<20)
	if e == nil || len(e.Error()) > 2048 {
		t.Fatalf("comparison diagnostic expanded value: %v", e)
	}
	r, _ = NewRenderer(`{{range .Source}}x{{end}}`)
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	_, _, e = r.RenderBounded(b, Data{Source: strings.Repeat("x", 128<<10)}, 1<<20)
	if e == nil || len(e.Error()) > 2048 {
		t.Fatalf("range diagnostic expanded value: %v", e)
	}
	r, _ = NewRenderer(`{{range 1000000000}}x{{end}}`)
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	_, _, e = r.RenderBounded(b, Data{}, 1<<20)
	if !errors.Is(e, backend.ErrBuildCapacity) {
		t.Fatalf("integer range wasn't precredited: %v", e)
	}
	r, _ = NewRenderer(`{{eq "` + strings.Repeat("x", 128<<10) + `" .Vars.value}}`)
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	_, _, e = r.RenderBounded(b, Data{Vars: map[string]any{"value": 3}}, 1<<20)
	if e == nil || len(e.Error()) > 2048 {
		t.Fatalf("literal context expanded body: %v", e)
	}
	r, _ = NewRenderer(`{{printf "%d" ` + strings.Repeat("0", 1024) + `3}}`)
	b, _ = backend.NewBuildBounds(1<<20, 1<<20)
	s, _, e := r.RenderBounded(b, Data{}, 1<<20)
	if e != nil || s != "3" {
		t.Fatalf("long numeric flavor/value: %q %v", s, e)
	}
	for _, source := range []string{`{{eq .Vars.value 2 3}}/{{ne .Vars.value 4}}`, `{{eq .Vars.missing nil}}`, `{{eq .Vars.object .Vars.object}}`} {
		r, _ := NewRenderer(source)
		d := Data{Vars: map[string]any{"value": uint(3), "object": struct{ A int }{A: 1}}}
		s, u, e := r.Render(d)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := backend.NewBuildBounds(1<<20, 1<<20)
		bs, bu, e := r.RenderBounded(b, d, 1<<20)
		if e != nil || bs != s || bu != u {
			t.Fatalf("comparison %q parity: %v", source, e)
		}
	}
}
