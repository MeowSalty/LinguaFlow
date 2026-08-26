package templates

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// TestEmbeddedAdjudicationTemplate_DocumentsAllAdjudicableCodes 保证裁决
// 系统提示词模板为 qa.AdjudicableCodes() 中每个可裁决 code 都提供了反引号
// 包裹的规则说明。未来向 AdjudicableCodes 新增 code 而未同步更新模板时，
// 本测试将失败，防止模板与代码漂移。
func TestEmbeddedAdjudicationTemplate_DocumentsAllAdjudicableCodes(t *testing.T) {
	tmpl := EmbeddedAdjudicationTemplate()
	for _, code := range qa.AdjudicableCodes() {
		mention := "`" + code + "`"
		if !strings.Contains(tmpl, mention) {
			t.Errorf("裁决提示词模板缺少对可裁决 code %q 的说明：应包含 %q", code, mention)
		}
	}
}
