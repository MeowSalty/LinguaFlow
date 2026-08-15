package repair

import (
	"encoding/json"
	"testing"
)

func TestEscapeUnescapedQuotes_ValueInnerQuotes(t *testing.T) {
	in := `{"verdicts":[{"id":"1","reason":""英国"是中文标准用词。"}]}`
	got := escapeUnescapedQuotes(in)
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("repaired JSON still invalid: %v\n%s", err, got)
	}
	verdicts := m["verdicts"].([]any)
	reason := verdicts[0].(map[string]any)["reason"].(string)
	if reason != `"英国"是中文标准用词。` {
		t.Errorf("reason = %q", reason)
	}
}

func TestEscapeUnescapedQuotes_NopOnValidJSON(t *testing.T) {
	in := `{"a":"x\"y","b":"plain","c":["1","2"],"d":{"e":"f"},"g":"h"}`
	if got := escapeUnescapedQuotes(in); got != in {
		t.Errorf("valid JSON modified:\n in: %s\ngot: %s", in, got)
	}
}

func TestEscapeUnescapedQuotes_KeyValueBoundary(t *testing.T) {
	// key 结束的 " 后跟 ':'，value 结束的 " 后跟 ',' 或 '}'，均不应被转义。
	in := `{"key":"value","list":["a","b"]}`
	if got := escapeUnescapedQuotes(in); got != in {
		t.Errorf("boundary JSON modified:\n in: %s\ngot: %s", in, got)
	}
}

func TestEscapeUnescapedQuotes_TrailingValueAtEOF(t *testing.T) {
	// 字符串在文本末尾终止（截断场景）：视为合法终止，不转义。
	in := `{"a":"b"`
	if got := escapeUnescapedQuotes(in); got != in {
		t.Errorf("EOF JSON modified:\n in: %s\ngot: %s", in, got)
	}
}

func TestEscapeUnescapedQuotes_WhitespaceBeforeComma(t *testing.T) {
	// 合法终止符前允许空白：","..."  " } 等均不应转义。
	in := "{\n  \"a\" : \"value\" ,\n  \"b\" : [ \"x\" , \"y\" ]\n}"
	if got := escapeUnescapedQuotes(in); got != in {
		t.Errorf("whitespace JSON modified:\n in: %s\ngot: %s", in, got)
	}

	// 值内引号后跟空白再跟普通字符：应转义。
	in2 := `{"a":"say " hi"}`
	got2 := escapeUnescapedQuotes(in2)
	var m map[string]any
	if err := json.Unmarshal([]byte(got2), &m); err != nil {
		t.Fatalf("repaired JSON invalid: %v\n%s", err, got2)
	}
	if m["a"] != `say " hi` {
		t.Errorf("a = %q", m["a"])
	}
}

func TestEscapeUnescapedQuotes_AlreadyEscapedUntouched(t *testing.T) {
	// \" 是已转义引号，不能二次转义成 \\"（会引入字面反斜杠）。
	in := `{"a":"x \"quoted\" y"}`
	if got := escapeUnescapedQuotes(in); got != in {
		t.Errorf("escaped JSON modified:\n in: %s\ngot: %s", in, got)
	}
}

// TestTryRepairAdjudication_UnescapedQuotesInReason 复现生产 parse_error：
// LLM 在 reason 值内用未转义引号包裹术语（"英国" 等），原修复链无法处理。
func TestTryRepairAdjudication_UnescapedQuotesInReason(t *testing.T) {
	in := `{"verdicts":[{"id":"241","issue_code":"source_residual","matched_text":"英国","verdict":"false_positive","reason":""英国"是中文标准用词，与日语同形，属合理使用。"},{"id":"245","issue_code":"length_ratio","matched_text":"","verdict":"false_positive","reason":"译文因补充修饰语略长，属合理扩展，无漏译或填充。"}]}`
	verdicts, repaired, err := TryRepairAdjudication(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(verdicts) != 2 {
		t.Fatalf("want 2 verdicts, got %d: %#v", len(verdicts), verdicts)
	}
	if verdicts[0].ID != "241" || verdicts[0].MatchedText != "英国" {
		t.Errorf("verdicts[0] = %#v", verdicts[0])
	}
	if want := `"英国"是中文标准用词，与日语同形，属合理使用。`; verdicts[0].Reason != want {
		t.Errorf("reason = %q, want %q", verdicts[0].Reason, want)
	}
	if verdicts[1].ID != "245" {
		t.Errorf("verdicts[1] = %#v", verdicts[1])
	}
	if !contains(repaired, "json.escape-quotes") {
		t.Errorf("expected json.escape-quotes op, got %v", repaired)
	}
}

// TestTryRepairSemanticQA_UnescapedQuotesInMessage 同形态在 issues envelope 上的复现。
func TestTryRepairSemanticQA_UnescapedQuotesInMessage(t *testing.T) {
	in := `{"issues":[{"id":"10","code":"mistranslation","message":""少女"是中文标准词汇，非残留","snippet":"少女"}]}`
	issues, repaired, err := TryRepairSemanticQA(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(issues) != 1 || issues[0].ID != "10" {
		t.Fatalf("issues = %#v", issues)
	}
	if !contains(repaired, "json.escape-quotes") {
		t.Errorf("expected json.escape-quotes op, got %v", repaired)
	}
}

// TestTryRepair_UnescapedQuotesInTranslation translate envelope 同形态复现。
func TestTryRepair_UnescapedQuotesInTranslation(t *testing.T) {
	in := `{"translations":{"1":"他称之为"魔法"力量","2":"正常译文"}}`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal || r.ParseErr != nil {
		t.Fatalf("fatal=%v err=%v repaired=%v", r.Fatal, r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != `他称之为"魔法"力量` {
		t.Errorf("trans[1] = %q", r.Trans["1"])
	}
	if !contains(r.Repaired, "json.escape-quotes") {
		t.Errorf("expected json.escape-quotes op, got %v", r.Repaired)
	}
}
