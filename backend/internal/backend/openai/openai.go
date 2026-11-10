// Package openai 是基于 openai/openai-go 的 OpenAI 兼容后端。
// 通过 base_url 切换可指向 Azure OpenAI / Ollama / LM Studio / 自定义网关。
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const TypeName = "openai"

// finishReasonLength 是 chat completions 的 finish_reason=length（输出 token 上限截断）。
// openai-go v3 对 chat.completions 的 finish_reason 以裸 string 传输（SDK 未提供
// chat 侧枚举常量，legacy completion.go 的 CompletionChoiceFinishReasonLength
// 字面值同为 "length"），此处与其上游 API 字面值对齐。
const finishReasonLength = "length"

// 合法的 response_format 取值。
const (
	respFmtJSONSchema = "json_schema"
	respFmtJSONObject = "json_object"
	respFmtText       = "text"
	respFmtNone       = "none"
)

type Backend struct {
	name           string
	client         openaigo.Client
	model          string
	maxTokens      int64
	timeout        time.Duration
	responseFormat string // backend 默认的响应格式：json_schema | json_object | none
	temperature    *float64
	topP           *float64
	stream         bool
	thinking       backend.Thinking
	responseBytes  int64
}

// Name 由 BackendConfig.Name 注入；这里使用 type/model 作 fallback。
func (b *Backend) Name() string {
	if b.name != "" {
		return b.name
	}
	return TypeName + ":" + b.model
}

// emptyChoicesError 构造带诊断信息的 EmptyResponseError：无 choices 时 finishReason
// 留空；choices 非空但内容为空（content_filter 等）时携带第一个 choice 的 finish_reason，
// 这是内容过滤/截断的关键线索。
func emptyChoicesError(name, model, finishReason string, promptTokens int64) error {
	return &backend.EmptyResponseError{
		BackendName:  name,
		Model:        model,
		FinishReason: finishReason,
		PromptTokens: promptTokens,
	}
}

func (b *Backend) callOpts() []option.RequestOption {
	callOpts := []option.RequestOption{}
	if b.timeout > 0 {
		callOpts = append(callOpts, option.WithRequestTimeout(b.timeout))
	}
	return callOpts
}

func (b *Backend) Close() error { return nil }

// wrapOpenAIError 将 OpenAI SDK 错误包装为 backend.StatusError。
// OpenAI SDK 的 apierror.Error 在 internal 包中，无法直接类型断言。
// 使用字符串解析提取 HTTP 状态码作为兜底方案。
// 错误格式：POST "/v1/chat/completions": 401 Unauthorized {...}
func wrapOpenAIError(err error) error {
	var apiErr *openaigo.Error
	if errors.As(err, &apiErr) {
		var headers http.Header
		if apiErr.Response != nil {
			headers = apiErr.Response.Header
		}
		return backend.WrapHTTPError("openai: chat completion", err, apiErr.StatusCode, headers)
	}
	return backend.WrapUpstreamError("openai: chat completion", err)
}

// factory 从 backend.Config 构造实例。
// Options 期望的键：api_key, base_url, model（必填）, max_tokens, timeout（duration 字符串）,
// response_format（json_schema | json_object | none，默认 json_schema）,
// stream（bool，默认 false；true 时以流式发起并在内部累积）,
// thinking_level（off|minimal|low|medium|high；不设置=开关关闭不传 reasoning_effort，
// off=显式关闭 -> reasoning_effort "none"）。
func factory(cfg backend.Config) (backend.Backend, error) {
	opts := cfg.Options
	apiKey, _ := opts["api_key"].(string)
	if apiKey == "" {
		return nil, errors.New("openai: api_key is required")
	}
	model := backend.StringOpt(opts, "model", "")
	if model == "" {
		return nil, errors.New("openai: model is required")
	}
	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
		option.WithHeader("User-Agent", backend.ClientUserAgent()),
		option.WithHeader("X-Client-Name", backend.ClientName()),
		option.WithHeader("X-Client-Version", backend.ClientVersion()),
	}
	if u, ok := opts["base_url"].(string); ok && u != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(u))
	}
	if cfg.HTTPClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(cfg.HTTPClient))
	}
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone:
	default:
		return nil, fmt.Errorf("openai: invalid response_format %q (want json_schema|json_object|text|none)", rf)
	}
	thinking, err := backend.ParseThinking(opts)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	b := &Backend{
		name:           cfg.Name,
		responseBytes:  cfg.MaxResponseBytes,
		client:         openaigo.NewClient(clientOpts...),
		model:          model,
		maxTokens:      backend.Int64Opt(opts, "max_tokens", 0),
		responseFormat: rf,
		stream:         backend.BoolOpt(opts, "stream", false),
		thinking:       thinking,
	}
	b.timeout, err = backend.DurationOpt(opts, "timeout", 60*time.Second)
	if err != nil || b.timeout < 0 {
		return nil, errors.New("openai: invalid timeout")
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
	client openaigo.Client
}

func modelListerFactory(opts map[string]any, clients ...*http.Client) (backend.ModelLister, error) {
	apiKey, _ := opts["api_key"].(string)
	if apiKey == "" {
		return nil, errors.New("openai: api_key is required")
	}
	clientOpts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHeader("User-Agent", backend.ClientUserAgent()),
		option.WithHeader("X-Client-Name", backend.ClientName()),
		option.WithHeader("X-Client-Version", backend.ClientVersion()),
	}
	if u, ok := opts["base_url"].(string); ok && u != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(u))
	}
	if len(clients) > 0 && clients[0] != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(clients[0]))
	}
	return &modelLister{client: openaigo.NewClient(clientOpts...)}, nil
}

func (l *modelLister) ListModels(ctx context.Context) ([]backend.ModelInfo, error) {
	out := make([]backend.ModelInfo, 0)
	iter := l.client.Models.ListAutoPaging(ctx)
	for iter.Next() {
		m := iter.Current()
		id := m.ID
		if id == "" {
			continue
		}
		out = append(out, backend.ModelInfo{ID: id, Name: id})
		if len(out) >= backend.MaxModels {
			break
		}
	}
	if err := iter.Err(); err != nil {
		return nil, wrapOpenAIModelsError(err)
	}
	return out, nil
}

func wrapOpenAIModelsError(err error) error {
	return backend.WrapUpstreamError("openai: list models", err)
}

func init() {
	backend.Register(TypeName, factory)
	backend.RegisterModelLister(TypeName, modelListerFactory)
}
