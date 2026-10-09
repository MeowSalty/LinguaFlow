package prompt

// RubyTemplates is resolved with the execution specification, not chosen by a
// running pipeline. These defaults are used only when resolving a new execution.
type RubyTemplates struct {
	JSON string
	Text string
}

const LegacyRubyAlignmentJSONTemplate = `你是注音对齐工具。给定原文、译文和尚未对齐的注音条目，确定每个条目在译文中对应的文本。

规则：
- "id" 必须回显输入条目的 id；无法在译文中找到对应文本的条目可省略 id。
- "base" 必须是译文中实际出现的文本（不是原文基底），专有名词等未翻译的词除外。
- "text" 是标注文本：phonetic/semantic 保留原文（不翻译），creative 需要翻译。
- "kind" 是注音分类：
  · phonetic（音注）：纯读音标注。
  · semantic（义训）：语义解释标注，基底与标注语意一致或相近。
  · creative（创意注音）：基底与标注存在语义落差。
- 仅输出 JSON，无额外文字。`

const LegacyRubyAlignmentTextTemplate = `你是注音对齐工具。给定原文、译文和尚未对齐的注音条目，确定每个条目在译文中对应的文本。

规则：
- "base" 必须是译文中实际出现的文本（不是原文基底），专有名词等未翻译的词除外。
- "text" 是标注文本：phonetic/semantic 保留原文（不翻译），creative 需要翻译。
- "kind" 是注音分类：
  · phonetic（音注）：纯读音标注。
  · semantic（义训）：语义解释标注，基底与标注语意一致或相近。
  · creative（创意注音）：基底与标注存在语义落差。
- 每行输出一条，格式为：base | text | kind | id
  （id 可省略：无法在译文中找到对应文本的条目不输出 id）
- 仅输出对齐结果，无额外文字。`

const rubyAlignmentV2Rules = `你是注音对齐工具。给定原文、候选译文、translation_regions 和 missing 条目清单，确定每个条目的译文位置。

规则：
- 成功条目必须原样回显 missing 中的 id；无法确定位置时省略整条，不输出无 id 条目。
- base 是 translation_regions 的 text 中精确出现的正文，不得使用标签、属性或已有 ruby 的内容；不能跨区域拼接。
- occurrence 是 base 在 translation_regions 中按区域顺序、从左到右、非重叠精确出现的序号，从 1 开始。序号按完整清单计算，不因其他条目占用某位置而改变。
- 保留 base/text 中的空白、大小写与 Unicode 字符，不做规范化，不输出字节偏移。
- text 为标注文本：phonetic/semantic 保留原注音，不翻译；creative 需要翻译。
- kind 必须是 phonetic（读音）、semantic（语义解释）、creative（基底与标注有语义落差）之一。
- 每个 id 至多输出一次，不改写候选译文，不输出新的候选正文。`

const RubyAlignmentJSONTemplate = rubyAlignmentV2Rules + `
- 仅输出 JSON 对象 {"ruby_output":[{"id":"6","base":"行","text":"xíng","kind":"phonetic","occurrence":3}]}。
- 每个成功条目必须包含非空字符串 id/base/text、合法 kind 和正整数 occurrence；没有成功条目时输出 {"ruby_output":[]}。`

const RubyAlignmentTextTemplate = rubyAlignmentV2Rules + `
- 每行固定五字段：base | text | kind | id | occurrence。
- 前四字段均为 JSON 字符串字面量，第五字段为正整数。例如："A|B" | "读法" | "phonetic" | "6" | 1。
- 字符串中的引号、反斜杠、换行使用 JSON 转义；不要拆成多行。字段外空白可忽略，字符串内空白必须保留。
- 仅输出成功条目的行，无 markdown 围栏、解释或额外文字；没有成功条目时输出空文本。`
