package qa

import (
	"context"
	"testing"
)

func TestSubtitleLineCount_Mismatch(t *testing.T) {
	c := NewSubtitleLineCountChecker("srt")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "line1\nline2", TargetText: "only one"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckSubtitleLineCount {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestSubtitleLineCount_Match(t *testing.T) {
	c := NewSubtitleLineCountChecker("vtt")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a\nb", TargetText: "x\ny"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestSubtitleLineCount_DisabledForTxt(t *testing.T) {
	c := NewSubtitleLineCountChecker("txt")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a\nb", TargetText: "x"},
	})
	if len(issues) != 0 {
		t.Fatalf("txt should skip, got %d", len(issues))
	}
}

func TestSubtitleLineCount_ASS(t *testing.T) {
	c := NewSubtitleLineCountChecker("ass")
	if !c.enabled {
		t.Fatal("ass should enable")
	}
}
