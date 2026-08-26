package correct

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestCorrectRuleNamesSyncWithOpenAPI 防止 AllRuleNames()（Go 规则白名单单一来源）
// 与 api/openapi/components/schemas/execution-plan-template.yaml 中
// CorrectRuleConfig.name 的 enum 漂移。任一端新增/删除规则名而另一端未同步时，
// 本测试失败。
func TestCorrectRuleNamesSyncWithOpenAPI(t *testing.T) {
	yamlPath := locateExecutionPlanTemplateYAML(t)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", yamlPath, err)
	}

	// 定位 CorrectRuleConfig 块下首个 enum 行（即 properties.name.enum）：
	//   CorrectRuleConfig:
	//     type: object
	//     required: [name]
	//     properties:
	//       name:
	//         type: string
	//         enum: [a, b, c]
	re := regexp.MustCompile(`CorrectRuleConfig:\s*\n[\s\S]*?enum:\s*\[([^\]]*)\]`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("在 %s 中未找到 CorrectRuleConfig.name 的 enum 定义", yamlPath)
	}

	var yamlNames []string
	for _, raw := range strings.Split(string(m[1]), ",") {
		n := strings.TrimSpace(raw)
		if n != "" {
			yamlNames = append(yamlNames, n)
		}
	}
	if len(yamlNames) == 0 {
		t.Fatalf("在 %s 中解析到空的 CorrectRuleConfig.name enum，疑似正则命中错误位置", yamlPath)
	}

	goNames := AllRuleNames()

	// 检查 Go 端无重复项（重复项会让下方集合比较"假性通过"）
	seen := make(map[string]int, len(goNames))
	for _, n := range goNames {
		seen[n]++
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("AllRuleNames() 存在重复项 %q（出现 %d 次）", n, c)
		}
	}

	// 双向集合比较
	goSet := nameSliceToSet(goNames)
	yamlSet := nameSliceToSet(yamlNames)

	var missing, extra []string
	for _, n := range goNames { // 在 Go 但不在 YAML
		if _, ok := yamlSet[n]; !ok {
			missing = append(missing, n)
		}
	}
	for _, n := range yamlNames { // 在 YAML 但不在 Go
		if _, ok := goSet[n]; !ok {
			extra = append(extra, n)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("correct.AllRuleNames() 与 OpenAPI CorrectRuleConfig.name enum 不一致:\n"+
			"  Go 端有但 OpenAPI 缺失: %v\n  OpenAPI 有但 Go 端缺失: %v",
			missing, extra)
	}

	// 数量一致性（兜底）
	if len(goNames) != len(yamlNames) {
		t.Errorf("数量不一致：Go AllRuleNames()=%d OpenAPI enum=%d", len(goNames), len(yamlNames))
	}
}

func nameSliceToSet(xs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		m[x] = struct{}{}
	}
	return m
}

// locateExecutionPlanTemplateYAML 从本测试文件位置向上查找仓库根目录
// （以 api/openapi/components/schemas/execution-plan-template.yaml 的存在为
// 标志），对工作目录不敏感。
func locateExecutionPlanTemplateYAML(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 获取测试文件路径失败")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "api", "openapi", "components", "schemas", "execution-plan-template.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir { // 已到文件系统根
			break
		}
		dir = parent
	}
	t.Fatal("无法定位 api/openapi/components/schemas/execution-plan-template.yaml（请确认在完整仓库下运行测试）")
	return ""
}
