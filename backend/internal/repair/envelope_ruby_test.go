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

// TestTryRepairRubyAlignment_PreservesID 修复链保持 OutputEntry.ID 透传。
func TestTryRepairRubyAlignment_PreservesID(t *testing.T) {
	in := `{"ruby_output":[{"id":"1","base":"I","text":"aɪ","kind":"phonetic"}]}`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %#v", entries)
	}
	if entries[0].ID != "1" {
		t.Errorf("expected id \"1\", got %q", entries[0].ID)
	}
	if entries[0].Base != "I" || entries[0].Text != "aɪ" || entries[0].Kind != "phonetic" {
		t.Errorf("wrong entry: %#v", entries[0])
	}
}

// TestTryRepairRubyAlignment_BareArray LLM 丢掉外层信封直接返回条目数组
// （线上 empty_output 实例：字段齐全、id 可合并，仅缺 {"ruby_output": ...} 包装）。
func TestTryRepairRubyAlignment_BareArray(t *testing.T) {
	in := `[
  {
    "id": "1",
    "base": "白银之风",
    "text": "アルジェント・ビアブレッザ",
    "kind": "phonetic"
  }
]`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %#v", entries)
	}
	if entries[0].ID != "1" || entries[0].Base != "白银之风" ||
		entries[0].Text != "アルジェント・ビアブレッザ" || entries[0].Kind != "phonetic" {
		t.Errorf("wrong entry: %#v", entries[0])
	}
	if !contains(repaired, "json.bare-array") {
		t.Errorf("expected json.bare-array, got %v", repaired)
	}
}

// TestTryRepairRubyAlignment_BareArrayTruncated 裸数组 + 截断（尾部括号缺失）。
func TestTryRepairRubyAlignment_BareArrayTruncated(t *testing.T) {
	in := `[{"id":"1","base":"白银之风","text":"アルジェント・ビアブレッザ","kind":"phonetic"`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Base != "白银之风" || entries[0].ID != "1" {
		t.Errorf("wrong: %#v", entries)
	}
	if !contains(repaired, "json.bare-array") {
		t.Errorf("expected json.bare-array, got %v", repaired)
	}
}

// TestTryRepairRubyAlignment_BareArrayRequiresOpts 零值 Options 不做裸数组兜底
// （兜底属 L1 结构修复，与主链路其余算子开关语义一致）。
func TestTryRepairRubyAlignment_BareArrayRequiresOpts(t *testing.T) {
	in := `[{"id":"1","base":"白银之风","text":"アルジェント・ビアブレッザ","kind":"phonetic"}]`
	_, _, err := TryRepairRubyAlignment(in, Options{})
	if err == nil {
		t.Fatal("expected error with zero Options")
	}
}

// TestTryRepairRubyAlignment_BareArrayEmptyRejected 空数组不采纳：噪声中游离的
// [] 不应把解析失败翻转为"成功零条目"。
func TestTryRepairRubyAlignment_BareArrayEmptyRejected(t *testing.T) {
	in := `示例：[]`
	if _, _, err := TryRepairRubyAlignment(in, allOpts); err == nil {
		t.Fatal("expected error for bare empty array")
	}
}

// TestTryRepairRubyAlignment_BareArrayScalarElementsRejected 元素非对象的数组
// （如字符串数组）不采纳，继续扫描下一候选。
func TestTryRepairRubyAlignment_BareArrayScalarElementsRejected(t *testing.T) {
	in := `["噪声", "示例"]`
	if _, _, err := TryRepairRubyAlignment(in, allOpts); err == nil {
		t.Fatal("expected error for scalar-element array")
	}
}

// TestTryRepairRubyAlignment_BareArrayWrongShapeRejected 元素为对象但缺目标
// 必需字段（base 为空）：形状门控放行后由 normalize 判定误采纳，报错重试而非
// 静默返回空结果。
func TestTryRepairRubyAlignment_BareArrayWrongShapeRejected(t *testing.T) {
	in := `[{"id":"1","kind":"phonetic"}]`
	_, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err == nil {
		t.Fatal("expected error for wrong-shape bare array")
	}
	if !contains(repaired, "json.bare-array") {
		t.Errorf("expected json.bare-array in repaired chain, got %v", repaired)
	}
}

// TestTryRepairRubyAlignment_BareArrayEchoRejected 回显诱饵拒绝：定向重试
// prompt 会下发 {id, source_base, source_text} 清单，模型回显该清单时条目仅有
// base 类字段、无 text/合法 kind，领域判别器在采纳前拒绝（继续扫描后落回错误
// 路径），修复链不含 json.bare-array。
func TestTryRepairRubyAlignment_BareArrayEchoRejected(t *testing.T) {
	in := `[{"id":"1","source_base":"白银之风","source_text":"アルジェント・ビアブレッザ"}]`
	_, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err == nil {
		t.Fatal("expected error for echoed item list")
	}
	if contains(repaired, "json.bare-array") {
		t.Errorf("echoed list should not be adopted: %v", repaired)
	}
}

// TestTryRepairRubyAlignment_LegalEmptyEnvelope 合法空信封 {"ruby_output":[]}
// 正常走主链路返回零条目，不受裸数组门控影响。
func TestTryRepairRubyAlignment_LegalEmptyEnvelope(t *testing.T) {
	entries, repaired, err := TryRepairRubyAlignment(`{"ruby_output":[]}`, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %#v", entries)
	}
	if contains(repaired, "json.bare-array") {
		t.Errorf("unexpected json.bare-array in repaired chain: %v", repaired)
	}
}
