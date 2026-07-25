package backend

import (
	"fmt"
)

// ThinkingLevel 统一思考强度档位。off = 不参与思考控制，沿用模型/网关默认。
type ThinkingLevel string

const (
	ThinkingOff    ThinkingLevel = "off"
	ThinkingLow    ThinkingLevel = "low"
	ThinkingMedium ThinkingLevel = "medium"
	ThinkingHigh   ThinkingLevel = "high"
)

// Enabled 表示需要向厂商发送思考相关参数（非 off / 空）。
func (l ThinkingLevel) Enabled() bool { return l != "" && l != ThinkingOff }

// ParseThinkingLevel 校验 options["thinking_level"]，缺失或空串 → off。非法值返回 error。
func ParseThinkingLevel(m map[string]any) (ThinkingLevel, error) {
	raw, ok := m["thinking_level"]
	if !ok || raw == nil {
		return ThinkingOff, nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("invalid thinking_level: want string, got %T", raw)
	}
	if s == "" {
		return ThinkingOff, nil
	}
	switch ThinkingLevel(s) {
	case ThinkingOff, ThinkingLow, ThinkingMedium, ThinkingHigh:
		return ThinkingLevel(s), nil
	default:
		return "", fmt.Errorf("invalid thinking_level %q (want off|low|medium|high)", s)
	}
}
