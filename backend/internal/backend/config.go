package backend

import "net/http"

// Config 是后端工厂的构造参数。
type Config struct {
	Name               string
	Type               string
	Enabled            bool
	RateLimitPerMinute int
	Options            map[string]any
	// HTTPClient is a process dependency; never serialize it into options/snapshots.
	HTTPClient *http.Client `json:"-"`
}
