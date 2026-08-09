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

// TestFilterableIssueCodesSyncWithOpenAPI 防止 FilterableIssueCodes()（Go 单一来源）
// 与 api/openapi/paths/resources.yaml 中 quality_code 参数的 enum 漂移。
// 任一端新增/删除 code 而另一端未同步时，本测试失败。
func TestFilterableIssueCodesSyncWithOpenAPI(t *testing.T) {
	yamlPath := locateOpenAPIResourcesYAML(t)
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", yamlPath, err)
	}

	// 定位 quality_code 参数块下首个 enum 行：
	//   - name: quality_code
	//     ...
	//     enum: [a, b, c]
	re := regexp.MustCompile(`name:\s*quality_code\b[\s\S]*?enum:\s*\[([^\]]*)\]`)
	m := re.FindSubmatch(data)
	if m == nil {
		t.Fatalf("在 %s 中未找到 quality_code 的 enum 定义", yamlPath)
	}

	var yamlCodes []string
	for _, raw := range strings.Split(string(m[1]), ",") {
		c := strings.TrimSpace(raw)
		if c != "" {
			yamlCodes = append(yamlCodes, c)
		}
	}

	goCodes := FilterableIssueCodes()

	// 检查 Go 端无重复项（重复项会让下方集合比较"假性通过"）
	seen := make(map[string]int, len(goCodes))
	for _, c := range goCodes {
		seen[c]++
	}
	for c, n := range seen {
		if n > 1 {
			t.Errorf("FilterableIssueCodes() 存在重复项 %q（出现 %d 次）", c, n)
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
		t.Errorf("Go FilterableIssueCodes() 与 OpenAPI quality_code enum 不一致:\n"+
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

func sliceToSet(xs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		m[x] = struct{}{}
	}
	return m
}

// locateOpenAPIResourcesYAML 从本测试文件位置向上查找仓库根目录
// （以 api/openapi/paths/resources.yaml 的存在为标志），对工作目录不敏感。
func locateOpenAPIResourcesYAML(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 获取测试文件路径失败")
	}
	dir := filepath.Dir(thisFile)
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "api", "openapi", "paths", "resources.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir { // 已到文件系统根
			break
		}
		dir = parent
	}
	t.Fatal("无法定位 api/openapi/paths/resources.yaml（请确认在完整仓库下运行测试）")
	return ""
}
