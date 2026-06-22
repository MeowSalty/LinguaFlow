package repair

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

var allOpts = Options{
	JSONStructural:       true,
	SchemaAliases:        true,
	Partial:              true,
	PartialThreshold:     0.5,
	PlaceholderNormalize: true,
	PromptUpgrade:        true,
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestTryRepair_HappyPath(t *testing.T) {
	in := `{"translations":{"1":"hello","2":"world"}}`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("unexpected fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "hello" || r.Trans["2"] != "world" {
		t.Errorf("wrong trans: %#v", r.Trans)
	}
	if len(r.Missing) != 0 {
		t.Errorf("expected no missing, got %v", r.Missing)
	}
	if len(r.Repaired) != 0 {
		t.Errorf("happy path should not record repairs, got %v", r.Repaired)
	}
}

func TestTryRepair_CodeFenceAndThinking(t *testing.T) {
	in := "<thinking>scratch {\"reasoning\":\"x\"}</thinking>\n```json\n" +
		`{"translations":{"1":"a","2":"b"}}` + "\n```\nDone."
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" || r.Trans["2"] != "b" {
		t.Errorf("wrong: %#v", r.Trans)
	}
}

func TestTryRepair_StripsBOM(t *testing.T) {
	in := "\uFEFF" + `{"translations":{"1":"a"}}`
	r := TryRepair(in, []string{"1"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	want := "json.strip-bom-zw"
	if !contains(r.Repaired, want) {
		t.Errorf("expected %q in repaired, got %v", want, r.Repaired)
	}
}

func TestTryRepair_TrailingComma(t *testing.T) {
	in := `{"translations":{"1":"a","2":"b",}}`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" || r.Trans["2"] != "b" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if !contains(r.Repaired, "json.trailing-comma") {
		t.Errorf("expected trailing-comma repair, got %v", r.Repaired)
	}
}

func TestTryRepair_UnclosedQuoteIsFatal(t *testing.T) {
	in := `{"translations":{"1":"hello`
	r := TryRepair(in, []string{"1"}, allOpts)
	if !r.Fatal {
		t.Fatalf("expected fatal for unclosed string, got %v", r.Trans)
	}
}

func TestTryRepair_FieldNameAlias(t *testing.T) {
	in := `{"translation":{"1":"a"}}`
	r := TryRepair(in, []string{"1"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if !contains(r.Repaired, "schema.alias") {
		t.Errorf("expected schema.alias, got %v", r.Repaired)
	}
}

func TestTryRepair_FieldNameAliasDisabled(t *testing.T) {
	in := `{"translation":{"1":"a"}}`
	opt := allOpts
	opt.SchemaAliases = false
	r := TryRepair(in, []string{"1"}, opt)
	if !r.Fatal {
		t.Fatalf("expected fatal without alias support, got %v", r.Trans)
	}
}

func TestTryRepair_PartialMissingIDs(t *testing.T) {
	in := `{"translations":{"1":"a","2":"b"}}`
	r := TryRepair(in, []string{"1", "2", "3"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" || r.Trans["2"] != "b" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if !reflect.DeepEqual(sortedStrings(r.Missing), []string{"3"}) {
		t.Errorf("missing mismatch: %v", r.Missing)
	}
}

func TestTryRepair_ExtraIDsIgnored(t *testing.T) {
	in := `{"translations":{"1":"a","2":"b","3":"c"}}`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" || r.Trans["2"] != "b" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if len(r.Missing) != 0 {
		t.Errorf("extra IDs should not produce missing, got %v", r.Missing)
	}
}

func TestTryRepair_PartialWithGlossary(t *testing.T) {
	in := `{"translations":{"1":"你好"},"glossary":[{"source":"Hello","target":"你好","notes":""}]}`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "你好" {
		t.Errorf("trans wrong: %#v", r.Trans)
	}
	if len(r.Glos) != 1 || r.Glos[0].Source != "Hello" {
		t.Errorf("glossary lost: %#v", r.Glos)
	}
	if !reflect.DeepEqual(r.Missing, []string{"2"}) {
		t.Errorf("missing mismatch: %v", r.Missing)
	}
}

func TestTryRepair_MergesMultipleObjects(t *testing.T) {
	in := `{"translations":{"1":"a"}}` + "\n" + `{"translations":{"2":"b"}}`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "a" || r.Trans["2"] != "b" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if !contains(r.Repaired, "json.merge-objects") {
		t.Errorf("expected merge-objects in %v", r.Repaired)
	}
}

func TestTryRepair_SkipsThinkingObjectAndPicksTranslations(t *testing.T) {
	in := `{"reasoning":"step by step"}` + "\n" + `{"translations":{"1":"a"}}`
	r := TryRepair(in, []string{"1"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	// 不应当因此触发 merge-objects（reasoning 对象不含 translations）
	if contains(r.Repaired, "json.merge-objects") {
		t.Errorf("merge should not trigger here: %v", r.Repaired)
	}
}

func TestTryRepair_ControlCharInsideString(t *testing.T) {
	// LLM 直接换行写多行：JSON 里出现裸 \n
	in := "{\"translations\":{\"1\":\"line1\nline2\"}}"
	r := TryRepair(in, []string{"1"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "line1\nline2" {
		t.Errorf("expected literal newline preserved, got %q", r.Trans["1"])
	}
	if !contains(r.Repaired, "json.escape-control") {
		t.Errorf("expected escape-control repair, got %v", r.Repaired)
	}
}

func TestTryRepair_FatalNotJSON(t *testing.T) {
	r := TryRepair("not json at all", []string{"1"}, allOpts)
	if !r.Fatal {
		t.Fatal("expected fatal")
	}
}

func TestTryRepair_CloseUnbalancedBraces(t *testing.T) {
	in := `{"translations":{"1":"a","2":"b"`
	r := TryRepair(in, []string{"1", "2"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v (repaired=%v)", r.ParseErr, r.Repaired)
	}
	if r.Trans["1"] != "a" || r.Trans["2"] != "b" {
		t.Errorf("wrong: %#v", r.Trans)
	}
}

func TestTryRepair_DataNestedTranslations(t *testing.T) {
	in := `{"data":{"translations":{"1":"a"}}}`
	r := TryRepair(in, []string{"1"}, allOpts)
	if r.Fatal {
		t.Fatalf("fatal: %v", r.ParseErr)
	}
	if r.Trans["1"] != "a" {
		t.Errorf("wrong: %#v", r.Trans)
	}
	if !contains(r.Repaired, "schema.alias") {
		t.Errorf("expected schema.alias, got %v", r.Repaired)
	}
}

// ---- placeholder ----

func TestNormalizePlaceholders_LowercaseToKnown(t *testing.T) {
	known := map[string]string{"__LF_000001__": "X"}
	got, fixed := NormalizePlaceholders("hello __lf_000001__ world", known)
	if got != "hello __LF_000001__ world" {
		t.Errorf("text: %q", got)
	}
	if !reflect.DeepEqual(fixed, []string{"__LF_000001__"}) {
		t.Errorf("fixed: %v", fixed)
	}
}

func TestNormalizePlaceholders_MissingUnderscore(t *testing.T) {
	known := map[string]string{"__LF_000001__": "X"}
	got, fixed := NormalizePlaceholders("__LF000001__", known)
	if got != "__LF_000001__" {
		t.Errorf("text: %q", got)
	}
	if len(fixed) != 1 {
		t.Errorf("fixed: %v", fixed)
	}
}

func TestNormalizePlaceholders_PadsShortDigits(t *testing.T) {
	known := map[string]string{"__LF_000005__": "X"}
	got, _ := NormalizePlaceholders("__LF_5__", known)
	if got != "__LF_000005__" {
		t.Errorf("text: %q", got)
	}
}

func TestNormalizePlaceholders_UnknownVariantUnchanged(t *testing.T) {
	known := map[string]string{"__LF_000001__": "X"}
	in := "this mentions __lf_999999__ which we never created"
	got, fixed := NormalizePlaceholders(in, known)
	if got != in {
		t.Errorf("should not rewrite unknown variant, got %q", got)
	}
	if len(fixed) != 0 {
		t.Errorf("should not record fix: %v", fixed)
	}
}

func TestNormalizePlaceholders_AlreadyStandardNoNormalize(t *testing.T) {
	known := map[string]string{"__LF_000001__": "X"}
	got, fixed := NormalizePlaceholders("__LF_000001__", known)
	if got != "__LF_000001__" {
		t.Errorf("text changed unexpectedly: %q", got)
	}
	if len(fixed) != 0 {
		t.Errorf("standard form should not record: %v", fixed)
	}
}

func TestNormalizePlaceholders_EmptyKnownKeys(t *testing.T) {
	got, fixed := NormalizePlaceholders("__lf_000001__", nil)
	if got != "__lf_000001__" {
		t.Errorf("text: %q", got)
	}
	if len(fixed) != 0 {
		t.Errorf("fixed: %v", fixed)
	}
}

// S3: LLM 剥离全部尾部下划线（零尾部下划线）时应能归一。
// 典型场景：__LF_000002 后跟 <ruby>（LLM 剥离了 __LF_000002__ 的尾部 __）
func TestNormalizePlaceholders_ZeroTrailingUnderscores(t *testing.T) {
	known := map[string]string{
		"__LF_000001__": "</span>",
		"__LF_000002__": "<ruby>",
	}
	in := "text__LF_000001<ruby>椎名__LF_000002"
	got, fixed := NormalizePlaceholders(in, known)
	want := "text__LF_000001__<ruby>椎名__LF_000002__"
	if got != want {
		t.Errorf("text:\n got: %q\nwant: %q", got, want)
	}
	if len(fixed) != 2 {
		t.Errorf("expected 2 normalized, got %d: %v", len(fixed), fixed)
	}
}

// S4: LLM 剥离一个尾部下划线时应能归一。
func TestNormalizePlaceholders_SingleTrailingUnderscore(t *testing.T) {
	known := map[string]string{"__LF_000003__": "</rt>"}
	in := "hello__LF_000003_world"
	got, fixed := NormalizePlaceholders(in, known)
	want := "hello__LF_000003__world"
	if got != want {
		t.Errorf("text: %q, want: %q", got, want)
	}
	if len(fixed) != 1 {
		t.Errorf("fixed: %v", fixed)
	}
}

// S5: 零尾部下划线但未知 key 不应被修改。
func TestNormalizePlaceholders_ZeroTrailingUnknownKey(t *testing.T) {
	known := map[string]string{"__LF_000001__": "X"}
	in := "text__LF_999999"
	got, _ := NormalizePlaceholders(in, known)
	if got != in {
		t.Errorf("should not rewrite unknown, got %q", got)
	}
}

// ---- bootstrap ----

func TestTryRepairBootstrap_HappyPath(t *testing.T) {
	in := `{"glossary":[{"source":"x","target":"y","notes":""}]}`
	entries, _, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Source != "x" {
		t.Errorf("entries: %#v", entries)
	}
}

func TestTryRepairBootstrap_FieldAlias(t *testing.T) {
	in := `{"terms":[{"source":"x","target":"y","notes":""}]}`
	entries, repaired, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entries: %#v", entries)
	}
	if !contains(repaired, "schema.alias") {
		t.Errorf("expected schema.alias, got %v", repaired)
	}
}

func TestTryRepairBootstrap_CodeFence(t *testing.T) {
	in := "Sure:\n```json\n" + `{"glossary":[{"source":"x","target":"y","notes":""}]}` + "\n```"
	entries, _, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entries: %#v", entries)
	}
}

func TestTryRepairBootstrap_FiltersAndDedup(t *testing.T) {
	in := `{"glossary":[
		{"source":" hello ","target":" hi ","notes":""},
		{"source":"hello","target":"hi-dup","notes":""},
		{"source":"","target":"y","notes":""}
	]}`
	entries, _, err := TryRepairBootstrap(in, allOpts)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(entries) != 1 || entries[0].Source != "hello" || entries[0].Target != "hi" {
		t.Errorf("filter/dedup wrong: %#v", entries)
	}
}

// ---- BuildRetryReminder ----

func TestBuildRetryReminder_IncludesAllFields(t *testing.T) {
	r := BuildRetryReminder([]string{"3", "5"}, errFake("bad json"), "ABC...")
	if !strings.Contains(r, "Missing IDs: 3, 5") {
		t.Errorf("missing IDs not embedded: %q", r)
	}
	if !strings.Contains(r, "bad json") {
		t.Errorf("err not embedded: %q", r)
	}
	if !strings.Contains(r, `"ABC..."`) {
		t.Errorf("prev head not embedded: %q", r)
	}
	if !strings.Contains(r, `{"translations"`) {
		t.Errorf("schema reminder missing: %q", r)
	}
}

func TestBuildRetryReminder_TruncatesLongPrevHead(t *testing.T) {
	long := strings.Repeat("x", 300)
	r := BuildRetryReminder(nil, nil, long)
	if strings.Count(r, "x") > 210 {
		t.Errorf("not truncated, %d x's", strings.Count(r, "x"))
	}
	if !strings.Contains(r, "…") {
		t.Errorf("expected ellipsis: %q", r)
	}
}

// ---- helpers ----

type errFake string

func (e errFake) Error() string { return string(e) }

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
