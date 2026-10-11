package prompt

import (
	"slices"
	"strings"
	"testing"
)

func TestRubyOutputSchemaVersionedIDRequirement(t *testing.T) {
	for _, version := range []int{1, 2} {
		schema := RubyOutputSchema([]string{"1"}, version)
		items := schema["properties"].(map[string]any)["1"].(map[string]any)["items"].(map[string]any)
		required := items["required"].([]string)
		if slices.Contains(required, "id") != (version == 2) {
			t.Fatalf("version %d required=%v", version, required)
		}
	}
	if !strings.Contains(RubyAlignmentJSONTemplate, "省略整条") || !strings.Contains(RubyAlignmentTextTemplate, "JSON 字符串") || strings.Contains(RubyAlignmentJSONTemplate, "可省略 id") {
		t.Fatal("new template does not require reliable IDs")
	}
	if !strings.Contains(LegacyRubyAlignmentJSONTemplate, "可省略 id") {
		t.Fatal("legacy template changed")
	}
}

func TestRubyBatchTemplatesSpecifyIdentityAndEmptyResults(t *testing.T) {
	for _, template := range []string{RubyAlignmentBatchJSONTemplate, RubyAlignmentBatchTextTemplate} {
		for _, required := range []string{"work_id", "candidate_id", "translation_regions", "occurrence", "missing", "原样回显", "每个输入段都必须返回结果"} {
			if !strings.Contains(template, required) {
				t.Fatalf("batch template missing %q", required)
			}
		}
	}
	if !strings.Contains(RubyAlignmentBatchJSONTemplate, `"alignments"`) || !strings.Contains(RubyAlignmentBatchJSONTemplate, "ruby_output 设为 []") {
		t.Fatal("JSON batch template does not define empty members")
	}
	if !strings.Contains(RubyAlignmentBatchTextTemplate, "前六字段均为 JSON 字符串") || !strings.Contains(RubyAlignmentBatchTextTemplate, `"work-1" | "candidate-1" | "" | "" | "" | "" | 0`) || !strings.Contains(RubyAlignmentBatchTextTemplate, "不得重复标记或与成功条目混用") {
		t.Fatal("text batch template does not define the seven-field empty marker")
	}
}
