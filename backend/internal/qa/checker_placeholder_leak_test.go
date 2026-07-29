package qa

import (
	"context"
	"testing"
)

func TestLeftoverPlaceholder_Hit(t *testing.T) {
	c := NewLeftoverPlaceholderChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "see __LF_000001__ here"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckLeftoverPlaceholder || issues[0].Severity != SeverityError {
		t.Errorf("issue=%+v", issues[0])
	}
	if issues[0].Span == nil || issues[0].Span.MatchedText != "__LF_000001__" {
		t.Errorf("span=%+v", issues[0].Span)
	}
}

func TestLeftoverPlaceholder_Clean(t *testing.T) {
	c := NewLeftoverPlaceholderChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "no placeholders"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestLeftoverPlaceholder_DedupSameKey(t *testing.T) {
	c := NewLeftoverPlaceholderChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "a", TargetText: "__LF_PH_0__ and __LF_PH_0__"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 deduped, got %d", len(issues))
	}
}
