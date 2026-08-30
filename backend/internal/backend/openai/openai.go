// Package openai 是基于 openai/openai-go 的 OpenAI 兼容后端。
// 通过 base_url 切换可指向 Azure OpenAI / Ollama / LM Studio / 自定义网关。
package openai

import (
	"context"
	"errors"
	"fmt"
	"time"

	openaigo "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

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
	thinking       backend.ThinkingLevel
}

// Name 由 BackendConfig.Name 注入；这里使用 type/model 作 fallback。
func (b *Backend) Name() string {
	if b.name != "" {
		return b.name
	}
	return TypeName + ":" + b.model
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	params, err := b.buildParams(req)
	if err != nil {
		return nil, err
	}
	callOpts := b.callOpts()
	if b.stream {
		return b.translateStream(ctx, params, callOpts)
	}
	resp, err := b.client.Chat.Completions.New(ctx, params, callOpts...)
	if err != nil {
		return nil, wrapOpenAIError(err)
	}
	if len(resp.Choices) == 0 {
		return nil, emptyChoicesError(b.Name(), b.model, "", resp.Usage.PromptTokens)
	}
	choice := resp.Choices[0]
	// content_filter / 空补全常返回一个 choice 但 Message.Content 为空。对齐
	// anthropic/google：归为 EmptyResponseError（不可重试），转入 shrink/fallback，
	// 而非把空文本当作成功响应。
	if choice.Message.Content == "" {
		return nil, emptyChoicesError(b.Name(), b.model, choice.FinishReason, resp.Usage.PromptTokens)
	}
	return &backend.Response{
		Text: choice.Message.Content,
		Usage: backend.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
		Raw: resp,
		// 截断（finish_reason=length）时部分文本仍有效：带 Truncated 标志返回，
		// 由上层修复链抢救前缀、缺失部分走重跑通道。
		Truncated: choice.FinishReason == finishReasonLength,
	}, nil
}

func (b *Backend) translateStream(ctx context.Context, params openaigo.ChatCompletionNewParams, callOpts []option.RequestOption) (*backend.Response, error) {
	params.StreamOptions = openaigo.ChatCompletionStreamOptionsParam{
		IncludeUsage: openaigo.Bool(true),
	}
	stream := b.client.Chat.Completions.NewStreaming(ctx, params, callOpts...)
	defer stream.Close()

	acc := openaigo.ChatCompletionAccumulator{}
	for stream.Next() {
		acc.AddChunk(stream.Current())
	}
	if err := stream.Err(); err != nil {
		return nil, wrapOpenAIError(err)
	}
	if len(acc.Choices) == 0 {
		return nil, emptyChoicesError(b.Name(), b.model, "", acc.Usage.PromptTokens)
	}
	choice := acc.Choices[0]
	// 流式同理：choice 非空但累计内容为空（content_filter 常见形态）需归为
	// EmptyResponseError，与非流式路径保持一致。
	if choice.Message.Content == "" {
		return nil, emptyChoicesError(b.Name(), b.model, choice.FinishReason, acc.Usage.PromptTokens)
	}
	return &backend.Response{
		Text: choice.Message.Content,
		Usage: backend.Usage{
			PromptTokens:     acc.Usage.PromptTokens,
			CompletionTokens: acc.Usage.CompletionTokens,
			TotalTokens:      acc.Usage.TotalTokens,
		},
		Raw: acc.ChatCompletion,
		// 流式同理：截断（finish_reason=length）时累积文本仍有效，
		// 带 Truncated 标志交由上层修复链处理，与非流式路径保持一致。
		Truncated: choice.FinishReason == finishReasonLength,
	}, nil
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

func (b *Backend) buildParams(req backend.Request) (openaigo.ChatCompletionNewParams, error) {
	model := req.Model
	if model == "" {
		model = b.model
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = b.maxTokens
	}

	params := openaigo.ChatCompletionNewParams{
		Model: shared.ChatModel(model),
		Messages: []openaigo.ChatCompletionMessageParamUnion{
			openaigo.SystemMessage(req.System),
			openaigo.UserMessage(req.User),
		},
	}
	if req.Temperature != nil {
		params.Temperature = openaigo.Float(*req.Temperature)
	} else if b.temperature != nil {
		params.Temperature = openaigo.Float(*b.temperature)
	}
	if req.TopP != nil {
		params.TopP = openaigo.Float(*req.TopP)
	} else if b.topP != nil {
		params.TopP = openaigo.Float(*b.topP)
	}
	if maxTok > 0 {
		params.MaxTokens = openaigo.Int(maxTok)
	}

	rf := req.ResponseFormat
	if rf == "" {
		rf = b.responseFormat
	}
	switch rf {
	case respFmtJSONSchema:
		params.ResponseFormat = openaigo.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "linguaflow_translations",
					Strict: openaigo.Bool(true),
					Schema: req.JSONSchema,
				},
			},
		}
	case respFmtJSONObject:
		params.ResponseFormat = openaigo.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		}
	case respFmtText, respFmtNone, "":
		// 不设置 ResponseFormat，让网关用默认。
	default:
		return params, fmt.Errorf("openai: unknown response_format %q", rf)
	}
	if b.thinking.Enabled() {
		// low/medium/high 与 shared.ReasoningEffort 字面一致；off 不传字段（零回归）。
		params.ReasoningEffort = shared.ReasoningEffort(b.thinking)
	}
	return params, nil
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
	return backend.WrapUpstreamError("openai: chat completion", err)
}

// factory 从 backend.Config 构造实例。
// Options 期望的键：api_key, base_url, model（必填）, max_tokens, timeout（duration 字符串）,
// response_format（json_schema | json_object | none，默认 json_schema）,
// stream（bool，默认 false；true 时以流式发起并在内部累积）,
// thinking_level（off|low|medium|high，默认 off；off=不传 reasoning_effort）。
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
		option.WithHeader("User-Agent", backend.ClientUserAgent()),
		option.WithHeader("X-Client-Name", backend.ClientName()),
		option.WithHeader("X-Client-Version", backend.ClientVersion()),
	}
	if u, ok := opts["base_url"].(string); ok && u != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(u))
	}
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone:
	default:
		return nil, fmt.Errorf("openai: invalid response_format %q (want json_schema|json_object|text|none)", rf)
	}
	thinking, err := backend.ParseThinkingLevel(opts)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	b := &Backend{
		name:           cfg.Name,
		client:         openaigo.NewClient(clientOpts...),
		model:          model,
		maxTokens:      backend.Int64Opt(opts, "max_tokens", 0),
		responseFormat: rf,
		stream:         backend.BoolOpt(opts, "stream", false),
		thinking:       thinking,
	}
	if t := backend.Int64Opt(opts, "timeout", 60); t > 0 {
		b.timeout = time.Duration(t) * time.Second
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

func modelListerFactory(opts map[string]any) (backend.ModelLister, error) {
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
