package qa

import (
	"context"
	"testing"
)

// TestLengthRatioChecker_RubyTagsExcluded 验证长度比检测在计长前剥离
// <ruby>/<rt> 注音标签，避免注音文本导致源/译长度不对称的误报。
//
// 背景：QA 收到的 SourceText 经注音提取（protect.NewRubyProtector →
// ruby.Extract）剥离了注音（只含基底），而 TargetText 经还原（ruby.RestoreItems）
// 后含 <ruby>...<rt>...</rt></ruby> 标签。
// 若直接计长，译文里的注音假名与标签字符会虚增长度，触发"过长"误报；
// 当用户开启 preserve_kinds 过滤仅保留部分注音时，差异更不稳定。
// 修复：两侧统一调用 ruby.StripRubyTags 后再计长，对齐"基底文本"形态。
func TestLengthRatioChecker_RubyTagsExcluded(t *testing.T) {
	checker := NewLengthRatioChecker(0.2, 3.0, LengthMethodCharWeight)

	tests := []struct {
		name     string
		segments []CheckInput
		want     int
	}{
		{
			name: "ruby in target would inflate length without stripping",
			segments: []CheckInput{
				// 原文（extractor 已剥离注音，仅基底）；加权 6 CJK = 12。
				{Index: 0, SourceText: "呪術廻戦を見る", TargetText: "<ruby>咒术回战<rt>じゅじゅつかいせん</rt></ruby>动画"},
			},
			// 剥前译文含 10 个假名(20) + 标签字符，会超 3.0 误报；剥后 6 CJK = 12，ratio=1.0 通过。
			want: 0,
		},
		{
			name: "ruby in source also stripped for symmetry",
			segments: []CheckInput{
				// 原文若残留 ruby 标签（防御性），译文仅基底，两侧都应剥离。
				{Index: 0, SourceText: "<ruby>呪術廻戦<rt>じゅじゅつかいせん</rt></ruby>を見る", TargetText: "咒术回战动画"},
			},
			want: 0,
		},
		{
			name: "no ruby unaffected",
			segments: []CheckInput{
				{Index: 0, SourceText: "これはテストです", TargetText: "这是一个测试"},
			},
			want: 0,
		},
		{
			name: "genuine too long still detected after stripping",
			segments: []CheckInput{
				{Index: 0, SourceText: "呪術廻戦を見る", TargetText: "<ruby>咒术回战<rt>じゅじゅつかいせん</rt></ruby>这是一个非常非常非常非常非常非常非常非常非常长的译文"},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := checker.Check(context.Background(), tt.segments)
			if len(issues) != tt.want {
				t.Errorf("got %d issues, want %d", len(issues), tt.want)
				for _, iss := range issues {
					t.Logf("  issue: code=%s msg=%s", iss.Code, iss.Message)
				}
			}
		})
	}
}

// TestStripRubyTags 的形态用例已随本地副本消灭迁入 ruby 包
// （TestStripRubyTagsCleansAuxTags）；本文件保留 LengthRatioChecker
// 与剥离行为的集成验证。
