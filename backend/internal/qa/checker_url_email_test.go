package qa

import (
	"context"
	"testing"
)

func TestURLEmail_Match(t *testing.T) {
	c := NewURLEmailMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: "see https://Example.COM/a and a@b.com",
			TargetText: "见 https://example.com/a 与 a@b.com",
		},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

func TestURLEmail_Mismatch(t *testing.T) {
	c := NewURLEmailMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{
			Index:      0,
			SourceText: "https://a.com",
			TargetText: "https://b.com",
		},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckURLEmailMismatch {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestURLEmail_NoLinks(t *testing.T) {
	c := NewURLEmailMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "hello", TargetText: "你好"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}
