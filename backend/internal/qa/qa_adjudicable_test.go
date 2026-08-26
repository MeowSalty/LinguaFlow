package qa

import "testing"

// TestAdjudicableCodes_NoDuplicates 防止 AdjudicableCodes() 出现重复项：
// 重复项会让下游 set 比较"假性通过"，也会让 OpenAPI enum 同步检查失真。
func TestAdjudicableCodes_NoDuplicates(t *testing.T) {
	seen := make(map[string]struct{}, len(AdjudicableCodes()))
	for _, c := range AdjudicableCodes() {
		if _, ok := seen[c]; ok {
			t.Errorf("AdjudicableCodes() 存在重复项 %q", c)
			continue
		}
		seen[c] = struct{}{}
	}
}

// TestDefaultAdjudicateCodes_SubsetOfAdjudicable 保证默认裁决集是可裁决集的子集：
// 执行计划校验白名单按 AdjudicableCodes 构建，默认集一旦越界即产生非法配置。
func TestDefaultAdjudicateCodes_SubsetOfAdjudicable(t *testing.T) {
	adjudicable := sliceToSet(AdjudicableCodes())

	seen := make(map[string]struct{}, len(DefaultAdjudicateCodes()))
	for _, c := range DefaultAdjudicateCodes() {
		if _, ok := adjudicable[c]; !ok {
			t.Errorf("DefaultAdjudicateCodes() 中的 %q 不在 AdjudicableCodes() 内", c)
		}
		if _, ok := seen[c]; ok {
			t.Errorf("DefaultAdjudicateCodes() 存在重复项 %q", c)
			continue
		}
		seen[c] = struct{}{}
	}
}

// TestAdjudicableCodes_SubsetOfAllCheckerNames 保证每个可裁决 code 都是真实注册的
// per-batch checker：裁决轮消费的是 checker 产出的 issue，code 必须能在 Engine 注册表中溯源。
func TestAdjudicableCodes_SubsetOfAllCheckerNames(t *testing.T) {
	checkers := sliceToSet(AllCheckerNames())
	for _, c := range AdjudicableCodes() {
		if _, ok := checkers[c]; !ok {
			t.Errorf("AdjudicableCodes() 中的 %q 不是已注册的 checker 名称（不在 AllCheckerNames() 内）", c)
		}
	}
}

// TestIsAdjudicableCode 覆盖判定函数的正反用例：
// 硬规则 untranslated / duplicate、空串与未知值均不可裁决。
func TestIsAdjudicableCode(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{CheckSourceResidual, true},
		{CheckPunctuationSurplus, true},
		{CheckLengthRatio, true},
		{CheckUntranslated, false},
		{CheckDuplicate, false},
		{"", false},
		{"nonsense", false},
	}
	for _, tt := range tests {
		if got := IsAdjudicableCode(tt.code); got != tt.want {
			t.Errorf("IsAdjudicableCode(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}
