package jsonp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func TestNormalizeFormat(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"yml", "yaml"},
		{".toml", "toml"},
		{"TOML", "toml"},
		{".YML", "yaml"},
		{"", ""},
		{"  json  ", "json"},
		{"yaml", "yaml"},
	}
	for _, tc := range cases {
		if got := normalizeFormat(tc.in); got != tc.want {
			t.Errorf("normalizeFormat(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"object", `{"a":1}`, "json"},
		{"array", `[1,2]`, "json"},
		{"table", "[table]\nkey = \"v\"", "toml"},
		{"hash_toml", "# c\nk = \"v\"", "toml"},
		{"hash_yaml", "# c\nk: v", "yaml"},
		{"eq_toml", `k = "v"`, "toml"},
		{"colon_yaml", "k: v", "yaml"},
		{"empty", "", "json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectFormat(tc.content); got != tc.want {
				t.Errorf("detectFormat(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestParseTOML(t *testing.T) {
	src := `
title = "Hello"
enabled = true
count = 42
pi = 3.14
created = 2020-01-02T15:04:05Z
items = ["a", "b"]
inline = { x = "ix", y = 1 }

[nested]
name = "World"

[[people]]
name = "Alice"

[[people]]
name = "Bob"
`
	doc, err := New().Parse(context.Background(), strings.NewReader(src), "toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "toml" {
		t.Fatalf("Format = %q, want toml", doc.Format)
	}

	byPath := map[string]string{}
	for _, seg := range doc.Segments {
		path, _ := seg.Meta["path"].(string)
		byPath[path] = seg.Source
	}

	want := map[string]string{
		"title":          "Hello",
		"nested.name":    "World",
		"items[0]":       "a",
		"items[1]":       "b",
		"people[0].name": "Alice",
		"people[1].name": "Bob",
		"inline.x":       "ix",
	}
	for path, s := range want {
		if byPath[path] != s {
			t.Errorf("path %q = %q, want %q (all=%v)", path, byPath[path], s, byPath)
		}
	}
	// non-string leaves must not appear
	for _, forbidden := range []string{"enabled", "count", "pi", "created", "inline.y"} {
		if _, ok := byPath[forbidden]; ok {
			t.Errorf("non-string path %q should not be a segment", forbidden)
		}
	}
}

func TestParseTrustsFormatHint(t *testing.T) {
	// Content starts with [table] — old detectFormat would misclassify as JSON.
	src := "[table]\nkey = \"value\""
	doc, err := New().Parse(context.Background(), strings.NewReader(src), "toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "toml" {
		t.Fatalf("Format = %q, want toml", doc.Format)
	}
	if len(doc.Segments) != 1 || doc.Segments[0].Source != "value" {
		t.Fatalf("segments = %+v", doc.Segments)
	}
}

func TestRenderTOMLRoundTrip(t *testing.T) {
	src := `
title = "Hello"
enabled = true
count = 42
pi = 3.14
created = 2020-01-02T15:04:05Z
[nested]
name = "World"
`
	p := New()
	doc, err := p.Parse(context.Background(), strings.NewReader(src), "toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i := range doc.Segments {
		doc.Segments[i].Target = "T-" + doc.Segments[i].Source
	}

	var out bytes.Buffer
	if err := p.Render(context.Background(), doc, strings.NewReader(src), &out); err != nil {
		t.Fatalf("Render: %v", err)
	}

	doc2, err := p.Parse(context.Background(), bytes.NewReader(out.Bytes()), "toml")
	if err != nil {
		t.Fatalf("re-Parse: %v", err)
	}
	byPath := map[string]string{}
	for _, seg := range doc2.Segments {
		path, _ := seg.Meta["path"].(string)
		byPath[path] = seg.Source
	}
	if byPath["title"] != "T-Hello" || byPath["nested.name"] != "T-World" {
		t.Fatalf("translated paths = %v", byPath)
	}

	// Re-parse original tree via Render path checks non-string preservation:
	// unmarshal rendered and inspect via second parse of a known structure.
	// datetime/bool/int/float must still not appear as string segments.
	for _, forbidden := range []string{"enabled", "count", "pi", "created"} {
		if _, ok := byPath[forbidden]; ok {
			t.Errorf("non-string path %q leaked into segments after render", forbidden)
		}
	}

	// Ensure rendered content still contains non-string values.
	rendered := out.String()
	for _, needle := range []string{"true", "42", "3.14", "2020-01-02"} {
		if !strings.Contains(rendered, needle) {
			t.Errorf("rendered missing non-string value %q:\n%s", needle, rendered)
		}
	}
}

func TestRenderUsesDocFormat(t *testing.T) {
	src := "[table]\nkey = \"value\""
	doc := &pipeline.Document{
		Format: "toml",
		Segments: []pipeline.Segment{
			{Source: "value", Target: "译值", Meta: map[string]any{"path": "table.key"}},
		},
	}
	var out bytes.Buffer
	if err := New().Render(context.Background(), doc, strings.NewReader(src), &out); err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Must be TOML, not JSON array.
	s := out.String()
	if strings.HasPrefix(strings.TrimSpace(s), "[") && !strings.Contains(s, "key") {
		t.Fatalf("unexpected render output: %s", s)
	}
	if !strings.Contains(s, "译值") {
		t.Fatalf("missing translation: %s", s)
	}
}

func TestEmptyParseRender(t *testing.T) {
	p := New()
	doc, err := p.Parse(context.Background(), strings.NewReader(""), "toml")
	if err != nil {
		t.Fatalf("Parse empty: %v", err)
	}
	if doc.Format != "toml" {
		t.Fatalf("Format = %q", doc.Format)
	}
	var out bytes.Buffer
	if err := p.Render(context.Background(), doc, strings.NewReader(""), &out); err != nil {
		t.Fatalf("Render empty: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected empty render, got %q", out.String())
	}
}

func TestJSONRoundTrip(t *testing.T) {
	src := `{"title":"Hello","nested":{"name":"World"},"n":1}`
	p := New()
	doc, err := p.Parse(context.Background(), strings.NewReader(src), "json")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i := range doc.Segments {
		doc.Segments[i].Target = "T-" + doc.Segments[i].Source
	}
	var out bytes.Buffer
	if err := p.Render(context.Background(), doc, strings.NewReader(src), &out); err != nil {
		t.Fatalf("Render: %v", err)
	}
	doc2, err := p.Parse(context.Background(), bytes.NewReader(out.Bytes()), "json")
	if err != nil {
		t.Fatalf("re-Parse: %v", err)
	}
	byPath := map[string]string{}
	for _, seg := range doc2.Segments {
		path, _ := seg.Meta["path"].(string)
		byPath[path] = seg.Source
	}
	if byPath["title"] != "T-Hello" || byPath["nested.name"] != "T-World" {
		t.Fatalf("got %v", byPath)
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	src := "title: Hello\nnested:\n  name: World\nn: 1\n"
	p := New()
	doc, err := p.Parse(context.Background(), strings.NewReader(src), "yaml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i := range doc.Segments {
		doc.Segments[i].Target = "T-" + doc.Segments[i].Source
	}
	var out bytes.Buffer
	if err := p.Render(context.Background(), doc, strings.NewReader(src), &out); err != nil {
		t.Fatalf("Render: %v", err)
	}
	doc2, err := p.Parse(context.Background(), bytes.NewReader(out.Bytes()), "yaml")
	if err != nil {
		t.Fatalf("re-Parse: %v", err)
	}
	byPath := map[string]string{}
	for _, seg := range doc2.Segments {
		path, _ := seg.Meta["path"].(string)
		byPath[path] = seg.Source
	}
	if byPath["title"] != "T-Hello" || byPath["nested.name"] != "T-World" {
		t.Fatalf("got %v", byPath)
	}
}
