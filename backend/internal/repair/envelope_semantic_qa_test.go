package repair

import (
	"strings"
	"testing"
)

func TestTryRepairSemanticQA_HappyPath(t *testing.T) {
	in := `{"issues":[{"id":"3","code":"term_fidelity","message":"音译不准确","snippet":"ル"}]}`
	issues, repaired, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(issues) != 1 || issues[0].ID != "3" || issues[0].Code != "term_fidelity" {
		t.Errorf("wrong: %#v", issues)
	}
	if len(repaired) != 0 {
		t.Errorf("unexpected repair: %v", repaired)
	}
}

// TestTryRepairSemanticQA_EmptyIssues 复现日志中的截断型故障：
//
//	{"issues":{ "issues": []}
//
// 旧 jsonObjectSlice 因括号不平衡（2 个 '{' 只有 1 个 '}'）返回 "" → fatal。
// 修复链应补齐括号后正确解析为空 issues 列表。
func TestTryRepairSemanticQA_EmptyIssues(t *testing.T) {
	in := `{"issues":{ "issues": []}`
	issues, repaired, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(issues) != 0 {
		t.Errorf("expected empty, got %#v", issues)
	}
	if !contains(repaired, "json.robust-extract") && !contains(repaired, "json.close-braces") {
		t.Errorf("expected a structural repair op, got %v", repaired)
	}
}

// TestTryRepairSemanticQA_StutteredPrefix 复现日志中的结巴/重复前缀故障：
//
//	{"issues{"issues":[{...}]}
//
// 第一个对象缺 ':'，matchBracePair 失步；修复链应从第二个 '{' 偏移用 Decoder 真实
// 解析，恢复内层合法数组 envelope。
func TestTryRepairSemanticQA_StutteredPrefix(t *testing.T) {
	in := `{"issues{"issues":[{"code":"term_fidelity","id":"3","message":"音译错误","snippet":"ル"}]}`
	issues, repaired, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(issues) != 1 || issues[0].ID != "3" {
		t.Errorf("wrong: %#v", issues)
	}
	if !contains(repaired, "json.robust-extract") {
		t.Errorf("expected json.robust-extract, got %v", repaired)
	}
}

// TestTryRepairSemanticQA_TruncatedWithContent 复现日志中的截断型故障（含内容）：
//
//	{"issues":[{"code":"term_fidelity","id":"3","message":"...","snippet":"ル…
//
// 被截断未闭合 → 补齐括号后解析。
func TestTryRepairSemanticQA_TruncatedWithContent(t *testing.T) {
	in := `{"issues":[{"code":"term_fidelity","id":"3","message":"音译不准确","snippet":"ルトルバーグ"`
	issues, repaired, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(issues) != 1 || issues[0].ID != "3" || issues[0].Snippet != "ルトルバーグ" {
		t.Errorf("wrong: %#v", issues)
	}
}

func TestTryRepairSemanticQA_TrailingComma(t *testing.T) {
	in := `{"issues":[{"id":"1","code":"term_fidelity","message":"x","snippet":"y",}]}`
	issues, repaired, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Errorf("wrong: %#v", issues)
	}
	if !contains(repaired, "json.trailing-comma") {
		t.Errorf("expected json.trailing-comma, got %v", repaired)
	}
}

func TestTryRepairSemanticQA_CodeFence(t *testing.T) {
	in := "```json\n{\"issues\":[{\"id\":\"1\",\"code\":\"term_fidelity\",\"message\":\"x\",\"snippet\":\"y\"}]}\n```"
	issues, _, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Errorf("wrong: %#v", issues)
	}
}

func TestTryRepairSemanticQA_FiltersIllegalCode(t *testing.T) {
	// length_ratio 不是语义质检 code，应被过滤掉。
	in := `{"issues":[{"id":"1","code":"term_fidelity","message":"a","snippet":"b"},{"id":"2","code":"length_ratio","message":"c","snippet":"d"}]}`
	issues, _, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Errorf("wrong: %#v", issues)
	}
}

func TestTryRepairSemanticQA_FatalNotJSON(t *testing.T) {
	_, _, err := TryRepairSemanticQA("totally not json", allOpts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("wrong err: %v", err)
	}
}

func TestParseSemanticQAByMode_NonTextUsesJSONRepair(t *testing.T) {
	in := `{"issues":{ "issues": []}`
	issues, repaired, err := ParseSemanticQAByMode(in, false, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected empty, got %#v", issues)
	}
	if len(repaired) == 0 {
		t.Errorf("expected repair ops for truncated input")
	}
}

func TestParseSemanticQAByMode_TextEmptyFallsBackJSON(t *testing.T) {
	// text 模式但内容实为 JSON 截断 → fallback JSON 修复路径。
	in := `{"issues":{ "issues": []}`
	issues, _, err := ParseSemanticQAByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected empty, got %#v", issues)
	}
}

func TestParseSemanticQAByMode_TextIssues(t *testing.T) {
	in := "[issues]\n1 | term_fidelity | ル | 音译错误\n"
	issues, _, err := ParseSemanticQAByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(issues) != 1 || issues[0].ID != "1" {
		t.Errorf("wrong: %#v", issues)
	}
}

// 确保 issues 零值 Options 路径行为与基础抽取一致（happy path 通过）。
func TestTryRepairSemanticQA_ZeroOpts(t *testing.T) {
	in := `{"issues":[{"id":"1","code":"term_fidelity","message":"x","snippet":"y"}]}`
	issues, _, err := TryRepairSemanticQA(in, Options{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("wrong: %#v", issues)
	}
}
