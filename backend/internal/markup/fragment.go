// Package markup 提供译文片段的 XML 结构合法性判定。
//
// 存在理由：EPUB 渲染把译文原样粘进 XHTML 文档，随后对整章做严格 XML 校验
// （parser/epub.renderXHTML）。单个段落的译文只要结构损坏（标签未闭合、嵌套
// 交错、裸 & 等），整章校验就会失败并被降级为原文复制——一个坏段毒死整章。
// 本包把「能否安全嵌入」抽成可复用判定，供导出预检、渲染端单段容错、写入侧
// 守卫与 QA 检测共用同一口径。
package markup

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// fragmentRoot 是校验时包裹片段的合成根元素名。
// 取一个正常文档不会出现的名字，避免与片段自身内容混淆。
const fragmentRoot = "lf-fragment"

// tagRe 探测文本中是否存在 XML / HTML 标签。
// 与 protect.xmlTagRe、qa.xmlTagTokenRe 同源（三处刻意保持一致：本包服务于结构
// 判定，protect 服务于保护区切分，qa 服务于标签守恒计数，职责不同但标签形态口径
// 必须一致）。
var tagRe = regexp.MustCompile(`</?[A-Za-z][A-Za-z0-9:-]*(?:\s+[^<>]*)?/?>`)

// xmlFragmentFormats 是「renderer 把译文原样嵌入 XML 文档」的格式集合。
//
// 只有这类格式的译文才受结构合法性约束：
//   - epub：renderXHTML 直接把译文字节写进 XHTML 元素内容，随后严格校验整章；
//   - docx：htmlToOOXML 用宽松 decoder 解析译文并转义后写入 <w:t>，解析失败降级
//     为纯文本 run，坏译文最多丢格式不丢文本，不需要外部守卫；
//   - html / markdown / text / subtitle / jsonp：译文按字节区间或行号直通，或经
//     序列化器转义，译文里的 <color=red>、裸 & 都是合法内容，加约束反而误伤。
var xmlFragmentFormats = map[string]struct{}{
	"epub": {},
}

// RequiresWellFormedTargets 报告该格式的 renderer 是否把译文原样嵌入 XML 文档，
// 即译文是否必须是 well-formed 片段。
//
// 供 pipeline 等无法 import parser 的位置做格式门禁：parser 依赖 pipeline，
// pipeline 不能反向依赖，因此格式知识落在本包这个更底层的位置。
func RequiresWellFormedTargets(format string) bool {
	_, ok := xmlFragmentFormats[strings.ToLower(strings.TrimSpace(format))]
	return ok
}

// ValidateFragment 报告 s 能否作为 well-formed XML 片段嵌入 XHTML 元素内容。
//
// 判定口径与 EPUB 渲染后的整章严格校验逐字一致：Strict 模式、不开 AutoClose、
// 不注入 HTML 实体表。这个一致性是本包唯一的正确性约束——口径一旦放宽，就会
// 出现「预检放过、渲染仍整章回退」的静默数据丢失。
//
// 因此以下写法一律判为非法，因为它们确实会让整章校验失败：
//   - 未闭合标签：<ruby>基底<rt>注音</rt>
//   - 交错嵌套：<ruby>a<rt>b</ruby></rt>
//   - HTML 空元素简写：<br>（XHTML 须写 <br />）
//   - 裸 & 与未定义实体：A & B、&nbsp;
func ValidateFragment(s string) error {
	if s == "" {
		return nil
	}
	var b strings.Builder
	b.Grow(len(s) + 2*len(fragmentRoot) + 5)
	b.WriteByte('<')
	b.WriteString(fragmentRoot)
	b.WriteByte('>')
	b.WriteString(s)
	b.WriteString("</")
	b.WriteString(fragmentRoot)
	b.WriteByte('>')

	dec := xml.NewDecoder(strings.NewReader(b.String()))
	for {
		_, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fragmentError(err)
		}
	}
}

// unclosedRe / strayCloseRe 从 encoding/xml 的报错里识别两类最常见的结构损坏。
// 解码器是站在合成根的视角描述它们的（「某标签被 lf-fragment 的闭标签关掉了」/
// 「lf-fragment 被某个闭标签关掉了」），直接透出会让用户看到内部实现细节，
// 反而以为自己的译文里有这个标签。
var (
	unclosedRe   = regexp.MustCompile(`element <([^>]+)> closed by </` + fragmentRoot + `>`)
	strayCloseRe = regexp.MustCompile(`element <` + fragmentRoot + `> closed by </([^>]+)>`)
)

// fragmentError 把解码器报错转成可直接展示给用户的说明。
// 四个调用侧（导出预检 detail、手动编辑 detail、QA issue message、日志）共用
// 这一份文案，避免各自再做字符串加工。
func fragmentError(err error) error {
	msg := err.Error()
	if m := unclosedRe.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("标签 <%s> 未闭合", m[1])
	}
	if m := strayCloseRe.FindStringSubmatch(msg); m != nil {
		return fmt.Errorf("多余的闭标签 </%s>", m[1])
	}
	// 其余形态（嵌套交错、裸 & 等）解码器的原文已足够具体，且不含合成根，原样透出。
	return errors.New(msg)
}

// TargetRegression 报告译文相对原文是否发生标签结构退化，返回 nil 表示未退化。
//
// 三重门禁同时成立才判违规：
//  1. source 含至少一个标签——纯文本格式（游戏文本、字幕）的 target 里出现的
//     <color=red> 之类非标签尖括号不受约束；
//  2. source 自身是合法片段——遗留数据的 source_text 可能在提取期就已非法
//     （历史上 CharData 未转义，&amp; 被解码成裸 &），此时无法要求译文比原文更
//     严格，否则每次重译都判违规，整轮任务会被无解地拖死；
//  3. target 不是合法片段。
//
// 用于「输入不可控、判错代价高」的边界（LLM 翻译轮、确定性改写轮、QA 扫描）。
// 导出边界不用本函数——那里的唯一问题是「译文能不能原样交付」，与原文无关。
func TargetRegression(source, target string) error {
	if !tagRe.MatchString(source) {
		return nil
	}
	if ValidateFragment(source) != nil {
		return nil
	}
	return ValidateFragment(target)
}
