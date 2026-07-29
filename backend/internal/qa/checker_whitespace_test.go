package qa

import (
	"context"
	"testing"
)

func TestWhitespaceIrregular_ZWSP(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello\u200bworld"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckWhitespaceIrregular {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestWhitespaceIrregular_NBSP(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello\u00a0world"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
}

func TestWhitespaceIrregular_TrimOnlyExempt(t *testing.T) {
	c := NewWhitespaceIrregularChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "  hello world  "},
	})
	if len(issues) != 0 {
		t.Fatalf("trim-only should not flag, got %d", len(issues))
	}
}

func TestRepeatedSpace_Double(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello  world"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckRepeatedSpace {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestRepeatedSpace_CJKBetween(t *testing.T) {
	c := NewRepeatedSpaceChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "你 好"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 CJK space, got %d", len(issues))
	}
}

func TestRepeatedSpace_LatinOK(t *testing.T) {
	c := NewRepeatedSpaceChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "hello world"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}
