package markup

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestValidateFragment_Valid(t *testing.T) {
	cases := []struct {
		name string
		frag string
	}{
		{"empty", ""},
		{"plain cjk", "「レベル６：劣化雷神皇っ！」"},
		{"balanced ruby", "「等级６：<ruby>劣化雷神皇<rt>雷瑟·艾福达尔</rt></ruby>！」"},
		{"nested inline", "<span class=\"a\">这是<b>粗体</b>与<i>斜体</i></span>"},
		{"xhtml void element", "第一行<br />第二行"},
		{"escaped ampersand", "A &amp; B"},
		{"escaped angle brackets", "1 &lt; 2 &gt; 0"},
		{"numeric char ref", "省略&#8230;"},
		{"namespaced tag", "<epub:span>text</epub:span>"},
		{"self closing img", "<img src=\"a.png\" alt=\"图\" />"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateFragment(c.frag); err != nil {
				t.Fatalf("ValidateFragment(%q) = %v, want nil", c.frag, err)
			}
		})
	}
}

func TestValidateFragment_Invalid(t *testing.T) {
	cases := []struct {
		name string
		frag string
	}{
		// 用户报告的坏数据形态：seg 879 丢了 </ruby>
		{"unclosed ruby", "「等级６：<ruby>劣化雷神皇<rt>雷瑟·艾福达尔</rt>！」"},
		// 资源 51 的坏数据形态：ruby 还原按子串插入劈开了合法标签
		{"crossed nesting", "<ruby>a<rt>b</ruby></rt>"},
		{"unclosed span", "<span>文本"},
		{"stray close tag", "文本</p>"},
		{"html void shorthand", "第一行<br>第二行"},
		{"bare ampersand", "A & B"},
		{"undefined html entity", "A&nbsp;B"},
		{"bare less than", "1 < 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateFragment(c.frag); err == nil {
				t.Fatalf("ValidateFragment(%q) = nil, want error", c.frag)
			}
		})
	}
}

// TestValidateFragment_MatchesChapterVerifier 锁定本包与 EPUB 整章校验的口径一致：
// 片段判定合法 ⇒ 嵌入章节后整章校验必然通过；判定非法 ⇒ 整章校验必然失败。
// 口径漂移会导致「预检放过、渲染仍整章回退」的静默数据丢失。
func TestValidateFragment_MatchesChapterVerifier(t *testing.T) {
	frags := []string{
		"", "纯文本", "<ruby>基<rt>注</rt></ruby>", "A &amp; B", "换行<br />后",
		"<ruby>基<rt>注</rt>", "A & B", "<br>", "文本</p>", "<ruby>a<rt>b</ruby></rt>",
	}
	for _, frag := range frags {
		chapter := `<?xml version="1.0" encoding="UTF-8"?>` +
			`<html xmlns="http://www.w3.org/1999/xhtml"><body><p>` + frag + `</p></body></html>`
		fragOK := ValidateFragment(frag) == nil
		chapterOK := chapterWellFormed(chapter)
		if fragOK != chapterOK {
			t.Fatalf("口径不一致 frag=%q: 片段合法=%v 整章合法=%v", frag, fragOK, chapterOK)
		}
	}
}

func TestTargetRegression(t *testing.T) {
	const brokenRuby = "「等级６：<ruby>劣化雷神皇<rt>雷瑟</rt>！」"

	cases := []struct {
		name    string
		source  string
		target  string
		wantErr bool
	}{
		{
			name:    "valid source broken target",
			source:  "「レベル６：<ruby>劣化雷神皇<rt>レツサーエフタル</rt></ruby>っ！」",
			target:  brokenRuby,
			wantErr: true,
		},
		{
			name:   "valid source valid target",
			source: "<ruby>劣化雷神皇<rt>レツサーエフタル</rt></ruby>",
			target: "<ruby>劣化雷神皇<rt>雷瑟</rt></ruby>",
		},
		{
			// preserve_kinds 过滤会合法地把 ruby 标签整体移除，不是退化
			name:   "ruby legitimately dropped",
			source: "<ruby>劣化雷神皇<rt>レツサーエフタル</rt></ruby>",
			target: "劣化雷神皇",
		},
		{
			// 门禁 1：纯文本原文不约束译文里的尖括号（游戏文本 / 字幕）
			name:   "tagless source",
			source: "普通台词",
			target: "<color=red>红色台词",
		},
		{
			// 门禁 2：原文自身非法时无法要求译文更严格
			name:   "invalid source",
			source: "A & B <ruby>基<rt>注</rt></ruby>",
			target: "A & B 基",
		},
		{
			name:   "empty target",
			source: "<ruby>基<rt>注</rt></ruby>",
			target: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := TargetRegression(c.source, c.target)
			if c.wantErr && err == nil {
				t.Fatalf("TargetRegression(%q, %q) = nil, want error", c.source, c.target)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("TargetRegression(%q, %q) = %v, want nil", c.source, c.target, err)
			}
		})
	}
}

// TestValidateFragment_ErrorMessageHidesSyntheticRoot 锁定错误文案：校验时包裹的
// 合成根是实现细节，直接透出会让用户以为自己的译文里有 lf-fragment 标签。四个
// 调用侧（导出 detail、编辑 detail、QA message、日志）都直接展示这段文案。
func TestValidateFragment_ErrorMessageHidesSyntheticRoot(t *testing.T) {
	err := ValidateFragment("「等级６：<ruby>劣化雷神皇<rt>雷瑟</rt>！」")
	if err == nil {
		t.Fatal("缺 </ruby> 的片段应判非法")
	}
	if got := err.Error(); got != "标签 <ruby> 未闭合" {
		t.Errorf("未闭合标签的文案 = %q，want %q", got, "标签 <ruby> 未闭合")
	}
	if got := ValidateFragment("文本</p>"); got == nil || got.Error() != "多余的闭标签 </p>" {
		t.Errorf("游离闭标签的文案 = %v，want %q", got, "多余的闭标签 </p>")
	}
	// 其余形态透出解码器原文，同样不得含合成根名。
	for _, frag := range []string{"A & B", "<ruby>a<rt>b</ruby></rt>", "<br>", "<span>x"} {
		err := ValidateFragment(frag)
		if err == nil {
			t.Fatalf("ValidateFragment(%q) 应判非法", frag)
		}
		if strings.Contains(err.Error(), fragmentRoot) {
			t.Errorf("ValidateFragment(%q) 文案泄漏合成根：%s", frag, err)
		}
	}
}

func TestRequiresWellFormedTargets(t *testing.T) {
	want := map[string]bool{
		"epub": true, "EPUB": true, " epub ": true,
		"docx": false, "html": false, "markdown": false, "txt": false,
		"srt": false, "json": false, "": false,
	}
	for format, expected := range want {
		if got := RequiresWellFormedTargets(format); got != expected {
			t.Errorf("RequiresWellFormedTargets(%q) = %v, want %v", format, got, expected)
		}
	}
}

// chapterWellFormed 复刻 parser/epub.renderXHTML 的整章校验循环。
func chapterWellFormed(doc string) bool {
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if err != nil {
			return errors.Is(err, io.EOF)
		}
	}
}
