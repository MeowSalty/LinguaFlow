package qa

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestAdjudicableCodesSyncWithOpenAPI 防止 AdjudicableCodes()（Go 单一来源）
// 与 api/openapi/components/schemas/execution-plan-template.yaml 中 adjudicate_codes
// 的 items enum 漂移。任一端新增/删除 code 而另一端未同步时，本测试失败。
//
// 只校验源 yaml（execution-plan-template.yaml），不校验捆绑产物 openapi-3.0.yaml：
// 源文件是权威，bundle 由 task openapi:bundle 重新生成即可。
func TestAdjudicableCodesSyncWithOpenAPI(t *testing.T) {
	yamlPath := locateAdjudicateCodesYAML(t)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", yamlPath, err)
	}

	// 定位 adjudicate_codes 属性块下的首个 enum 行（内联格式）：
	//   adjudicate_codes:
	//     type: array
	//     items:
	//       type: string
	//       enum: [a, b, c]
	// 文件中另有 segment_scope / response_mode 等属性的 enum，必须以
	// adjudicate_codes: 为锚点惰性匹配到其后最近的 enum，避免误取。
	re := regexp.MustCompile(`(?s)adjudicate_codes:\s*.*?enum:\s*\[([^\]]*)\]`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("在 %s 中未找到 adjudicate_codes 的 enum 定义", yamlPath)
	}

	var yamlCodes []string
	for _, raw := range strings.Split(string(m[1]), ",") {
		c := strings.TrimSpace(raw)
		if c != "" {
			yamlCodes = append(yamlCodes, c)
		}
	}
	if len(yamlCodes) == 0 {
		t.Fatalf("adjudicate_codes enum 解析结果为空，正则可能匹配到了错误位置")
	}

	goCodes := AdjudicableCodes()

	// 检查两端各自无重复项（重复项会让下方集合比较"假性通过"）
	for name, codes := range map[string][]string{"Go": goCodes, "OpenAPI": yamlCodes} {
		seen := make(map[string]int, len(codes))
		for _, c := range codes {
			seen[c]++
		}
		for c, n := range seen {
			if n > 1 {
				t.Errorf("%s 端存在重复项 %q（出现 %d 次）", name, c, n)
			}
		}
	}

	// 双向集合比较
	goSet := sliceToSet(goCodes)
	yamlSet := sliceToSet(yamlCodes)

	var missing, extra []string
	for _, c := range goCodes { // 在 Go 但不在 YAML
		if _, ok := yamlSet[c]; !ok {
			missing = append(missing, c)
		}
	}
	for _, c := range yamlCodes { // 在 YAML 但不在 Go
		if _, ok := goSet[c]; !ok {
			extra = append(extra, c)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("AdjudicableCodes() 与 OpenAPI adjudicate_codes enum 不一致:\n"+
			"  Go 端有但 OpenAPI 缺失: %v\n  OpenAPI 有但 Go 端缺失: %v",
			missing, extra)
	}

	// 数量一致性（兜底）
	if len(goCodes) != len(yamlCodes) {
		t.Errorf("数量不一致：Go=%d OpenAPI=%d", len(goCodes), len(yamlCodes))
	}

	// 排序快照，便于人工 diff
	want := append([]string(nil), goCodes...)
	got := append([]string(nil), yamlCodes...)
	sort.Strings(want)
	sort.Strings(got)
}

// locateAdjudicateCodesYAML 从本测试文件位置向上查找仓库根目录
// （以 api/openapi/components/schemas/execution-plan-template.yaml 的存在为标志），
// 对工作目录不敏感。模式与 locateOpenAPIResourcesYAML 一致。
func locateAdjudicateCodesYAML(t *testing.T) string {
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
