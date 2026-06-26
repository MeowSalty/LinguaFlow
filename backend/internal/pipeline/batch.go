package pipeline

// BatchResult 描述一批翻译的结果，传给 BatchHandler 回调。
type BatchResult struct {
	Segments   []TranslatedSegment // 本批已翻译的段落（已 Unprotect + RubyRestore）
	BatchIndex int                 // 批次序号（从 0 开始）
}

// TranslatedSegment 单个已翻译段落。
type TranslatedSegment struct {
	Index      int            // 在 doc.Segments 中的索引
	ID         string         // 段落 ID
	SourceText string         // 原文（OriginalSource，无占位符）
	TargetText string         // 译文（已还原占位符和注音）
	Failed     bool           // true 表示翻译失败，Target 为空
	Meta       map[string]any // 格式元数据
}
