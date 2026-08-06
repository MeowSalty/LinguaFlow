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
	thinking          backend.ThinkingLevel
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
		if b.thinking.Enabled() {
			// thinking 开启时 budget_tokens 与最终输出共用 max_tokens 池：
			// 截断可能源于思考预算挤占输出，提示用户从三处可调方向定位。
			return nil, fmt.Errorf("anthropic: response truncated (stop_reason=max_tokens); thinking_level=%s shares max_tokens between thinking budget and output — raise max_tokens, lower thinking_level, or shrink batch_size", b.thinking)
		}
		return nil, fmt.Errorf("anthropic: response truncated (stop_reason=max_tokens), raise max_tokens")
	}

	text, err := extractResponseText(msg, useToolPath, b, msg.StopReason, msg.Usage.InputTokens)
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
	// 开启 thinking 时 API 拒绝非默认 temperature/top_p，整段跳过采样参数。
	if b.thinking.Enabled() {
		budget, err := anthropicThinkingBudget(b.thinking, maxTok)
		if err != nil {
			return sdk.MessageNewParams{}, false, err
		}
		params.Thinking = sdk.ThinkingConfigParamOfEnabled(budget)
	} else {
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
// 截断保护由 responseFromMessage 的可定位错误信息承担（见 stop_reason=max_tokens 分支）。
func anthropicThinkingBudget(level backend.ThinkingLevel, maxTokens int64) (int64, error) {
	minBudget := int64(1024)
	if maxTokens <= minBudget {
		return 0, fmt.Errorf("anthropic: thinking_level requires max_tokens > %d", minBudget)
	}
	var ratio float64
	switch level {
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
	return backend.WrapUpstreamError("anthropic: messages", err)
}

// extractResponseText 把响应内容拼成可供上层 parseBatchResponse 解析的字符串。
// useToolPath=true 时优先在 content 中找 emit_translations 的 tool_use 块，
// 取其 Input(json.RawMessage) 字面值。退化：无 tool_use 时拼所有 text block,
// 让上层 jsonObjectSlice 抢救解析。
// 空内容时返回 EmptyResponseError（携带 stop_reason/input_tokens 诊断信息），
// 使上层将其归为不可重试，转入 shrink/fallback 而非退避重试刷屏。
func extractResponseText(msg *sdk.Message, useToolPath bool, b *Backend, stopReason sdk.StopReason, inputTokens int64) (string, error) {
	if useToolPath {
		for _, blk := range msg.Content {
			if blk.Type == "tool_use" && blk.Name == toolName {
				if len(blk.Input) == 0 {
					return "", &backend.EmptyResponseError{
						BackendName:  b.Name(),
						Model:        b.model,
						FinishReason: string(stopReason),
						PromptTokens: inputTokens,
					}
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
		return "", &backend.EmptyResponseError{
			BackendName:  b.Name(),
			Model:        b.model,
			FinishReason: string(stopReason),
			PromptTokens: inputTokens,
		}
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
//   - thinking_level (off|low|medium|high，默认 off；开启时忽略 temperature/top_p)
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
	thinking, err := backend.ParseThinkingLevel(opts)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	b := &Backend{
		name:              cfg.Name,
		client:            sdk.NewClient(clientOpts...),
		model:             model,
		maxTokens:         backend.Int64Opt(opts, "max_tokens", defaultMaxTokens),
		responseFormat:    rf,
		enablePromptCache: backend.BoolOpt(opts, "enable_prompt_cache", true),
		stream:            backend.BoolOpt(opts, "stream", false),
		thinking:          thinking,
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
	client sdk.Client
}

func modelListerFactory(opts map[string]any) (backend.ModelLister, error) {
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
