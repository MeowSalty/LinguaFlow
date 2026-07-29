package qa

import (
	"context"
	"testing"
)

func TestNumberMismatch_EqualNormalized(t *testing.T) {
	c := NewNumberMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "price is 1,234.5", TargetText: "价格为 1234.5"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0 equal after norm, got %d: %+v", len(issues), issues)
	}
}

func TestNumberMismatch_thousandDot(t *testing.T) {
	c := NewNumberMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "1.234.567", TargetText: "1234567"},
	})
	if len(issues) != 0 {
		t.Fatalf("European thousands should match, got %d", len(issues))
	}
}

func TestNumberMismatch_missing(t *testing.T) {
	c := NewNumberMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "version 2.0 and 3", TargetText: "版本 2.0"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckNumberMismatch {
		t.Errorf("code=%s", issues[0].Code)
	}
}

func TestNumberMismatch_noNumbers(t *testing.T) {
	c := NewNumberMismatchChecker()
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "hello", TargetText: "你好"},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d", len(issues))
	}
}

func TestNormalizeNumberToken(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1234", "1234"},
		{"1,234", "1234"},
		{"1.234.567", "1234567"},
		{"1,234.56", "1234.56"},
		{"1.234,56", "1234.56"},
	}
	for _, tt := range tests {
		if got := normalizeNumberToken(tt.in); got != tt.want {
			t.Errorf("normalizeNumberToken(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}
