package backend

import (
	"fmt"
	"time"
)

// StringOpt 从 options map 读取字符串。空串或类型不符均回退到 def。
func StringOpt(m map[string]any, k, def string) string {
	if v, ok := m[k].(string); ok && v != "" {
		return v
	}
	return def
}

// Int64Opt 从 options map 读取 int64。yaml 解码常给 int / float64，统一归一。
func Int64Opt(m map[string]any, k string, def int64) int64 {
	switch v := m[k].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return def
}

// Float64Opt 同 Int64Opt，容忍 int/int64/float32/float64。
func Float64Opt(m map[string]any, k string, def float64) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return def
}

// BoolOpt 从 options map 读取 bool。缺失或类型不符回退 def。
func BoolOpt(m map[string]any, k string, def bool) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return def
}

// DurationOpt 解析 duration:字符串走 time.ParseDuration;数字按秒计;缺失回退 def。
// 字符串解析失败返回 error，调用方应当中止后端构造。
func DurationOpt(m map[string]any, k string, def time.Duration) (time.Duration, error) {
	v, ok := m[k]
	if !ok {
		return def, nil
	}
	switch x := v.(type) {
	case string:
		d, err := time.ParseDuration(x)
		if err != nil {
			return 0, fmt.Errorf("backend: invalid %s %q: %w", k, x, err)
		}
		return d, nil
	case int:
		return time.Duration(x) * time.Second, nil
	case int64:
		return time.Duration(x) * time.Second, nil
	case float64:
		return time.Duration(x) * time.Second, nil
	}
	return def, nil
}
