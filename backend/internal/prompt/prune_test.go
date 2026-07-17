package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

const defaultTestPruneTmpl = `You are LinguaFlow, a glossary prune assistant.
Task: refine terms from {{.SourceLang}} to {{.TargetLang}}.
Reply as JSON: {"glossary":[...]}.`

func TestPruneRenderer_RendersTaskAndEntries(t *testing.T) {
	r, err := NewPruneRenderer(defaultTestPruneTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	sys, usr, err := r.Render(PruneData{
		SourceLang: "en", TargetLang: "zh",
		Entries: []PruneEntry{
			{Source: "Gemini", Target: "哈基米", Notes: "company"},
			{Source: "API", Target: "接口", Notes: ""},
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(sys, "refine terms") {
		t.Errorf("system prompt missing core instruction:\n%s", sys)
	}

	var env struct {
		Task       string       `json:"task"`
		SourceLang string       `json:"source_lang"`
		TargetLang string       `json:"target_lang"`
		Entries    []PruneEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v\n%s", err, usr)
	}
	if env.Task != "refine_glossary" {
		t.Errorf("task want refine_glossary, got %q", env.Task)
	}
	if len(env.Entries) != 2 || env.Entries[0].Source != "Gemini" {
		t.Errorf("entries mismatch: %#v", env.Entries)
	}
}

func TestPruneRenderer_EmptyEntries(t *testing.T) {
	r, err := NewPruneRenderer(defaultTestPruneTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	_, usr, err := r.Render(PruneData{
		SourceLang: "en", TargetLang: "zh",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var env struct {
		Task    string       `json:"task"`
		Entries []PruneEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v", err)
	}
	if env.Task != "refine_glossary" {
		t.Errorf("task want refine_glossary, got %q", env.Task)
	}
	// empty entries should serialize as [] not null
	if len(env.Entries) != 0 {
		t.Errorf("want empty entries, got %#v", env.Entries)
	}
}

func TestPruneRenderer_EmptyContent(t *testing.T) {
	if _, err := NewPruneRenderer(""); err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestPruneRenderer_LargeEntries(t *testing.T) {
	r, err := NewPruneRenderer(defaultTestPruneTmpl)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}
	entries := make([]PruneEntry, 0, 100)
	for i := 0; i < 100; i++ {
		entries = append(entries, PruneEntry{
			Source: "term-" + strings.Repeat("x", i%10),
			Target: "译",
			Notes:  "",
		})
	}
	_, usr, err := r.Render(PruneData{
		SourceLang: "en", TargetLang: "zh",
		Entries: entries,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var env struct {
		Entries []PruneEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(usr), &env); err != nil {
		t.Fatalf("user not json: %v", err)
	}
	if len(env.Entries) != 100 {
		t.Errorf("want 100 entries, got %d", len(env.Entries))
	}
}
