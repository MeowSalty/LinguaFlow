package repair

import (
	"reflect"
	"testing"
)

// ---- truncateToLastCompleteValue 单元 ----

func TestTruncateToLastCompleteValue_ValueMidCut(t *testing.T) {
	// 实录形态：值中途截断（未闭合引号）。最后一个完整值闭合点在 "完整乙" 的
	// 闭合引号处，其后残尾整体丢弃。
	in := `{"translations":{"1":"完整甲","2":"完整乙","3":"瑞贝卡学姐一问`
	cut, ok := truncateToLastCompleteValue(in)
	if !ok {
		t.Fatal("expected salvage cut")
	}
	want := `{"translations":{"1":"完整甲","2":"完整乙"`
	if cut != want {
		t.Errorf("cut = %q, want %q", cut, want)
	}
}

func TestTruncateToLastCompleteValue_DanglingKeyColon(t *testing.T) {
	// 实录形态：键冒号悬空。键引号后跟 ':' 不构成值闭合点，`"3":` 整段落入残尾。
	in := `{"translations":{"1":"完整甲","2":"完整乙","3":`
	cut, ok := truncateToLastCompleteValue(in)
	if !ok {
		t.Fatal("expected salvage cut")
	}
	want := `{"translations":{"1":"完整甲","2":"完整乙"`
	if cut != want {
		t.Errorf("cut = %q, want %q", cut, want)
	}
}

func TestTruncateToLastCompleteValue_KeyMidCut(t *testing.T) {
	// 实录形态：键中途截断。`"3` 未闭合引号不算闭合点，键引号后跟 ':' 的假边界
	// 被排除，回退到 "完整乙" 的闭合引号。
	in := `{"translations":{"1":"完整甲","2":"完整乙","3`
	cut, ok := truncateToLastCompleteValue(in)
	if !ok {
		t.Fatal("expected salvage cut")
	}
	want := `{"translations":{"1":"完整甲","2":"完整乙"`
	if cut != want {
		t.Errorf("cut = %q, want %q", cut, want)
	}
}

func TestTruncateToLastCompleteValue_NoCompleteValue(t *testing.T) {
	// 无任何值闭合点（键引号后跟 ':' 不算）：返回原文 + false。
	in := `{"translations":{"1":"hello`
	if cut, ok := truncateToLastCompleteValue(in); ok || cut != in {
		t.Errorf("expected (original, false), got (%q, %v)", cut, ok)
	}
}

func TestTruncateToLastCompleteValue_CutAtEndIsNoop(t *testing.T) {
	// 闭合点恰在文本末尾（根对象已闭合）：无需抢救，返回原文 + false。
	in := `{"translations":{"1":"甲"}`
	if cut, ok := truncateToLastCompleteValue(in); ok || cut != in {
		t.Errorf("expected (original, false), got (%q, %v)", cut, ok)
	}
}

func TestTruncateToLastCompleteValue_ContainerClosure(t *testing.T) {
	// '}' / ']' 容器闭合也是值闭合点：回退到 glossary 首条目的 '}'。
	in := `{"translations":{"1":"甲"},"glossary":[{"source":"a","target":"b"},{"source":"c`
	cut, ok := truncateToLastCompleteValue(in)
	if !ok {
		t.Fatal("expected salvage cut")
	}
	want := `{"translations":{"1":"甲"},"glossary":[{"source":"a","target":"b"}`
	if cut != want {
		t.Errorf("cut = %q, want %q", cut, want)
	}
}

// ---- TryRepair 截断抢救：四条实录形态 ----

// TestTryRepair_TruncationSalvage_ValueMidCut 实录形态：值中途截断。此前整批
// Fatal 丢弃全部前缀；现在保住 {1,2}，缺失的 3 走 Missing/重试通道。
func TestTryRepair_TruncationSalvage_ValueMidCut(t *testing.T) {
	in := `{"translations":{"1":"完整甲","2":"完整乙","3":"瑞贝卡学姐一问`
	r := TryRepair(in, []string{"1", "2", "3"}, allOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "完整甲" || r.Trans["2"] != "完整乙" || len(r.Trans) != 2 {
		t.Errorf("prefix must survive, tail must drop: %#v", r.Trans)
	}
	if !reflect.DeepEqual(sortedStrings(r.Missing), []string{"3"}) {
		t.Errorf("missing mismatch: %v", r.Missing)
	}
	if !contains(r.Repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", r.Repaired)
	}
}

// TestTryRepair_TruncationSalvage_DanglingKeyColon 实录形态：键冒号悬空。
func TestTryRepair_TruncationSalvage_DanglingKeyColon(t *testing.T) {
	in := `{"translations":{"1":"完整甲","2":"完整乙","3":`
	r := TryRepair(in, []string{"1", "2", "3"}, allOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "完整甲" || r.Trans["2"] != "完整乙" || len(r.Trans) != 2 {
		t.Errorf("prefix must survive, tail must drop: %#v", r.Trans)
	}
	if !reflect.DeepEqual(sortedStrings(r.Missing), []string{"3"}) {
		t.Errorf("missing mismatch: %v", r.Missing)
	}
	if !contains(r.Repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", r.Repaired)
	}
}

// TestTryRepair_TruncationSalvage_KeyMidCut 实录形态：键中途截断。
func TestTryRepair_TruncationSalvage_KeyMidCut(t *testing.T) {
	in := `{"translations":{"1":"完整甲","2":"完整乙","3`
	r := TryRepair(in, []string{"1", "2", "3"}, allOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "完整甲" || r.Trans["2"] != "完整乙" || len(r.Trans) != 2 {
		t.Errorf("prefix must survive, tail must drop: %#v", r.Trans)
	}
	if !reflect.DeepEqual(sortedStrings(r.Missing), []string{"3"}) {
		t.Errorf("missing mismatch: %v", r.Missing)
	}
	if !contains(r.Repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", r.Repaired)
	}
}

// TestTryRepair_TruncationSalvage_TrailingCommaHandledByCloseBraces 值后边界
// （尾逗号）截断：既有 close-braces + trailing-comma 已能救回，不进 salvage。
func TestTryRepair_TruncationSalvage_TrailingCommaHandledByCloseBraces(t *testing.T) {
	in := `{"translations":{"1":"完整甲","2":"完整乙",`
	r := TryRepair(in, []string{"1", "2", "3"}, allOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "完整甲" || r.Trans["2"] != "完整乙" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if !contains(r.Repaired, "json.close-braces") {
		t.Errorf("expected json.close-braces in %v", r.Repaired)
	}
	if contains(r.Repaired, "json.truncation-salvage") {
		t.Errorf("close-braces should handle this form, salvage must not fire: %v", r.Repaired)
	}
}

// ---- TryRepair 截断抢救：容器闭合点 / 拒绝路径 / 门控 ----

// TestTryRepair_TruncationSalvage_ContainerClosure '}'/']' 闭合点：translations
// 完整保留，glossary 救回第一条，残尾第二条丢弃。
func TestTryRepair_TruncationSalvage_ContainerClosure(t *testing.T) {
	in := `{"translations":{"1":"甲"},"glossary":[{"source":"a","target":"b"},{"source":"c`
	r := TryRepair(in, []string{"1"}, allOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "甲" {
		t.Errorf("translations lost: %#v", r.Trans)
	}
	if len(r.Glos) != 1 || r.Glos[0].Source != "a" || r.Glos[0].Target != "b" {
		t.Errorf("glossary salvage wrong: %#v", r.Glos)
	}
	if !contains(r.Repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", r.Repaired)
	}
}

// TestTryRepair_TruncationSalvage_RejectsGarbageAfterQuote 无合法值闭合点
// （引号闭合后跟垃圾、无 ','/'}'/']'/EOF 后继）：不制造假边界，仍 Fatal。
func TestTryRepair_TruncationSalvage_RejectsGarbageAfterQuote(t *testing.T) {
	in := `{"translations":{"1":"甲"垃圾"2":"乙`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if !r.Fatal {
		t.Fatalf("expected fatal (no legal value closure), got %#v (repaired=%v)", r.Trans, r.Repaired)
	}
}

// TestTryRepair_TruncationSalvage_Declined WithoutSalvage 显式弃用：同形态仍
// Fatal（fail-closed 调用方自选，见 TryRepairSemanticQA / glossary prune）。
func TestTryRepair_TruncationSalvage_Declined(t *testing.T) {
	in := `{"translations":{"1":"完整甲","2":"完整乙","3":"瑞贝卡学姐一问`
	r := TryRepair(in, []string{"1", "2", "3"}, allOpts.WithoutSalvage())
	if !r.Fatal {
		t.Fatalf("expected fatal with salvage declined, got %#v (repaired=%v)", r.Trans, r.Repaired)
	}
}

// TestTryRepair_TruncationSalvage_RequiresJSONStructural 抢救挂在 JSONStructural
// 门控下（与 close-braces/robust-extract 同口径）：关闭时不抢救。
func TestTryRepair_TruncationSalvage_RequiresJSONStructural(t *testing.T) {
	in := `{"translations":{"1":"完整甲","2":"完整乙","3":"瑞贝卡学姐一问`
	r := TryRepair(in, []string{"1", "2", "3"}, Options{})
	if !r.Fatal {
		t.Fatalf("expected fatal without JSONStructural, got %#v", r.Trans)
	}
}

// ---- envelope 系入口 ----

// TestTryRepairRubyAlignment_TruncationSalvage_KeyMidCut ruby 键中途截断：
// 前缀条目救回。截断点在数组内、第二个条目的键被截断（数组不闭合）：裸数组
// 兜底无法解码未闭合引号，由 '}' 容器闭合点回退 + 递归修复链补齐。
func TestTryRepairRubyAlignment_TruncationSalvage_KeyMidCut(t *testing.T) {
	in := `{"ruby_output":[{"base":"漢字","text":"かんじ","kind":"phonetic"}` + `,{"bas`
	entries, repaired, err := TryRepairRubyAlignment(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" || entries[0].Text != "かんじ" {
		t.Errorf("prefix entry lost: %#v", entries)
	}
	if !contains(repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", repaired)
	}
}

// TestTryRepairAdjudication_TruncationSalvageDeclined adjudication 入口固有弃用
// 截断抢救：成功路径对所有批次段一律 SegmentDone（无「缺失 verdict → 重跑」通道），
// 抢救出的 partial 会被计为终态已裁决、缺失段经 ResolvedIndices 永久跳过。
// 截断形态取数组内键中途（数组不闭合）：裸数组兜底同样无法解码，若无弃用本可
// 被 salvage 救回，弃用后必须仍报错（由 handler 走 unresolved → 下一池整批重试）。
func TestTryRepairAdjudication_TruncationSalvageDeclined(t *testing.T) {
	in := `{"verdicts":[{"id":"3","issue_code":"source_residual","matched_text":"t","verdict":"real","reason":"残留"}` + `,{"i`
	if _, _, err := TryRepairAdjudication(in, allOpts); err == nil {
		t.Fatal("expected error: adjudication must decline truncation salvage")
	}
}

// TestTryRepairSemanticQA_TruncationSalvageDeclined issues envelope 固有弃用
// 截断抢救：部分结果会被下游解释为「缺失段=已扫描无问题」（假阴性质检）。
// 截断形态取数组内键中途（数组不闭合）：裸数组兜底同样无法解码，若无弃用本可
// 被 salvage 救回，弃用后必须仍报错。
func TestTryRepairSemanticQA_TruncationSalvageDeclined(t *testing.T) {
	in := `{"issues":[{"id":"1","code":"term_fidelity","message":"x","snippet":"y"}` + `,{"i`
	if _, _, err := TryRepairSemanticQA(in, allOpts); err == nil {
		t.Fatal("expected error: semantic_qa must decline truncation salvage")
	}
}

// ---- bootstrap ----

// TestTryRepairBootstrap_TruncationSalvage_KeyMidCut bootstrap 键中途截断：
// 前缀条目救回（'}' 容器闭合点回退 + 递归 close-braces 补齐 ']}'）。
func TestTryRepairBootstrap_TruncationSalvage_KeyMidCut(t *testing.T) {
	in := `{"glossary":[{"source":"x","target":"y","notes":""},{"sour`
	entries, repaired, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Source != "x" || entries[0].Target != "y" {
		t.Errorf("prefix entries lost: %#v", entries)
	}
	if !contains(repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", repaired)
	}
}

// TestTryRepairBootstrap_TruncationSalvage_Declined prune 场景以 WithoutSalvage
// 调用（fail-closed）：截断缺口会被解释为「建议删除」，必须维持报错。
func TestTryRepairBootstrap_TruncationSalvage_Declined(t *testing.T) {
	in := `{"glossary":[{"source":"x","target":"y","notes":""},{"sour`
	if _, _, err := TryRepairBootstrap(in, allOpts.WithoutSalvage()); err == nil {
		t.Fatal("expected error with salvage declined")
	}
}

// TestTryRepairBootstrap_TruncationCloseBracesForm 完整值边界截断（值闭引号后
// 直接截断、括号不闭合）：close-braces 兜底补齐 ']}' 后前缀全数恢复。普通调用方
// （extract bootstrap）保持部分恢复语义。
func TestTryRepairBootstrap_TruncationCloseBracesForm(t *testing.T) {
	in := `{"glossary":[{"source":"x","target":"y"},{"source":"a","target":"b"`
	entries, repaired, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 2 || entries[0].Source != "x" || entries[1].Source != "a" {
		t.Errorf("prefix entries lost: %#v", entries)
	}
	if !contains(repaired, "json.close-braces") {
		t.Errorf("expected json.close-braces in %v", repaired)
	}
}

// TestTryRepairBootstrap_TruncationCloseBracesForm_Declined 同上完整值边界截断
// 形态，但以 WithoutSalvage 调用（glossary prune 场景）：close-braces 截断恢复随
// 抢救一并弃用——否则部分 refined 列表会被 computePruneDiff 解释为「未列出条目
// 建议删除」，fail-closed 必须对所有截断形态成立（本形态曾绕过 WithoutSalvage）。
func TestTryRepairBootstrap_TruncationCloseBracesForm_Declined(t *testing.T) {
	in := `{"glossary":[{"source":"x","target":"y"},{"source":"a","target":"b"`
	if _, _, err := TryRepairBootstrap(in, allOpts.WithoutSalvage()); err == nil {
		t.Fatal("expected error: close-braces truncation recovery must be declined with WithoutSalvage")
	}
}

// ---- envelope 系入口：完整值边界截断的门控矩阵 ----

// TestTryRepairEnvelope_BoundaryTruncation_Gating 完整值边界截断（值闭引号后直接
// 截断、缺收尾 }]）下 WithoutSalvage 的门控矩阵：弃用方（adjudication 经
// TryRepairAdjudication、semantic_qa 经 TryRepairSemanticQA）必须报错——
// close-braces 兜底、robust-extract 的截断修补、bare-array 的截断残尾包装
// 三条恢复路径全部随 salvageDeclined 关闭；未弃用方（ruby）仍可部分恢复。
// 该形态曾绕过 WithoutSalvage（close-braces/robust-extract 未门控时）。
func TestTryRepairEnvelope_BoundaryTruncation_Gating(t *testing.T) {
	verdictForm := `{"verdicts":[{"id":"3","issue_code":"source_residual","matched_text":"t","verdict":"real","reason":"残留"}`
	if _, _, err := TryRepairAdjudication(verdictForm, allOpts); err == nil {
		t.Error("adjudication: boundary truncation must error (declined salvage)")
	}

	issueForm := `{"issues":[{"id":"1","code":"term_fidelity","message":"x","snippet":"y"}`
	if _, _, err := TryRepairSemanticQA(issueForm, allOpts); err == nil {
		t.Error("semantic_qa: boundary truncation must error (declined salvage)")
	}

	rubyForm := `{"ruby_output":[{"base":"漢字","text":"かんじ","kind":"phonetic"}`
	entries, repaired, err := TryRepairRubyAlignment(rubyForm, allOpts)
	if err != nil {
		t.Fatalf("ruby (salvage enabled) should recover boundary truncation: %v", err)
	}
	if len(entries) != 1 || entries[0].Base != "漢字" {
		t.Errorf("ruby prefix entry lost: %#v", entries)
	}
	if !contains(repaired, "json.close-braces") && !contains(repaired, "json.robust-extract") && !contains(repaired, "json.bare-array") {
		t.Errorf("expected a structural recovery op for ruby, got %v", repaired)
	}

	// 同形态直调 TryRepairEnvelope（未弃用 Options）：close-braces 兜底恢复。
	raw, _, err := TryRepairEnvelope(rubyForm, "ruby_output", allOpts)
	if err != nil {
		t.Fatalf("TryRepairEnvelope with salvage enabled: %v", err)
	}
	if arr, ok := raw["ruby_output"].([]any); !ok || len(arr) != 1 {
		t.Errorf("envelope recovery wrong: %#v", raw)
	}
}

// TestTryRepairBootstrap_TruncationSalvage_BrokenBody 命中第二处抢救末环
// （body 已抽出但连同修复链仍无法解析）：值内非法转义（\q）+ 截断双故障形态。
// close-braces 兜底抽出的 body 含非法转义与裸词（escape-quotes 修复反而制造
// 未闭合字符串，L1 链失败）；截断前缀回退到第二个条目的 "x" 闭合点，经递归
// close-braces 补齐后可解析，救回首个完整条目。
func TestTryRepairBootstrap_TruncationSalvage_BrokenBody(t *testing.T) {
	in := `{"glossary":[{"source":"a","target":"b"},{"source":"x","target":"y\q" garbage`
	entries, repaired, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v (repaired=%v)", err, repaired)
	}
	if len(entries) != 1 || entries[0].Source != "a" || entries[0].Target != "b" {
		t.Errorf("salvaged entries wrong: %#v", entries)
	}
	if !contains(repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", repaired)
	}
}

// ---- 真实事故样本回归 ----

// TestTryRepair_TruncationSalvage_RealWorldSample 复现线上日志中的真实截断事故
// （Gemini 经中转返回 max_tokens 截断）：21 条完整译文 + 键 "11<下一键>" 中途截断。
// 旧行为为整批 Fatal 重跑（no JSON object found），21 条完整译文全部陪葬；
// 抢救后前缀全数保留，未及生成的 "119" 落入 Missing 走重跑通道。
func TestTryRepair_TruncationSalvage_RealWorldSample(t *testing.T) {
	in := `{
  "translations": {
    "10": "只要经过几分钟，暴食就会被赫冰封印给吞没。",
    "100": "那个夜晚，在她离去之际我听说了。",
    "101": "她说自己等了埃文·伯恩斯坦整整十年。",
    "102": "所以她才说希望把伯恩斯坦先生交给她来处理。",
    "103": "那个时候，她应该就已经做好了杀他的觉悟了吧。",
    "104": "“和埃文嘛，没错。我们曾是恋人。但是，我并不懂得什么是爱。因为渴望理解爱，我才和他成为了恋人。然后一直等待着消失的他。期待着如果能再见埃文一次，或许就能明白自己的这份感情了。”",
    "105": "她将手轻轻放在胸口。",
    "106": "“但是，埃文死后我明白了。我果然还是爱着他的啊。”",
    "107": "“是，这样吗……”",
    "108": "“真是讽刺啊。竟然要等到死后才像这样产生自觉。我真是个无可救药的人。”",
    "109": "用着如造物般漂亮的脸庞诉说着的她，看起来不知为何有些落寞。",
    "11": "虽然他似乎还在反抗，但魔术领域已经完全停止了。现在应该只是用最后残存的力量在抵抗吧。",
    "110": "“不过，埃文因为对我异常执着而变得不正常，那是他的软弱所致。”",
    "111": "“因为软弱，所以才追求了力量吗？”",
    "112": "“没错。我和你都是‘拥有’的那一方。即便能想象对方的心情，也永远无法真正理解。”",
    "113": "确实如此。",
    "114": "心情是可以想象的。",
    "115": "但绝对无法完全理解他人的心情。",
    "116": "“我啊。在想。”",
    "117": "这次她用温柔的声线继续说道。",
    "118": "“想什么呢？”",
    "11`
	salvagedIDs := []string{
		"10", "100", "101", "102", "103", "104", "105", "106", "107", "108",
		"109", "11", "110", "111", "112", "113", "114", "115", "116", "117", "118",
	}
	// 批次中还有 "119"（模型未及生成）：抢救后应落入 Missing。
	wantIDs := append(append([]string{}, salvagedIDs...), "119")

	r := TryRepair(in, wantIDs, allOpts)
	if r.Fatal {
		t.Fatalf("expected salvage to recover prefix, got fatal: %v", r.ParseErr)
	}
	if len(r.Trans) != len(salvagedIDs) {
		t.Errorf("expected %d salvaged translations, got %d", len(salvagedIDs), len(r.Trans))
	}
	if r.Trans["118"] != "“想什么呢？”" {
		t.Errorf("last salvaged entry mismatch: %q", r.Trans["118"])
	}
	if len(r.Missing) != 1 || r.Missing[0] != "119" {
		t.Errorf("expected missing [119], got %v", r.Missing)
	}
	if !contains(r.Repaired, "json.truncation-salvage") {
		t.Errorf("expected json.truncation-salvage in %v", r.Repaired)
	}
}
