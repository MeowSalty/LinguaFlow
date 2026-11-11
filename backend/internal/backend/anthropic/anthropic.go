// Package anthropic 实现基于官方 anthropic-sdk-go 的 Anthropic 后端。
// 通过 Tool Use 强制结构化输出，在 LinguaFlow 协议上等价于 OpenAI 的
// response_format=json_schema。
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const TypeName = "anthropic"

// 合法的 response_format 取值，与 openai 后端对齐。
const (
	respFmtJSONSchema = "json_schema"
	respFmtJSONObject = "json_object"
	respFmtText       = "text"
	respFmtNone       = "none"
)

const (
	defaultMaxTokens = int64(8192) // Anthropic 必填;覆盖典型批量翻译 + glossary
	toolName         = "emit_translations"
	toolDescription  = "Emit the translation result and any extracted glossary entries in the required structured form."
)

type Backend struct {
	name              string
	client            sdk.Client
	model             string
	maxTokens         int64
	timeout           time.Duration
	responseFormat    string
	enablePromptCache bool
	temperature       *float64
	topP              *float64
	stream            bool
	thinking          backend.Thinking
	thinkingBudget    *int64
	responseBytes     int64
}

func (b *Backend) Name() string {
	if b.name != "" {
		return b.name
	}
	return TypeName + ":" + b.model
}

// anthropicThinkingBudget 按档位比例计算 budget_tokens（≥1024 且 < maxTokens）。
//
// 设计原则：透明遵从用户配置，不做输出预留。
// Anthropic 的 budget_tokens 与最终文本/tool JSON 共用同一个 max_tokens 池，
// 这是 API 的客观约束。用户设置 max_tokens 即承担对该池的分配责任；
// 代码按 thinking_level 字面 ratio 切分，不引入与实际输入大小无关的固定预留
// （固定预留只会制造"程序已保证输出"的安全错觉：大输入时输出需求可能远超预留，
//
//	仍会截断，却让用户放松对 batch_size / max_tokens 的主动调优）。
//
// 截断信号由 the controlled wire decoder 的 Response.Truncated 标志传递给上层修复链处理。
func anthropicThinkingBudget(level backend.ThinkingLevel, maxTokens int64) (int64, error) {
	minBudget := int64(1024)
	if maxTokens <= minBudget {
		return 0, fmt.Errorf("anthropic: thinking_level requires max_tokens > %d", minBudget)
	}
	var ratio float64
	switch level {
	case backend.ThinkingMinimal:
		ratio = 0.125
	case backend.ThinkingLow:
		ratio = 0.25
	case backend.ThinkingMedium:
		ratio = 0.50
	case backend.ThinkingHigh:
		ratio = 0.75
	default:
		return 0, fmt.Errorf("anthropic: unexpected thinking_level %q", level)
	}
	budget := int64(float64(maxTokens) * ratio)
	if budget < minBudget {
		budget = minBudget
	}
	if budget > maxTokens-1 {
		budget = maxTokens - 1
	}
	return budget, nil
}

func (b *Backend) callOpts() []option.RequestOption {
	callOpts := []option.RequestOption{}
	if b.timeout > 0 {
		callOpts = append(callOpts, option.WithRequestTimeout(b.timeout))
	}
	return callOpts
}

func (b *Backend) Close() error { return nil }

// wrapAnthropicError 将 Anthropic SDK 错误包装为 backend.StatusError。
// 与 OpenAI 类似，apierror.Error 在 internal 包中。
func wrapAnthropicError(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		var headers http.Header
		if apiErr.Response != nil {
			headers = apiErr.Response.Header
		}
		return backend.WrapHTTPError("anthropic: messages", err, apiErr.StatusCode, headers)
	}
	return backend.WrapUpstreamError("anthropic: messages", err)
}

// factory 从 backend.Config 构造实例。Options 期望的键：
//   - api_key (必填)
//   - base_url (留空走 SDK 默认)
//   - model (必填)
//   - max_tokens (默认 8192,Anthropic 必填)
//   - timeout (默认 60s,duration 字符串)
//   - response_format (json_schema|json_object|none，默认 json_schema)
//   - enable_prompt_cache (bool，默认 true，启用后给 system block 加 ephemeral 缓存)
//   - stream (bool，默认 false；true 时以流式发起并在内部累积)
//   - thinking_level (off|minimal|low|medium|high；不设置=开关关闭不传 thinking，
//     off=显式关闭 thinking，minimal/low/medium/high 开启档位并忽略 temperature/top_p)
func factory(cfg backend.Config) (backend.Backend, error) {
	opts := cfg.Options
	apiKey := backend.StringOpt(opts, "api_key", "")
	if apiKey == "" {
		return nil, errors.New("anthropic: api_key is required")
	}
	model := backend.StringOpt(opts, "model", "")
	if model == "" {
		return nil, errors.New("anthropic: model is required")
	}
	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
		option.WithHeader("User-Agent", backend.ClientUserAgent()),
		option.WithHeader("X-Client-Name", backend.ClientName()),
		option.WithHeader("X-Client-Version", backend.ClientVersion()),
	}
	if u := backend.StringOpt(opts, "base_url", ""); u != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(u))
	}
	if cfg.HTTPClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(cfg.HTTPClient))
	}
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone:
	default:
		return nil, fmt.Errorf("anthropic: invalid response_format %q (want json_schema|json_object|text|none)", rf)
	}
	thinking, err := backend.ParseThinking(opts)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	b := &Backend{
		name:              cfg.Name,
		responseBytes:     cfg.MaxResponseBytes,
		client:            sdk.NewClient(clientOpts...),
		model:             model,
		maxTokens:         backend.Int64Opt(opts, "max_tokens", defaultMaxTokens),
		responseFormat:    rf,
		enablePromptCache: backend.BoolOpt(opts, "enable_prompt_cache", true),
		stream:            backend.BoolOpt(opts, "stream", false),
		thinking:          thinking,
	}
	if _, ok := opts["thinking_budget_tokens"]; ok {
		budget := backend.Int64Opt(opts, "thinking_budget_tokens", 0)
		b.thinkingBudget = &budget
	}
	b.timeout, err = backend.DurationOpt(opts, "timeout", 60*time.Second)
	if err != nil || b.timeout < 0 {
		return nil, errors.New("anthropic: invalid timeout")
	}
	if v, ok := opts["temperature"].(float64); ok {
		b.temperature = &v
	}
	if v, ok := opts["top_p"].(float64); ok {
		b.topP = &v
	}
	return b, nil
}

// modelLister 仅凭 api_key(+base_url) 列模型，不依赖 model。
type modelLister struct {
	client sdk.Client
}

func modelListerFactory(opts map[string]any, clients ...*http.Client) (backend.ModelLister, error) {
	apiKey := backend.StringOpt(opts, "api_key", "")
	if apiKey == "" {
		return nil, errors.New("anthropic: api_key is required")
	}
	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHeader("User-Agent", backend.ClientUserAgent()),
		option.WithHeader("X-Client-Name", backend.ClientName()),
		option.WithHeader("X-Client-Version", backend.ClientVersion()),
	}
	if u := backend.StringOpt(opts, "base_url", ""); u != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(u))
	}
	if len(clients) > 0 && clients[0] != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(clients[0]))
	}
	return &modelLister{client: sdk.NewClient(clientOpts...)}, nil
}

func (l *modelLister) ListModels(ctx context.Context) ([]backend.ModelInfo, error) {
	out := make([]backend.ModelInfo, 0)
	iter := l.client.Models.ListAutoPaging(ctx, sdk.ModelListParams{})
	for iter.Next() {
		m := iter.Current()
		id := m.ID
		if id == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, backend.ModelInfo{ID: id, Name: name})
		if len(out) >= backend.MaxModels {
			break
		}
	}
	if err := iter.Err(); err != nil {
		return nil, wrapAnthropicModelsError(err)
	}
	return out, nil
}

func wrapAnthropicModelsError(err error) error {
	return backend.WrapUpstreamError("anthropic: list models", err)
}

func init() {
	backend.Register(TypeName, factory)
	backend.RegisterModelLister(TypeName, modelListerFactory)
}
