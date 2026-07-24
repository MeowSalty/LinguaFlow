// Package anthropic 实现基于官方 anthropic-sdk-go 的 Anthropic 后端。
// 通过 Tool Use 强制结构化输出，在 LinguaFlow 协议上等价于 OpenAI 的
// response_format=json_schema。
package anthropic

import (
	"context"
	"errors"
	"fmt"
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
}

func (b *Backend) Name() string {
	if b.name != "" {
		return b.name
	}
	return TypeName + ":" + b.model
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	params, useToolPath, err := b.buildParams(req)
	if err != nil {
		return nil, err
	}
	callOpts := b.callOpts()
	if b.stream {
		return b.translateStream(ctx, params, useToolPath, callOpts)
	}
	msg, err := b.client.Messages.New(ctx, params, callOpts...)
	if err != nil {
		return nil, wrapAnthropicError(err)
	}
	return b.responseFromMessage(msg, useToolPath)
}

func (b *Backend) translateStream(ctx context.Context, params sdk.MessageNewParams, useToolPath bool, callOpts []option.RequestOption) (*backend.Response, error) {
	stream := b.client.Messages.NewStreaming(ctx, params, callOpts...)
	defer stream.Close()

	var msg sdk.Message
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, fmt.Errorf("anthropic: accumulate: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, wrapAnthropicError(err)
	}
	return b.responseFromMessage(&msg, useToolPath)
}

func (b *Backend) responseFromMessage(msg *sdk.Message, useToolPath bool) (*backend.Response, error) {
	// 截断会让 tool_use 的 JSON 残缺，显式失败以触发上层 shrinkOrFallback
	if msg.StopReason == sdk.StopReasonMaxTokens {
		return nil, fmt.Errorf("anthropic: response truncated (stop_reason=max_tokens), raise max_tokens")
	}

	text, err := extractResponseText(msg, useToolPath)
	if err != nil {
		return nil, err
	}

	return &backend.Response{
		Text: text,
		Usage: backend.Usage{
			PromptTokens:     msg.Usage.InputTokens,
			CompletionTokens: msg.Usage.OutputTokens,
			TotalTokens:      msg.Usage.InputTokens + msg.Usage.OutputTokens,
		},
		Raw: msg,
	}, nil
}

func (b *Backend) buildParams(req backend.Request) (sdk.MessageNewParams, bool, error) {
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
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone, "":
	default:
		return sdk.MessageNewParams{}, false, fmt.Errorf("anthropic: unknown response_format %q", rf)
	}

	sysText := req.System
	if rf == respFmtJSONObject {
		// Anthropic 无 json_object 原生支持，用 system 指令模拟
		sysText += "\n\nRespond with a single valid JSON object and nothing else."
	}
	sysBlock := sdk.TextBlockParam{Text: sysText}
	if b.enablePromptCache {
		// 在 system block 上打 ephemeral 缓存断点;TTL 字段留空走默认 5m
		sysBlock.CacheControl = sdk.CacheControlEphemeralParam{}
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: maxTok,
		System:    []sdk.TextBlockParam{sysBlock},
		Messages: []sdk.MessageParam{
			sdk.NewUserMessage(sdk.NewTextBlock(req.User)),
		},
	}
	if req.Temperature != nil {
		params.Temperature = sdk.Float(*req.Temperature)
	} else if b.temperature != nil {
		params.Temperature = sdk.Float(*b.temperature)
	}
	if req.TopP != nil {
		params.TopP = sdk.Float(*req.TopP)
	} else if b.topP != nil {
		params.TopP = sdk.Float(*b.topP)
	}

	useToolPath := rf == respFmtJSONSchema && req.JSONSchema != nil
	if useToolPath {
		params.Tools = []sdk.ToolUnionParam{{
			OfTool: &sdk.ToolParam{
				Name:        toolName,
				Description: sdk.String(toolDescription),
				InputSchema: buildToolInputSchema(req.JSONSchema),
			},
		}}
		params.ToolChoice = sdk.ToolChoiceUnionParam{
			OfTool: &sdk.ToolChoiceToolParam{Name: toolName},
		}
	}
	return params, useToolPath, nil
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
	if code, ok := backend.ExtractHTTPStatusCode(err.Error()); ok {
		return fmt.Errorf("anthropic: messages: %w",
			&backend.StatusError{StatusCode: code, Err: err})
	}
	return fmt.Errorf("anthropic: messages: %w", err)
}

// extractResponseText 把响应内容拼成可供上层 parseBatchResponse 解析的字符串。
// useToolPath=true 时优先在 content 中找 emit_translations 的 tool_use 块，
// 取其 Input(json.RawMessage) 字面值。退化：无 tool_use 时拼所有 text block,
// 让上层 jsonObjectSlice 抢救解析。
func extractResponseText(msg *sdk.Message, useToolPath bool) (string, error) {
	if useToolPath {
		for _, blk := range msg.Content {
			if blk.Type == "tool_use" && blk.Name == toolName {
				if len(blk.Input) == 0 {
					return "", errors.New("anthropic: empty tool_use input")
				}
				return string(blk.Input), nil
			}
		}
	}
	var buf []byte
	for _, blk := range msg.Content {
		if blk.Type == "text" && blk.Text != "" {
			buf = append(buf, blk.Text...)
		}
	}
	if len(buf) == 0 {
		return "", errors.New("anthropic: no usable content in response")
	}
	return string(buf), nil
}

// buildToolInputSchema 把 LinguaFlow 的 JSON Schema map 拆进 ToolInputSchemaParam。
// SDK 固定 Type="object";properties/required 用专字段;其他键 (additionalProperties 等)
// 放进 ExtraFields，让 SDK 在 MarshalJSON 时透传。
func buildToolInputSchema(schema map[string]any) sdk.ToolInputSchemaParam {
	out := sdk.ToolInputSchemaParam{}
	if props, ok := schema["properties"]; ok {
		out.Properties = props
	}
	if req, ok := schema["required"]; ok {
		switch r := req.(type) {
		case []string:
			out.Required = r
		case []any:
			ss := make([]string, 0, len(r))
			for _, v := range r {
				if s, ok := v.(string); ok {
					ss = append(ss, s)
				}
			}
			out.Required = ss
		}
	}
	extras := make(map[string]any)
	for k, v := range schema {
		switch k {
		case "type", "properties", "required":
			continue
		}
		extras[k] = v
	}
	if len(extras) > 0 {
		out.ExtraFields = extras
	}
	return out
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
		option.WithHeader("User-Agent", backend.ClientUserAgent()),
		option.WithHeader("X-Client-Name", backend.ClientName()),
		option.WithHeader("X-Client-Version", backend.ClientVersion()),
	}
	if u := backend.StringOpt(opts, "base_url", ""); u != "" {
		clientOpts = append(clientOpts, option.WithBaseURL(u))
	}
	rf := backend.StringOpt(opts, "response_format", respFmtJSONSchema)
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone:
	default:
		return nil, fmt.Errorf("anthropic: invalid response_format %q (want json_schema|json_object|text|none)", rf)
	}
	b := &Backend{
		name:              cfg.Name,
		client:            sdk.NewClient(clientOpts...),
		model:             model,
		maxTokens:         backend.Int64Opt(opts, "max_tokens", defaultMaxTokens),
		responseFormat:    rf,
		enablePromptCache: backend.BoolOpt(opts, "enable_prompt_cache", true),
		stream:            backend.BoolOpt(opts, "stream", false),
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
