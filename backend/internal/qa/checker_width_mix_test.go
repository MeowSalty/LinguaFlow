package qa

import (
	"context"
	"testing"
)

func TestWidthMix_CJKHalfwidthPunct(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "你好,世界"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckWidthMix {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestWidthMix_CJKFullwidthOK(t *testing.T) {
	c := NewWidthMixChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "你好，世界"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestWidthMix_LatinFullwidth(t *testing.T) {
	c := NewWidthMixChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "Hello，world"}, // fullwidth comma U+FF0C
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
}

func TestWidthMix_LatinOK(t *testing.T) {
	c := NewWidthMixChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "Hello, world!"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}
