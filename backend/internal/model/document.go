// Package model 定义翻译流水线的核心数据类型。
// 独立于任何处理逻辑，供 pipeline、protect、parser 等包共同引用。
package model

// Segment 是文档中一个可翻译的最小单元。
type Segment struct {
	ID             string            // 稳定 hash（基于 Source）
	Source         string            // 原文；protect stage 之后变为含占位符的版本
	OriginalSource string            // protect 之前的原文快照，供上下文展示给 LLM 使用
	Target         string            // 译文；translate stage 写入；unprotect 还原占位符
	Protected      map[string]string // 占位符 → 原片段
	Meta           map[string]any    // parser 注入的格式信息（块类型、行号、缩进等）
	Skip           bool              // 增量翻译标记：true 时 translate stage 跳过
}

// Document 是 parser 解析后的中间表示。stages 在其上原地修改。
type Document struct {
	Segments   []Segment
	SourceLang string
	TargetLang string
	Format     string         // "markdown" / "srt" ...
	Vars       map[string]any // 提示词模板可访问的全局变量
}
