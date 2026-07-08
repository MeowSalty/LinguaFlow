package pipeline

// ContextConfig 是 pipeline 级别的翻译上下文窗口运行时配置。
type ContextConfig struct {
	Enabled  bool
	Before   int
	After    int
	MaxChars int
}

// DefaultContextConfig 返回默认的上下文配置。
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		Enabled:  true,
		Before:   1,
		After:    1,
		MaxChars: 0,
	}
}
