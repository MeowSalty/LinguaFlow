package engine

// TranslateJob 描述一次翻译任务。CLI 层负责构造，engine 层负责执行。
type TranslateJob struct {
	Source DocumentSource // 输入文档源
	Sink   DocumentSink   // 输出文档目标

	SourceLang string // 空字符串表示沿用配置 / auto
	TargetLang string // 空字符串表示沿用配置

	// SegmentIndexes 非空时仅翻译这些 parser 段落索引；未选段落会在输出中保留 ExistingTargets。
	SegmentIndexes  []int
	ExistingTargets map[int]string
}
