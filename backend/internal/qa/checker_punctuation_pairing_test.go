package qa

import (
	"context"
	"testing"
)

func TestPunctuationPairing_CJKUnclosed(t *testing.T) {
	c := NewPunctuationPairingChecker("zh")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: "他说「你好"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
	if issues[0].Code != CheckPunctuationPairing || issues[0].Severity != SeverityWarning {
		t.Errorf("issue=%+v", issues[0])
	}
}

func TestPunctuationPairing_LatinOK(t *testing.T) {
	c := NewPunctuationPairingChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: `He said "hello" (world).`},
	})
	if len(issues) != 0 {
		t.Fatalf("want 0, got %d: %+v", len(issues), issues)
	}
}

func TestPunctuationPairing_ApostropheIsNotAQuotePair(t *testing.T) {
	c := NewPunctuationPairingChecker("en")
	issues := c.Check(context.Background(), []CheckInput{{Index: 0, TargetText: "Don't change it."}})
	if len(issues) != 0 {
		t.Fatalf("apostrophe should not trigger pairing: %+v", issues)
	}
}

func TestPunctuationPairing_Misnested(t *testing.T) {
	c := NewPunctuationPairingChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: "a (b [c) d]"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1 misnested, got %d", len(issues))
	}
}

func TestPunctuationPairing_ExtraClose(t *testing.T) {
	c := NewPunctuationPairingChecker("en")
	issues := c.Check(context.Background(), []CheckInput{
		{Index: 0, SourceText: "x", TargetText: "hello)"},
	})
	if len(issues) != 1 {
		t.Fatalf("want 1, got %d", len(issues))
	}
}
