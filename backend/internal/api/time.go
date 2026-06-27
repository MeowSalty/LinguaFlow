package api

import "time"

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// timePtrToString 将 *time.Time 转换为 *string（RFC3339 格式），nil 输入返回 nil。
func timePtrToString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}
