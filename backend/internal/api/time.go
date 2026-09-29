package api

import (
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// timePtrToString 将 *time.Time 转换为 *string（UTC RFC3339Nano 格式），nil 输入返回 nil。
func timePtrToString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := timeutil.Format(*t)
	return &s
}
