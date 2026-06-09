// Package google 实现基于 googleapis/go-genai 官方库的 Google Gemini 后端。
// 通过 ResponseMIMEType + ResponseJsonSchema 强制结构化输出，在 LinguaFlow 协议上
// 等价于 OpenAI 的 response_format=json_schema。
package google

import (
	"context"
	"errors"
	"fmt"
	"time"

	genai "google.golang.org/genai"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const TypeName = "google"

// 合法的 response_format 取值，与 openai/anthropic 后端对齐。
const (
	respFmtJSONSchema = "json_schema"
	respFmtJSONObject = "json_object"
	respFmtNone       = "none"
)

const (
	defaultModel     = "gemini-2.5-flash"
	defaultMaxTokens = int64(8192) // 与 anthropic 取齐;覆盖批量翻译 + glossary 自举的典型输出量
)

type Backend struct {
	name           string
	client         *genai.Client
	model          string
	maxTokens      int64
	timeout        time.Duration
	responseFormat string
}

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

	rf := req.ResponseFormat
	if rf == "" {
		rf = b.responseFormat
	}
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtNone, "":
	default:
		return nil, fmt.Errorf("google: unknown response_format %q", rf)
	}

	if b.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, b.timeout)
		defer cancel()
	}

	sysText := req.System
	if rf == respFmtJSONObject {
		// Gemini json_object 模式不带 schema，用 system 指令模拟纯 JSON 输出
		sysText += "\n\nRespond with a single valid JSON object and nothing else."
	}

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(sysText, genai.RoleUser),
	}
	if req.Temperature != nil {
		cfg.Temperature = genai.Ptr(float32(*req.Temperature))
	}
	if maxTok > 0 {
		cfg.MaxOutputTokens = int32(maxTok)
	}

	switch rf {
	case respFmtJSONSchema:
		cfg.ResponseMIMEType = "application/json"
		if req.JSONSchema != nil {
			// SDK 支持直接传 map 作为 raw JSON schema，与 LinguaFlow 协议零转换
			cfg.ResponseJsonSchema = req.JSONSchema
		}
	case respFmtJSONObject:
		cfg.ResponseMIMEType = "application/json"
	case respFmtNone, "":
		// 不约束，沿用 Gemini 默认 text/plain
	}

	resp, err := b.client.Models.GenerateContent(
		ctx,
		model,
		[]*genai.Content{genai.NewContentFromText(req.User, genai.RoleUser)},
		cfg,
	)
	if err != nil {
		return nil, fmt.Errorf("google: generate content: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return nil, errors.New("google: empty candidates")
	}
	// 截断会让 JSON 残缺，显式失败以触发上层 shrinkOrFallback
	if resp.Candidates[0].FinishReason == genai.FinishReasonMaxTokens {
		return nil, fmt.Errorf("google: response truncated (finish_reason=MAX_TOKENS), raise max_tokens")
	}

	text := resp.Text()
	if text == "" {
		return nil, errors.New("google: no usable content in response")
	}

	usage := backend.Usage{}
	if um := resp.UsageMetadata; um != nil {
		usage.PromptTokens = int64(um.PromptTokenCount)
		usage.CompletionTokens = int64(um.CandidatesTokenCount)
		usage.TotalTokens = int64(um.TotalTokenCount)
	}

	return &backend.Response{
		Text:  text,
		Usage: usage,
		Raw:   resp,
	}, nil
}

func (b *Backend) Close() error { return nil }

// factory 从 BackendConfig.Options 构造实例。期望的键：
//   - api_key (必填)
//   - base_url (可选，留空走 SDK 默认 https://generativelanguage.googleapis.com/)
//   - model (默认 gemini-2.5-flash)
//   - max_tokens (默认 8192)
//   - timeout (默认 60s, duration 字符串)
//   - response_format (json_schema|json_object|none, 默认 json_schema)
func factory(opts map[string]any) (backend.Backend, error) {
	apiKey := backend.StringOpt(opts, "api_key", "")
	if apiKey == "" {
		return nil, errors.New("google: api_key is required")
	}
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtNone:
	default:
		return nil, fmt.Errorf("google: invalid response_format %q (want json_schema|json_object|none)", rf)
	}

	cc := &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	}
	if u := backend.StringOpt(opts, "base_url", ""); u != "" {
		cc.HTTPOptions = genai.HTTPOptions{BaseURL: u}
	}
	client, err := genai.NewClient(context.Background(), cc)
	if err != nil {
		return nil, fmt.Errorf("google: new client: %w", err)
	}

	b := &Backend{
		client:         client,
		model:          backend.StringOpt(opts, "model", defaultModel),
		maxTokens:      backend.Int64Opt(opts, "max_tokens", defaultMaxTokens),
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
