// Package google 实现基于 googleapis/go-genai 官方库的 Google Gemini 后端。
// 通过 ResponseMIMEType + ResponseJsonSchema 强制结构化输出，在 LinguaFlow 协议上
// 等价于 OpenAI 的 response_format=json_schema。
package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	genai "google.golang.org/genai"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const TypeName = "google"

// 合法的 response_format 取值，与 openai/anthropic 后端对齐。
const (
	respFmtJSONSchema = "json_schema"
	respFmtJSONObject = "json_object"
	respFmtText       = "text"
	respFmtNone       = "none"
)

const (
	defaultMaxTokens = int64(8192) // 与 anthropic 取齐;覆盖批量翻译 + glossary 自举的典型输出量
)

type Backend struct {
	name           string
	httpClient     *http.Client
	baseURL        string
	apiKey         string
	responseBytes  int64
	model          string
	maxTokens      int64
	timeout        time.Duration
	responseFormat string
	temperature    *float64
	topP           *float64
	stream         bool
	thinking       backend.Thinking
}

func (b *Backend) Name() string {
	if b.name != "" {
		return b.name
	}
	return TypeName + ":" + b.model
}

func toGoogleThinkingLevel(level backend.ThinkingLevel) genai.ThinkingLevel {
	switch level {
	case backend.ThinkingMinimal:
		return genai.ThinkingLevelMinimal
	case backend.ThinkingLow:
		return genai.ThinkingLevelLow
	case backend.ThinkingMedium:
		return genai.ThinkingLevelMedium
	case backend.ThinkingHigh:
		return genai.ThinkingLevelHigh
	default:
		return genai.ThinkingLevel("")
	}
}

func (b *Backend) Close() error { return nil }

// wrapGoogleError 将 Google SDK 错误包装为 backend.StatusError。
// genai.APIError 是公开类型，可直接 errors.As。
func wrapGoogleError(err error) error {
	var apiErr *genai.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("google: generate content: %w",
			&backend.StatusError{StatusCode: apiErr.Code, Err: err})
	}
	return fmt.Errorf("google: generate content: %w", err)
}

// emptyResponseError 构造带诊断信息的 EmptyResponseError，携带第一个 candidate
// 的 finish_reason（SAFETY/RECITATION 等内容过滤类问题的关键线索）。
func emptyResponseError(b *Backend, finishReason string, promptTokens int64) error {
	return &backend.EmptyResponseError{
		BackendName:  b.Name(),
		Model:        b.model,
		FinishReason: finishReason,
		PromptTokens: promptTokens,
	}
}

// factory 从 backend.Config 构造实例。Options 期望的键：
//   - api_key (必填)
//   - base_url (可选，留空走 SDK 默认 https://generativelanguage.googleapis.com/)
//   - model (必填)
//   - max_tokens (默认 8192)
//   - timeout (默认 60s, duration 字符串)
//   - response_format (json_schema|json_object|none, 默认 json_schema)
//   - stream (bool，默认 false；true 时以流式发起并在内部累积)
//   - thinking_level (off|minimal|low|medium|high；不设置=开关关闭不传 ThinkingConfig，
//     off=显式关闭 -> thinkingBudget 0)
func factory(cfg backend.Config) (backend.Backend, error) {
	opts := cfg.Options
	apiKey := backend.StringOpt(opts, "api_key", "")
	if apiKey == "" {
		return nil, errors.New("google: api_key is required")
	}
	model := backend.StringOpt(opts, "model", "")
	if model == "" {
		return nil, errors.New("google: model is required")
	}
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone:
	default:
		return nil, fmt.Errorf("google: invalid response_format %q (want json_schema|json_object|text|none)", rf)
	}
	thinking, err := backend.ParseThinking(opts)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}

	t, err := backend.DurationOpt(opts, "timeout", 60*time.Second)
	if err != nil || t < 0 {
		return nil, errors.New("google: invalid timeout")
	}
	b := &Backend{
		name:           cfg.Name,
		httpClient:     cfg.HTTPClient,
		baseURL:        backend.StringOpt(opts, "base_url", ""),
		apiKey:         apiKey,
		responseBytes:  cfg.MaxResponseBytes,
		model:          model,
		maxTokens:      backend.Int64Opt(opts, "max_tokens", defaultMaxTokens),
		timeout:        t,
		responseFormat: rf,
		stream:         backend.BoolOpt(opts, "stream", false),
		thinking:       thinking,
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
	client *genai.Client
}

func modelListerFactory(opts map[string]any, clients ...*http.Client) (backend.ModelLister, error) {
	apiKey := backend.StringOpt(opts, "api_key", "")
	if apiKey == "" {
		return nil, errors.New("google: api_key is required")
	}
	headers := make(http.Header)
	headers.Set("User-Agent", backend.ClientUserAgent())
	headers.Set("X-Client-Name", backend.ClientName())
	headers.Set("X-Client-Version", backend.ClientVersion())
	cc := &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	}
	if len(clients) > 0 {
		cc.HTTPClient = clients[0]
	}
	cc.HTTPOptions.Headers = headers
	if u := backend.StringOpt(opts, "base_url", ""); u != "" {
		cc.HTTPOptions.BaseURL = u
	}
	client, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		return nil, fmt.Errorf("google: new client: %w", err)
	}
	return &modelLister{client: client}, nil
}

func (l *modelLister) ListModels(ctx context.Context) ([]backend.ModelInfo, error) {
	out := make([]backend.ModelInfo, 0)
	for m, err := range l.client.Models.All(ctx) {
		if err != nil {
			return nil, wrapGoogleModelsError(err)
		}
		if m == nil {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
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
	return out, nil
}

func wrapGoogleModelsError(err error) error {
	var apiErr *genai.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("google: list models: %w",
			&backend.StatusError{StatusCode: apiErr.Code, Err: err})
	}
	return fmt.Errorf("google: list models: %w", err)
}

func init() {
	backend.Register(TypeName, factory)
	backend.RegisterModelLister(TypeName, modelListerFactory)
}
