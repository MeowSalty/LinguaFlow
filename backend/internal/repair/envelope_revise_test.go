package repair

import (
	"strings"
	"testing"
)

func TestTryRepairReviseWithRuby_HappyPath(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后"}]}`
	revs, _, repaired, err := tryRepairReviseWithRuby(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(revs) != 1 || revs[0].ID != "1" || revs[0].Target != "修订后" {
		t.Errorf("wrong: %#v", revs)
	}
	if len(repaired) != 0 {
		t.Errorf("unexpected repair: %v", repaired)
	}
}

func TestTryRepairReviseWithRuby_NormalizesAndDeduplicates(t *testing.T) {
	in := `{"revisions":[{"id":" 1 ","target":" first "},{"id":"1","target":"second"},{"id":"2","target":""},{"id":"3","target":" third "}]}`
	revs, _, _, err := tryRepairReviseWithRuby(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 2 || revs[0].ID != "1" || revs[0].Target != "first" || revs[1].ID != "3" {
		t.Fatalf("wrong: %#v", revs)
	}
}

func TestTryRepairReviseWithRuby_TruncatedAndTrailingComma(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后",}]`
	revs, _, repaired, err := tryRepairReviseWithRuby(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Errorf("wrong: %#v", revs)
	}
	if len(repaired) == 0 {
		t.Errorf("expected repair ops")
	}
}

func TestTryRepairReviseWithRuby_CodeFence(t *testing.T) {
	in := "```json\n{\"revisions\":[{\"id\":\"1\",\"target\":\"修订后\"}]}\n```"
	revs, _, _, err := tryRepairReviseWithRuby(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Errorf("wrong: %#v", revs)
	}
}

func TestTryRepairReviseWithRuby_FatalNotJSON(t *testing.T) {
	_, _, _, err := tryRepairReviseWithRuby("totally not json", allOpts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("wrong err: %v", err)
	}
}

func TestParseReviseByMode_NonTextUsesJSONRepair(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后",}]}`
	revs, rubyOutput, repaired, err := ParseReviseByMode(in, false, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || len(repaired) == 0 {
		t.Fatalf("revs=%#v repaired=%v", revs, repaired)
	}
	if rubyOutput != nil {
		t.Errorf("rubyOutput should be nil when absent: %#v", rubyOutput)
	}
}

func TestParseReviseByMode_TextRevision(t *testing.T) {
	in := "[revisions]\n1 | 修订后 | 含竖线\n"
	revs, rubyOutput, repaired, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].Target != "修订后 | 含竖线" {
		t.Errorf("wrong: %#v", revs)
	}
	if len(repaired) != 0 {
		t.Errorf("text path should not report repair ops: %v", repaired)
	}
	if rubyOutput != nil {
		t.Errorf("rubyOutput should be nil when [ruby] absent: %#v", rubyOutput)
	}
}

func TestParseReviseByMode_TextJSONFallsBack(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后"}]}`
	revs, rubyOutput, _, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Errorf("wrong: %#v", revs)
	}
	if rubyOutput != nil {
		t.Errorf("rubyOutput should be nil when absent: %#v", rubyOutput)
	}
}

func TestParseReviseByMode_JSONWithRubyOutput(t *testing.T) {
	in := `{"revisions":[{"id":"1","target":"修订后"}],"ruby_output":{"1":[{"base":"呪","text":"じゅ","kind":"phonetic","id":"1"}]}}`
	revs, rubyOutput, _, err := ParseReviseByMode(in, false, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" || revs[0].Target != "修订后" {
		t.Fatalf("revs=%#v", revs)
	}
	if len(rubyOutput) != 1 {
		t.Fatalf("rubyOutput=%#v", rubyOutput)
	}
	entries := rubyOutput["1"]
	if len(entries) != 1 {
		t.Fatalf("entries=%#v", entries)
	}
	e := entries[0]
	if e.Base != "呪" || e.Text != "じゅ" || e.Kind != "phonetic" || e.ID != "1" {
		t.Errorf("entry mismatch: %#v", e)
	}
}

func TestParseReviseByMode_TextWithRubySection(t *testing.T) {
	in := "[revisions]\n1 | 修订後\n\n[ruby]\n1: 呪 | じゅ | phonetic | 1\n2: 微笑 | ほほえ | semantic\n"
	revs, rubyOutput, repaired, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(repaired) != 0 {
		t.Errorf("text path should not report repair ops: %v", repaired)
	}
	if len(revs) != 1 || revs[0].ID != "1" || revs[0].Target != "修订後" {
		t.Fatalf("revs=%#v", revs)
	}
	if len(rubyOutput) != 2 {
		t.Fatalf("rubyOutput=%#v", rubyOutput)
	}
	e1 := rubyOutput["1"]
	if len(e1) != 1 || e1[0].Base != "呪" || e1[0].Text != "じゅ" || e1[0].Kind != "phonetic" || e1[0].ID != "1" {
		t.Errorf("rubyOutput[1]=%#v", e1)
	}
	e2 := rubyOutput["2"]
	if len(e2) != 1 || e2[0].Base != "微笑" || e2[0].Text != "ほほえ" || e2[0].Kind != "semantic" || e2[0].ID != "" {
		t.Errorf("rubyOutput[2]=%#v", e2)
	}
}

func TestParseReviseByMode_TextWithoutRubySection(t *testing.T) {
	in := "[revisions]\n1 | 修订后\n"
	revs, rubyOutput, _, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" {
		t.Fatalf("wrong: %#v", revs)
	}
	if rubyOutput != nil {
		t.Errorf("rubyOutput should be nil when [ruby] absent: %#v", rubyOutput)
	}
}

// TestParseReviseByMode_TextFencedWithRubySection 验证整响应被 ``` 围栏包裹时
// [revisions] 与 [ruby] 的扫描看到同一（剥围栏后）文本形态——collectSectionLines
// 与 ParseReviseTextRevisions 的围栏预处理保持一致。
func TestParseReviseByMode_TextFencedWithRubySection(t *testing.T) {
	in := "```\n[revisions]\n1 | 修订後\n\n[ruby]\n1: 呪 | じゅ | phonetic | 1\n```"
	revs, rubyOutput, _, err := ParseReviseByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(revs) != 1 || revs[0].ID != "1" || revs[0].Target != "修订後" {
		t.Fatalf("wrong: %#v", revs)
	}
	if len(rubyOutput) != 1 {
		t.Fatalf("rubyOutput=%#v want one segment", rubyOutput)
	}
	entries := rubyOutput["1"]
	if len(entries) != 1 || entries[0].Base != "呪" || entries[0].Text != "じゅ" ||
		entries[0].Kind != "phonetic" || entries[0].ID != "1" {
		t.Fatalf("rubyOutput[1]=%#v", entries)
	}
}
