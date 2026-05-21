package engine

// TranslateJob 描述一次翻译任务。CLI 层负责构造，engine 层负责执行。
type TranslateJob struct {
	InputPath  string
	OutputPath string
	SourceLang string // 空字符串表示沿用配置 / auto
	TargetLang string // 空字符串表示沿用配置
}
