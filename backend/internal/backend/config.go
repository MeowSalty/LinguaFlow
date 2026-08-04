package backend

// Config 是后端工厂的构造参数。
type Config struct {
	Name               string
	Type               string
	Enabled            bool
	RateLimitPerMinute int
	Options            map[string]any
}
