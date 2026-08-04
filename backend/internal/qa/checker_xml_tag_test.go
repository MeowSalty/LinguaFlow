package qa

import (
	"context"
	"testing"
)

func TestXMLTag_Mismatch(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<b>hi</b>`, TargetText: `<i>hi</i>`},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckXMLTagMismatch || issues[0].Severity != SeverityError {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestXMLTag_Match(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `<span class="x">a</span>`, TargetText: `<span class="x">甲</span>`},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

func TestXMLTag_SkipWhenSourceNoTags(t *testing.T) {
	c := NewXMLTagMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: `plain text`, TargetText: `<b>plain</b>`},
	})
	if len(issues) != 0 {
		t.Fatalf("source without tags should skip, got %d", len(issues))
	}
}
