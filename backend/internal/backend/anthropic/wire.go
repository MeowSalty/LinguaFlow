package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func (b *Backend) wireParams(req backend.Request) (map[string]any, bool, error) {
	return b.wireParamsPrompt(req, nil)
}
func (b *Backend) wireParamsPrompt(req backend.Request, measure *backend.PromptMeasure) (map[string]any, bool, error) {
	bounds, e := backend.NewBuildBounds(0, backend.DefaultMaxInputBytes)
	if e != nil {
		return nil, false, e
	}
	return b.wireParamsBounded(req, measure, bounds)
}
func (b *Backend) wireParamsBounded(req backend.Request, measure *backend.PromptMeasure, bounds *backend.BuildBounds) (map[string]any, bool, error) {
	if e := backend.ReserveSlice[any](bounds, 3); e != nil {
		return nil, false, e
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = b.maxTokens
	}
	if maxTok == 0 {
		maxTok = defaultMaxTokens
	}
	rf := req.ResponseFormat
	if rf == "" {
		rf = b.responseFormat
	}
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone, "":
	default:
		return nil, false, fmt.Errorf("anthropic: unknown response_format")
	}
	suffix := ""
	if rf == respFmtJSONObject {
		suffix = "\n\nRespond with a single valid JSON object and nothing else."
	}
	system, user, e := backend.PromptValues(req, measure, suffix)
	if e != nil {
		return nil, false, e
	}
	block := map[string]any{"type": "text", "text": system}
	if b.enablePromptCache {
		block["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	p := map[string]any{"model": model, "max_tokens": maxTok, "system": []any{block}, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": user}}}}}
	if b.thinking.Active() {
		var budget int64
		var err error
		if b.thinkingBudget != nil {
			budget = *b.thinkingBudget
			if budget < 1024 || budget >= maxTok {
				return nil, false, errors.New("anthropic: invalid resolved thinking budget")
			}
		} else {
			budget, err = anthropicThinkingBudget(b.thinking.Level, maxTok)
			if err != nil {
				return nil, false, err
			}
		}
		p["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	} else {
		if b.thinking.Set {
			p["thinking"] = map[string]any{"type": "disabled"}
		}
		if req.Temperature != nil {
			p["temperature"] = *req.Temperature
		} else if b.temperature != nil {
			p["temperature"] = *b.temperature
		}
		if req.TopP != nil {
			p["top_p"] = *req.TopP
		} else if b.topP != nil {
			p["top_p"] = *b.topP
		}
	}
	tools := rf == respFmtJSONSchema && req.JSONSchema != nil
	if tools {
		if err := backend.ReserveSlice[any](bounds, 1); err != nil {
			return nil, false, err
		}
		p["tools"] = []any{map[string]any{"name": toolName, "description": toolDescription, "input_schema": req.JSONSchema}}
		p["tool_choice"] = map[string]any{"type": "tool", "name": toolName}
	}
	if b.stream {
		p["stream"] = true
	}
	return p, tools, nil
}
func (b *Backend) MeasureRequest(req backend.Request) (int64, error) {
	return b.MeasureRequestContext(context.Background(), req)
}
func (b *Backend) MeasureRequestContext(ctx context.Context, req backend.Request) (int64, error) {
	return backend.MeasureWireBuildContext(ctx, func(bounds *backend.BuildBounds) (any, error) {
		p, _, e := b.wireParamsBounded(req, nil, bounds)
		return p, e
	})
}
func (b *Backend) MeasureRequestPrompt(req backend.Request, m backend.PromptMeasure) (int64, error) {
	return b.MeasureRequestPromptContext(context.Background(), req, m)
}
func (b *Backend) MeasureRequestPromptContext(ctx context.Context, req backend.Request, m backend.PromptMeasure) (int64, error) {
	return backend.MeasureWireBuildContext(ctx, func(bounds *backend.BuildBounds) (any, error) {
		p, _, e := b.wireParamsBounded(req, &m, bounds)
		return p, e
	})
}

type messageBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}
type messageUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}
type messageWire struct {
	Content    []messageBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      messageUsage   `json:"usage"`
}
type messageEvent struct {
	Type         string       `json:"type"`
	Index        int          `json:"index"`
	Message      *messageWire `json:"message"`
	ContentBlock messageBlock `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Type string `json:"type"`
	} `json:"error"`
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	var tools bool
	data, releaseInput, err := backend.EncodeWireBuild(ctx, func(bounds *backend.BuildBounds) (any, error) {
		p, t, e := b.wireParamsBounded(req, nil, bounds)
		tools = t
		return p, e
	})
	if err != nil {
		return nil, err
	}
	defer func() { data = nil; releaseInput() }()
	inputTrace := backend.NewOwnedTrace(ctx, "wire", "anthropic-input")
	inputTrace.Set(int64(cap(data)))
	defer inputTrace.Clear()
	limit := backend.ResponseBudget(ctx, b.responseBytes)
	ctx, err = backend.WithProviderResponseAllocation(ctx, limit)
	if err != nil {
		return nil, err
	}
	opts := b.callOpts()
	opts = append(opts, option.WithMaxRetries(0), option.WithMiddleware(backend.GuardProviderErrors))
	var resp *http.Response
	if err = b.client.Post(ctx, "v1/messages", data, &resp, opts...); err != nil {
		return nil, wrapAnthropicError(err)
	}
	defer resp.Body.Close()
	var usage backend.Usage
	var finish backend.WireStringSlot
	defer finish.Close()
	model := req.Model
	if model == "" {
		model = b.model
	}
	plain, e := backend.NewResponseTextObserved(ctx, limit, "anthropic-text")
	if e != nil {
		return nil, e
	}
	defer plain.ClearObserved()
	tool, e := backend.NewResponseTextObserved(ctx, limit, "anthropic-tool")
	if e != nil {
		return nil, e
	}
	defer tool.ClearObserved()
	toolFound := false
	partial := func() *backend.Response {
		selected := plain
		if tools && toolFound {
			selected = tool
		}
		return backend.ResponseWithPartialText(selected, usage)
	}
	startTool := func(block backend.WireJSONValue) error {
		toolFound = true
		plain.ClearObserved()
		input, e := block.Field("input")
		if e != nil {
			return e
		}
		if !input.IsNull() {
			return tool.AppendBytes(input.Raw())
		}
		return nil
	}
	consumeBlock := func(block backend.WireJSONValue) error {
		kind, e := block.Field("type")
		if e != nil {
			return e
		}
		name, e := block.Field("name")
		if e != nil {
			return e
		}
		if tools && kind.StringEquals("tool_use") && name.StringEquals(toolName) && !toolFound {
			return startTool(block)
		}
		if kind.StringEquals("text") && !toolFound {
			text, e := block.Field("text")
			if e != nil {
				return e
			}
			return text.AppendString(plain)
		}
		return nil
	}
	if !b.stream {
		trace := backend.NewOwnedTrace(ctx, "wire", "anthropic-http")
		defer trace.Clear()
		raw, release, e := backend.ReadProviderBounded(ctx, resp.Body, limit, trace)
		defer release()
		if e != nil {
			return partial(), e
		}
		e = backend.RunBuild(ctx, func() error {
			reply, e := backend.BorrowWireJSONContext(ctx, raw, limit)
			if e != nil {
				return e
			}
			if e = anthropicUsage(reply, &usage); e != nil {
				return e
			}
			reason, e := reply.Field("stop_reason")
			if e != nil {
				return e
			}
			if e = finish.Set(reason); e != nil {
				return e
			}
			blocks, e := reply.Field("content")
			if e != nil {
				return e
			}
			return blocks.EachArray(consumeBlock)
		})
		if e != nil {
			return partial(), e
		}
	} else {
		toolIndex, activeTextIndex := -1, -1
		toolStartedEmptyObject := false
		err = backend.ReadSSEContext(ctx, resp.Body, limit, func(event []byte) error {
			return backend.RunBuild(ctx, func() error {
				item, e := backend.BorrowWireJSONContext(ctx, event, limit)
				if e != nil {
					return e
				}
				kind, e := item.Field("type")
				if e != nil {
					return e
				}
				switch kind.StringCode("message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "error") {
				case "message_start":
					message, e := item.Field("message")
					if e != nil {
						return e
					}
					if message.IsNull() {
						return nil
					}
					if e = anthropicUsage(message, &usage); e != nil {
						return e
					}
					reason, e := message.Field("stop_reason")
					if e != nil {
						return e
					}
					return finish.Set(reason)
				case "content_block_start":
					index, e := item.Field("index")
					if e != nil {
						return e
					}
					i, e := index.Int()
					if e != nil {
						return e
					}
					if i < 0 {
						return &backend.BuildCapacityError{Resource: "anthropic content block index"}
					}
					block, e := item.Field("content_block")
					if e != nil {
						return e
					}
					t, e := block.Field("type")
					if e != nil {
						return e
					}
					name, e := block.Field("name")
					if e != nil {
						return e
					}
					if tools && t.StringEquals("tool_use") && name.StringEquals(toolName) && !toolFound {
						toolFound = true
						plain.ClearObserved()
						toolIndex = i
						input, e := block.Field("input")
						if e != nil {
							return e
						}
						if string(input.Raw()) == "{}" {
							toolStartedEmptyObject = true
						} else if !input.IsNull() {
							return tool.AppendBytes(input.Raw())
						}
					}
					if t.StringEquals("text") && !toolFound {
						activeTextIndex = i
						text, e := block.Field("text")
						if e != nil {
							return e
						}
						return text.AppendString(plain)
					}
				case "content_block_delta":
					index, e := item.Field("index")
					if e != nil {
						return e
					}
					i, e := index.Int()
					if e != nil {
						return e
					}
					delta, e := item.Field("delta")
					if e != nil {
						return e
					}
					t, e := delta.Field("type")
					if e != nil {
						return e
					}
					if i == toolIndex && t.StringEquals("input_json_delta") {
						part, e := delta.Field("partial_json")
						if e != nil {
							return e
						}
						return part.AppendString(tool)
					}
					if i == activeTextIndex && t.StringEquals("text_delta") && !toolFound {
						text, e := delta.Field("text")
						if e != nil {
							return e
						}
						return text.AppendString(plain)
					}
				case "content_block_stop":
					index, e := item.Field("index")
					if e != nil {
						return e
					}
					i, e := index.Int()
					if e != nil {
						return e
					}
					if i == activeTextIndex {
						activeTextIndex = -1
					}
				case "message_delta":
					reason, e := item.Path("delta", "stop_reason")
					if e != nil {
						return e
					}
					if !reason.IsNull() && !reason.StringEquals("") {
						if e = finish.Set(reason); e != nil {
							return e
						}
					}
					tokens, e := item.Path("usage", "output_tokens")
					if e != nil {
						return e
					}
					return tokens.DecodeScalar(&usage.CompletionTokens)
				case "error":
					t, e := item.Path("error", "type")
					if e != nil {
						return e
					}
					return backend.ProviderStreamError("anthropic", t.StringCode("rate_limit_error", "overloaded_error", "authentication_error", "invalid_request_error"), resp.Header)
				}
				return nil
			})
		})
		if err != nil {
			return partial(), err
		}
		if toolFound && toolStartedEmptyObject && tool.Len() == 0 {
			if e = tool.Append("{}"); e != nil {
				return partial(), e
			}
		}
	}
	usage.TotalTokens, e = backend.CheckedAdd(usage.PromptTokens, usage.CompletionTokens)
	if e != nil {
		return partial(), e
	}
	selected := plain
	if tools && toolFound {
		selected = tool
	}
	out, e := backend.FinishResponseText(ctx, selected)
	if e != nil {
		return partial(), e
	}
	if out == "" {
		return nil, &backend.EmptyResponseError{BackendName: b.Name(), Model: model, FinishReason: backend.DiagnosticFinishReason(finish.Value), PromptTokens: usage.PromptTokens}
	}
	return &backend.Response{Text: out, Usage: usage, Truncated: finish.Value == "max_tokens"}, nil
}
func anthropicUsage(value backend.WireJSONValue, usage *backend.Usage) error {
	for _, field := range []struct {
		name  string
		value *int64
	}{{"input_tokens", &usage.PromptTokens}, {"output_tokens", &usage.CompletionTokens}} {
		token, e := value.Path("usage", field.name)
		if e != nil {
			return e
		}
		if e = token.DecodeScalar(field.value); e != nil {
			return e
		}
	}
	return nil
}
