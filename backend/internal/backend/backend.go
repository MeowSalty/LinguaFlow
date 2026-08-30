// Package backend 定义 AI 后端抽象与工厂注册表。
package backend

import (
	"context"
	"errors"
	"fmt"
)

// Request 是发往后端的统一请求。
type Request struct {
	System string
	User   string
	Model  string
	// Temperature 控制采样温度。nil 表示未设置，回退到后端默认值；
	// 指向 0 表示确定性输出（greedy decoding）；其他合法范围取决于具体后端。
	Temperature *float64
	// TopP 核采样参数。nil 表示未设置，回退到后端默认值；
	// 1.0 表示不限制；0.9 表示仅考虑累积概率前90%的token。
	TopP      *float64
	MaxTokens int64
	Meta      map[string]any // 透传给具体后端（如 stop 序列）

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

	// Truncated 表示响应因输出 token 上限被截断（finish_reason=MAX_TOKENS/length，
	// stop_reason=max_tokens）。部分文本仍然有效：上层修复链会抢救完整前缀，
	// 缺失条目走重跑通道。仅作信号与诊断，不影响调用方对 Text 的正常解析。
	Truncated bool
}

// Backend 是 AI 翻译后端的抽象。
type Backend interface {
	Name() string
	Translate(ctx context.Context, req Request) (*Response, error)
	Close() error
}

// Factory 接受完整 Config 构造后端实例。
type Factory func(cfg Config) (Backend, error)

// ErrNotImplemented 由占位后端返回。
var ErrNotImplemented = errors.New("backend: not implemented")

// ErrUnknownBackendType 配置中引用了未注册的 type。
var ErrUnknownBackendType = errors.New("backend: unknown type")

// ErrNoBackend 选择器找不到可用后端。
var ErrNoBackend = errors.New("backend: no enabled backend")

// EmptyResponseError 表示上游返回 HTTP 200 但响应中无可用于翻译的内容。
// 典型成因：内容过滤（finish_reason=content_filter）、空补全、网关抽风。
// 这类错误不应按网络/限流错误退避重试——重试基本无效且会刷屏；
// 上层 IsRetryable 对其返回 false，使其转入 shrink/fallback 路径。
type EmptyResponseError struct {
	BackendName  string
	Model        string
	FinishReason string // 第一个 choice 的 finish_reason；无 choices 时为空
	PromptTokens int64  // 若上游返回了 usage 则填充
}

func (e *EmptyResponseError) Error() string {
	if e == nil {
		return "backend: empty response"
	}
	detail := "no usable content in response"
	if e.FinishReason != "" {
		detail = fmt.Sprintf("empty choices (finish_reason=%s)", e.FinishReason)
	}
	prefix := e.BackendName
	if prefix == "" {
		prefix = "backend"
	}
	if e.PromptTokens > 0 {
		return fmt.Sprintf("%s: %s; model=%s prompt_tokens=%d", prefix, detail, e.Model, e.PromptTokens)
	}
	return fmt.Sprintf("%s: %s; model=%s", prefix, detail, e.Model)
}

// factories 在 init 阶段由各后端包写入，main 后只读。
// Go 内存模型保证所有 init 先于 main 执行（happens-before），因此无需加锁。
var factories = map[string]Factory{}

// Register 注册一个后端 type 的工厂。仅应在 init 中调用。
func Register(typ string, f Factory) {
	factories[typ] = f
}

// Build 按 Config 构造后端实例。
func Build(cfg Config) (Backend, error) {
	f, ok := factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackendType, cfg.Type)
	}
	return f(cfg)
}
