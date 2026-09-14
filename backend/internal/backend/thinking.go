package backend

import (
	"fmt"
)

// ThinkingLevel 统一思考强度档位。off = 开关开启且显式关闭思考。
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
)

// Enabled 表示需要向厂商发送档位参数（minimal/low/medium/high）。
func (l ThinkingLevel) Enabled() bool { return l != "" && l != ThinkingOff }

// Thinking 思考控制解析结果。Set 表示 options 提供了 thinking_level（开关开启）。
type Thinking struct {
	Level ThinkingLevel
	Set   bool
}

// Active 表示需要发送档位参数（开关开启且为 minimal/low/medium/high）。
func (t Thinking) Active() bool { return t.Set && t.Level.Enabled() }

// ParseThinking 解析 options["thinking_level"]：缺失/nil/空串 → Set=false（开关关闭）；
// off/minimal/low/medium/high → Set=true；非法值返回 error。
func ParseThinking(m map[string]any) (Thinking, error) {
	raw, ok := m["thinking_level"]
	if !ok || raw == nil {
		return Thinking{}, nil
	}
	s, ok := raw.(string)
	if !ok {
		return Thinking{}, fmt.Errorf("invalid thinking_level: want string, got %T", raw)
	}
	if s == "" {
		return Thinking{}, nil
	}
	switch ThinkingLevel(s) {
	case ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh:
		return Thinking{Level: ThinkingLevel(s), Set: true}, nil
	default:
		return Thinking{}, fmt.Errorf("invalid thinking_level %q (want off|minimal|low|medium|high)", s)
	}
}
