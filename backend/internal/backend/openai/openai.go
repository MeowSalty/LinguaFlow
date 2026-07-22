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
		return nil, errors.New("openai: empty choices")
	}
	return &backend.Response{
		Text: resp.Choices[0].Message.Content,
		Usage: backend.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
		Raw: resp,
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
		return nil, errors.New("openai: empty choices")
	}
	return &backend.Response{
		Text: acc.Choices[0].Message.Content,
		Usage: backend.Usage{
			PromptTokens:     acc.Usage.PromptTokens,
			CompletionTokens: acc.Usage.CompletionTokens,
			TotalTokens:      acc.Usage.TotalTokens,
		},
		Raw: acc.ChatCompletion,
	}, nil
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
	if code, ok := backend.ExtractHTTPStatusCode(err.Error()); ok {
		return fmt.Errorf("openai: chat completion: %w",
			&backend.StatusError{StatusCode: code, Err: err})
	}
	return fmt.Errorf("openai: chat completion: %w", err)
}

// factory 从 backend.Config 构造实例。
// Options 期望的键：api_key, base_url, model, max_tokens, timeout（duration 字符串）,
// response_format（json_schema | json_object | none，默认 json_schema）,
// stream（bool，默认 false；true 时以流式发起并在内部累积）。
func factory(cfg backend.Config) (backend.Backend, error) {
	opts := cfg.Options
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
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone:
	default:
		return nil, fmt.Errorf("openai: invalid response_format %q (want json_schema|json_object|text|none)", rf)
	}
	b := &Backend{
		name:           cfg.Name,
		client:         openaigo.NewClient(clientOpts...),
		model:          backend.StringOpt(opts, "model", "gpt-4o-mini"),
		maxTokens:      backend.Int64Opt(opts, "max_tokens", 0),
		responseFormat: rf,
		stream:         backend.BoolOpt(opts, "stream", false),
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

func init() {
	backend.Register(TypeName, factory)
}
