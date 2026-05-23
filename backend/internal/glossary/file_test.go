package glossary

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileGlossary_LoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.csv")
	content := "source,target,case_sensitive,notes\n" +
		"LinguaFlow,灵枢,true,产品名\n" +
		"pipeline,流水线,false,\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	g, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if g.Len() != 2 {
		t.Fatalf("want 2 entries, got %d", g.Len())
	}
	if g.Dirty() {
		t.Error("freshly loaded should not be dirty")
	}

	// Save 后再 Load，结果一致。
	dst := filepath.Join(dir, "out.csv")
	g.path = dst
	if err := g.Save(context.Background()); err != nil {
		t.Fatalf("save: %v", err)
	}
	g2, err := LoadFile(dst)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if g2.Len() != 2 {
		t.Fatalf("reload want 2, got %d", g2.Len())
	}
}

func TestFileGlossary_LookupCaseSensitivity(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	_, _ = g.Add(context.Background(),
		Entry{Source: "API", Target: "接口", CaseSensitive: true},
		Entry{Source: "pipeline", Target: "流水线", CaseSensitive: false},
	)

	hits, _ := g.Lookup(context.Background(), "build an API for the Pipeline", "", "")
	if len(hits) != 2 {
		t.Fatalf("want 2 hits, got %d: %#v", len(hits), hits)
	}

	hits2, _ := g.Lookup(context.Background(), "build an api for the Pipeline", "", "")
	// "API" 是大小写敏感，"api" 不命中；"pipeline" 大小写不敏感，"Pipeline" 命中。
	if len(hits2) != 1 || hits2[0].Source != "pipeline" {
		t.Fatalf("want only pipeline hit, got %#v", hits2)
	}
}

func TestFileGlossary_LongTermFirst(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	_, _ = g.Add(context.Background(),
		Entry{Source: "Gemini", Target: "哈基米"},
		Entry{Source: "Gemini API", Target: "哈基米接口"},
	)
	hits, _ := g.Lookup(context.Background(), "we call the Gemini API today", "", "")
	if len(hits) != 2 {
		t.Fatalf("want both hits, got %d: %#v", len(hits), hits)
	}
	if hits[0].Source != "Gemini API" {
		t.Errorf("long term should come first, got %q", hits[0].Source)
	}
}

func TestFileGlossary_AddStrictMerge(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	_, _ = g.Add(context.Background(), Entry{Source: "Foo", Target: "原始译文"})
	_, _ = g.Add(context.Background(), Entry{Source: "foo", Target: "覆盖尝试"}) // 大小写不敏感，同 key
	_, _ = g.Add(context.Background(), Entry{Source: "Foo", Target: "再次覆盖"})

	if g.Len() != 1 {
		t.Fatalf("want 1 entry after strict merge, got %d", g.Len())
	}
	hits, _ := g.Lookup(context.Background(), "Foo here", "", "")
	if len(hits) != 1 || hits[0].Target != "原始译文" {
		t.Errorf("first write should win, got %#v", hits)
	}
}

func TestFileGlossary_AddDirtyFlag(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	if g.Dirty() {
		t.Error("empty should not be dirty")
	}
	_, _ = g.Add(context.Background(), Entry{Source: "x", Target: "x"})
	if !g.Dirty() {
		t.Error("after add should be dirty")
	}
	// 重复添加不应翻 dirty（已经 dirty 没事，但严格合并 no-op 不该再设）
	g2 := &FileGlossary{path: "x", bySource: map[string]int{}}
	_, _ = g2.Add(context.Background(), Entry{Source: "x", Target: "x"})
	g2.dirty = false
	_, _ = g2.Add(context.Background(), Entry{Source: "x", Target: "x"})
	if g2.Dirty() {
		t.Error("duplicate add should not set dirty")
	}
}

func TestFileGlossary_LoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	g, err := LoadFile(filepath.Join(dir, "does-not-exist.csv"))
	if err != nil {
		t.Fatalf("missing file should return empty, got %v", err)
	}
	if g.Len() != 0 {
		t.Errorf("want empty, got %d entries", g.Len())
	}
}

func TestFileGlossary_LoadWithoutHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.csv")
	content := "Foo,福\nBar,巴,true,note\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if g.Len() != 2 {
		t.Fatalf("want 2, got %d", g.Len())
	}
}

func TestFileGlossary_SaveCreatesDirAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "g.csv")
	g := &FileGlossary{path: path, bySource: map[string]int{}}
	_, _ = g.Add(context.Background(), Entry{Source: "x", Target: "ε"})
	if err := g.Save(context.Background()); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}
	g2, err := LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if g2.Len() != 1 {
		t.Errorf("round-trip lost rows: %d", g2.Len())
	}
}

func TestNopImplementsGlossary(t *testing.T) {
	var g Glossary = Nop{}
	if hits, err := g.Lookup(context.Background(), "anything", "", ""); err != nil || hits != nil {
		t.Errorf("Nop.Lookup should be empty, got %#v %v", hits, err)
	}
	if res, err := g.Add(context.Background(), Entry{Source: "x", Target: "y"}); err != nil || len(res.Added) != 0 || len(res.Skipped) != 0 {
		t.Errorf("Nop.Add should be no-op, got res=%#v err=%v", res, err)
	}
}

func TestFileGlossary_AddReportsConflictWithExisting(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	if _, err := g.Add(context.Background(), Entry{Source: "thread pool", Target: "线程池"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	res, err := g.Add(context.Background(), Entry{Source: "thread pool", Target: "并发池"})
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if len(res.Added) != 0 {
		t.Errorf("conflict should not write, got Added=%#v", res.Added)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("want 1 Skipped, got %d: %#v", len(res.Skipped), res.Skipped)
	}
	sk := res.Skipped[0]
	if sk.Reason != SkipReasonExists {
		t.Errorf("Reason want %q, got %q", SkipReasonExists, sk.Reason)
	}
	if sk.Proposed.Target != "并发池" {
		t.Errorf("Proposed.Target want %q, got %q", "并发池", sk.Proposed.Target)
	}
	if sk.Existing.Target != "线程池" {
		t.Errorf("Existing.Target want %q, got %q", "线程池", sk.Existing.Target)
	}
}

func TestFileGlossary_AddSameTargetIsNoop(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	if _, err := g.Add(context.Background(), Entry{Source: "foo", Target: "甲"}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	res, err := g.Add(context.Background(), Entry{Source: "foo", Target: "甲"})
	if err != nil {
		t.Fatalf("duplicate add: %v", err)
	}
	if len(res.Added) != 0 {
		t.Errorf("noop should not Add, got %#v", res.Added)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("identical target should not be reported as conflict, got %#v", res.Skipped)
	}
}

func TestFileGlossary_AddReportsEmptySkipped(t *testing.T) {
	g := &FileGlossary{path: "x", bySource: map[string]int{}}
	res, err := g.Add(context.Background(),
		Entry{Source: "", Target: "x"},
		Entry{Source: "y", Target: ""},
		Entry{Source: "z", Target: "z"},
	)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0].Source != "z" {
		t.Errorf("only the valid entry should be added, got %#v", res.Added)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("want 2 empty Skipped, got %d: %#v", len(res.Skipped), res.Skipped)
	}
	for _, sk := range res.Skipped {
		if sk.Reason != SkipReasonEmpty {
			t.Errorf("Reason want %q, got %q", SkipReasonEmpty, sk.Reason)
		}
	}
}
