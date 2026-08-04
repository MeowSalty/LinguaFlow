package repair

import (
	"strings"
	"testing"
)

func TestTryRepairRubyAlignment_HappyPath(t *testing.T) {
	in := `{"ruby_output":[{"base":"漢字","text":"かんじ","kind":"phonetic"}]}`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" || entries[0].Text != "かんじ" || entries[0].Kind != "phonetic" {
		t.Errorf("wrong: %#v", entries)
	}
	if len(repaired) != 0 {
		t.Errorf("unexpected repair: %v", repaired)
	}
}

// TestTryRepairRubyAlignment_EmptyTruncated 截断型：括号未闭合。
func TestTryRepairRubyAlignment_EmptyTruncated(t *testing.T) {
	in := `{"ruby_output":{ "ruby_output": []}`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty, got %#v", entries)
	}
	if !contains(repaired, "json.robust-extract") && !contains(repaired, "json.close-braces") {
		t.Errorf("expected a structural repair op, got %v", repaired)
	}
}

// TestTryRepairRubyAlignment_StutteredPrefix 结巴/重复前缀。
func TestTryRepairRubyAlignment_StutteredPrefix(t *testing.T) {
	in := `{"ruby_output{"ruby_output":[{"base":"漢字","text":"かんじ","kind":"phonetic"}]}`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" {
		t.Errorf("wrong: %#v", entries)
	}
	if !contains(repaired, "json.robust-extract") {
		t.Errorf("expected json.robust-extract, got %v", repaired)
	}
}

// TestTryRepairRubyAlignment_TruncatedWithContent 截断含内容。
func TestTryRepairRubyAlignment_TruncatedWithContent(t *testing.T) {
	in := `{"ruby_output":[{"base":"漢字","text":"かんじ","kind":"phonetic"`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" {
		t.Errorf("wrong: %#v", entries)
	}
	if !contains(repaired, "json.robust-extract") && !contains(repaired, "json.close-braces") {
		t.Errorf("expected a structural repair op, got %v", repaired)
	}
}

func TestTryRepairRubyAlignment_TrailingComma(t *testing.T) {
	in := `{"ruby_output":[{"base":"漢字","text":"かんじ","kind":"phonetic",}]}`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" {
		t.Errorf("wrong: %#v", entries)
	}
	if !contains(repaired, "json.trailing-comma") {
		t.Errorf("expected json.trailing-comma, got %v", repaired)
	}
}

func TestTryRepairRubyAlignment_FiltersEmptyBase(t *testing.T) {
	in := `{"ruby_output":[{"base":"","text":"かんじ","kind":"phonetic"},{"base":"漢字","text":"かんじ","kind":"phonetic"}]}`
	entries, _, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" {
		t.Errorf("wrong: %#v", entries)
	}
}

func TestTryRepairRubyAlignment_FatalNotJSON(t *testing.T) {
	_, _, err := TryRepairRubyAlignment("totally not json", allOpts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("wrong err: %v", err)
	}
}
