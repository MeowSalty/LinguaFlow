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
}

// Name 由 BackendConfig.Name 注入；这里使用 type/model 作 fallback。
func (b *Backend) Name() string {
	if b.name != "" {
		return b.name
	}
	return TypeName + ":" + b.model
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
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
		return nil, fmt.Errorf("openai: unknown response_format %q", rf)
	}

	callOpts := []option.RequestOption{}
	if b.timeout > 0 {
		callOpts = append(callOpts, option.WithRequestTimeout(b.timeout))
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

// factory 从 BackendConfig.Options 构造实例。
// 期望的键：api_key, base_url, model, max_tokens, timeout（duration 字符串）,
// response_format（json_schema | json_object | none，默认 json_schema）。
func factory(opts map[string]any) (backend.Backend, error) {
	apiKey, _ := opts["api_key"].(string)
	if apiKey == "" {
		return nil, errors.New("openai: api_key is required")
	}
	clientOpts := []option.RequestOption{option.WithAPIKey(apiKey)}
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
		client:         openaigo.NewClient(clientOpts...),
		model:          backend.StringOpt(opts, "model", "gpt-4o-mini"),
		maxTokens:      backend.Int64Opt(opts, "max_tokens", 0),
		responseFormat: rf,
	}
	if t, err := backend.DurationOpt(opts, "timeout", 60*time.Second); err == nil {
		b.timeout = t
	} else {
		return nil, err
	}
	return b, nil
}

func init() {
	backend.Register(TypeName, factory)
}
