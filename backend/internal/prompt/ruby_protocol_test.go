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
