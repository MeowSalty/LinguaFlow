// Package backend 定义 AI 后端抽象、工厂注册表与选择器。
package backend

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// Request 是发往后端的统一请求。
type Request struct {
	System      string
	User        string
	Model       string
	Temperature float64
	MaxTokens   int64
	Meta        map[string]any // 透传给具体后端（如 stop 序列）

	// ResponseFormat 控制本次请求的响应格式约束。空表示沿用 backend 默认。
	// 合法值（由具体 backend 解释）："json_schema" | "json_object" | "none"。
	// 不识别的 backend 应忽略本字段。
	ResponseFormat string
	// JSONSchema 仅当 ResponseFormat == "json_schema" 时使用，作为 schema 字段提交。
	// 调用方需保证 OpenAI 严格模式的要求（additionalProperties:false、required 列全所有属性等）。
	JSONSchema map[string]any
}

// Usage 描述 token 消耗（部分后端可能未填充）。
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// Response 是后端的统一响应。
type Response struct {
	Text  string
	Usage Usage
	Raw   any // 调试用，可为 nil
}

// Backend 是 AI 翻译后端的抽象。
type Backend interface {
	Name() string
	Translate(ctx context.Context, req Request) (*Response, error)
	Close() error
}

// Factory 接受 BackendConfig.Options 构造后端实例。
type Factory func(opts map[string]any) (Backend, error)

// ErrNotImplemented 由占位后端返回。
var ErrNotImplemented = errors.New("backend: not implemented")

// ErrUnknownBackendType 配置中引用了未注册的 type。
var ErrUnknownBackendType = errors.New("backend: unknown type")

// ErrNoBackend 选择器找不到可用后端。
var ErrNoBackend = errors.New("backend: no enabled backend")

var (
	factoryMu sync.RWMutex
	factories = map[string]Factory{}
)

// Register 注册一个后端 type 的工厂。重复注册会覆盖。
func Register(typ string, f Factory) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	factories[typ] = f
}

// Build 按 BackendConfig 构造后端实例。
func Build(cfg config.BackendConfig) (Backend, error) {
	factoryMu.RLock()
	f, ok := factories[cfg.Type]
	factoryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackendType, cfg.Type)
	}
	return f(cfg.Options)
}

// Selector 按优先级选择已启用的后端，并支持简单回退。
type Selector interface {
	Pick(ctx context.Context, hint string) (Backend, error)
	All() []Backend
	Close() error
}

type prioritySelector struct {
	entries []entry
}

type entry struct {
	name     string
	priority int
	backend  Backend
}

// NewSelector 按 cfgs 顺序构造、按 priority 降序排序，仅纳入 enabled=true 的项。
func NewSelector(cfgs []config.BackendConfig) (Selector, error) {
	var entries []entry
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		b, err := Build(c)
		if err != nil {
			// 单个后端构造失败不阻塞其余后端
			return nil, fmt.Errorf("build backend %q: %w", c.Name, err)
		}
		entries = append(entries, entry{name: c.Name, priority: c.Priority, backend: b})
	}
	if len(entries) == 0 {
		return nil, ErrNoBackend
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].priority > entries[j].priority
	})
	return &prioritySelector{entries: entries}, nil
}

func (s *prioritySelector) Pick(_ context.Context, hint string) (Backend, error) {
	if hint != "" {
		for _, e := range s.entries {
			if e.name == hint {
				return e.backend, nil
			}
		}
	}
	if len(s.entries) == 0 {
		return nil, ErrNoBackend
	}
	return s.entries[0].backend, nil
}

func (s *prioritySelector) All() []Backend {
	out := make([]Backend, len(s.entries))
	for i, e := range s.entries {
		out[i] = e.backend
	}
	return out
}

func (s *prioritySelector) Close() error {
	var firstErr error
	for _, e := range s.entries {
		if err := e.backend.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
