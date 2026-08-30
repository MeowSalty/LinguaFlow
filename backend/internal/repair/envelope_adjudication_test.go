package repair

import (
	"strings"
	"testing"
)

func TestTryRepairAdjudication_HappyPath(t *testing.T) {
	in := `{"verdicts":[{"id":"3","issue_code":"source_residual","matched_text":"test","verdict":"real","reason":"残留"}]}`
	verdicts, repaired, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "3" || verdicts[0].Verdict != "real" {
		t.Errorf("wrong: %#v", verdicts)
	}
	if len(repaired) != 0 {
		t.Errorf("unexpected repair: %v", repaired)
	}
}

// TestTryRepairAdjudication_EmptyTruncated 截断型：括号未闭合。
func TestTryRepairAdjudication_EmptyTruncated(t *testing.T) {
	in := `{"verdicts":{ "verdicts": []}`
	verdicts, repaired, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(verdicts) != 0 {
		t.Errorf("expected empty, got %#v", verdicts)
	}
	if !contains(repaired, "json.robust-extract") && !contains(repaired, "json.close-braces") {
		t.Errorf("expected a structural repair op, got %v", repaired)
	}
}

// TestTryRepairAdjudication_StutteredPrefix 结巴/重复前缀。
func TestTryRepairAdjudication_StutteredPrefix(t *testing.T) {
	in := `{"verdicts{"verdicts":[{"id":"3","issue_code":"source_residual","matched_text":"t","verdict":"false_positive","reason":"误报"}]}`
	verdicts, repaired, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "3" || verdicts[0].Verdict != "false_positive" {
		t.Errorf("wrong: %#v", verdicts)
	}
	if !contains(repaired, "json.robust-extract") {
		t.Errorf("expected json.robust-extract, got %v", repaired)
	}
}

// TestTryRepairAdjudication_TruncatedWithContent 截断含内容（完整值边界截断：
// 值闭引号后直接截断，缺收尾 }]）。adjudication 入口经 WithoutSalvage 弃用截断
// 抢救（无「缺失 verdict → 重跑」通道，partial 会被计为终态成功），fail-closed
// 对所有截断形态成立——close-braces/robust-extract 的截断残尾修补一并弃用，
// 必须报错走 unresolved → 下一池整批重试。非截断噪声的恢复见 StutteredPrefix。
func TestTryRepairAdjudication_TruncatedWithContent(t *testing.T) {
	in := `{"verdicts":[{"id":"3","issue_code":"source_residual","matched_text":"test","verdict":"real","reason":"残留"`
	verdicts, _, err := TryRepairAdjudication(in, allOpts)
	if err == nil {
		t.Fatalf("expected error: adjudication must decline boundary truncation, got %#v", verdicts)
	}
}

func TestTryRepairAdjudication_TrailingComma(t *testing.T) {
	in := `{"verdicts":[{"id":"1","issue_code":"source_residual","matched_text":"t","verdict":"real","reason":"x",}]}`
	verdicts, repaired, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "1" {
		t.Errorf("wrong: %#v", verdicts)
	}
	if !contains(repaired, "json.trailing-comma") {
		t.Errorf("expected json.trailing-comma, got %v", repaired)
	}
}

func TestTryRepairAdjudication_CodeFence(t *testing.T) {
	in := "```json\n{\"verdicts\":[{\"id\":\"1\",\"issue_code\":\"source_residual\",\"matched_text\":\"t\",\"verdict\":\"real\",\"reason\":\"x\"}]}\n```"
	verdicts, _, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "1" {
		t.Errorf("wrong: %#v", verdicts)
	}
}

func TestTryRepairAdjudication_FiltersMissingID(t *testing.T) {
	in := `{"verdicts":[{"id":"","issue_code":"source_residual","matched_text":"t","verdict":"real","reason":"x"},{"id":"2","issue_code":"source_residual","matched_text":"t","verdict":"real","reason":"y"}]}`
	verdicts, _, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "2" {
		t.Errorf("wrong: %#v", verdicts)
	}
}

func TestTryRepairAdjudication_FatalNotJSON(t *testing.T) {
	_, _, err := TryRepairAdjudication("totally not json", allOpts)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no JSON object") {
		t.Errorf("wrong err: %v", err)
	}
}

func TestParseAdjudicationByMode_NonTextUsesJSONRepair(t *testing.T) {
	in := `{"verdicts":{ "verdicts": []}`
	verdicts, repaired, err := ParseAdjudicationByMode(in, false, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(verdicts) != 0 {
		t.Errorf("expected empty, got %#v", verdicts)
	}
	if len(repaired) == 0 {
		t.Errorf("expected repair ops for truncated input")
	}
}

func TestParseAdjudicationByMode_TextVerdicts(t *testing.T) {
	in := "[verdicts]\n3 | source_residual | test | real | 残留\n"
	verdicts, _, err := ParseAdjudicationByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "3" {
		t.Errorf("wrong: %#v", verdicts)
	}
}

func TestParseAdjudicationByMode_TextEmptyFallsBackJSON(t *testing.T) {
	in := `{"verdicts":{ "verdicts": []}`
	verdicts, _, err := ParseAdjudicationByMode(in, true, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(verdicts) != 0 {
		t.Errorf("expected empty, got %#v", verdicts)
	}
}

// TestTryRepairAdjudication_BareArray 裸数组兜底：LLM 丢掉外层信封直接返回
// verdicts 条目数组，判别器（合法 verdict 值）放行后包装采纳。
func TestTryRepairAdjudication_BareArray(t *testing.T) {
	in := `[{"id":"3","issue_code":"source_residual","matched_text":"test","verdict":"real","reason":"残留"}]`
	verdicts, repaired, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(verdicts) != 1 || verdicts[0].ID != "3" || verdicts[0].Verdict != "real" {
		t.Errorf("wrong: %#v", verdicts)
	}
	if !contains(repaired, "json.bare-array") {
		t.Errorf("expected json.bare-array, got %v", repaired)
	}
}

// TestTryRepairAdjudication_BareArrayEchoRejected 回显诱饵拒绝：issue 清单
// 回显（无 verdict 字段）不被裸数组兜底采纳，报错走重试。
func TestTryRepairAdjudication_BareArrayEchoRejected(t *testing.T) {
	in := `[{"id":"3","issue_code":"source_residual","snippet":"test"}]`
	_, repaired, err := TryRepairAdjudication(in, allOpts)
	if err == nil {
		t.Fatal("expected error for echoed issue list")
	}
	if contains(repaired, "json.bare-array") {
		t.Errorf("echoed list should not be adopted: %v", repaired)
	}
}
