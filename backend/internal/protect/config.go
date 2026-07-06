package protect

// Config 是内容保护的运行时配置。
type Config struct {
	Enabled bool
	Rules   []string
}
