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
		p["tools"] = []any{map[string]any{"name": toolName, "description": toolDescription, "input_schema": req.JSONSchema}}
		p["tool_choice"] = map[string]any{"type": "tool", "name": toolName}
	}
	if b.stream {
		p["stream"] = true
	}
	return p, tools, nil
}
func (b *Backend) MeasureRequest(req backend.Request) (int64, error) {
	return backend.MeasureWireBuild(func() (any, error) { p, _, e := b.wireParams(req); return p, e })
}
func (b *Backend) MeasureRequestPrompt(req backend.Request, m backend.PromptMeasure) (int64, error) {
	return backend.MeasureWireBuild(func() (any, error) { p, _, e := b.wireParamsPrompt(req, &m); return p, e })
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
	data, err := backend.EncodeWireBuild(ctx, func() (any, error) { p, t, e := b.wireParams(req); tools = t; return p, e })
	if err != nil {
		return nil, err
	}
	inputTrace := backend.NewOwnedTrace(ctx, "wire", "anthropic-input")
	inputTrace.Set(int64(cap(data)))
	defer inputTrace.Clear()
	opts := b.callOpts()
	opts = append(opts, option.WithMaxRetries(0), option.WithMiddleware(backend.GuardProviderErrors))
	var resp *http.Response
	if err = b.client.Post(ctx, "v1/messages", data, &resp, opts...); err != nil {
		return nil, wrapAnthropicError(err)
	}
	defer resp.Body.Close()
	limit := backend.ResponseBudget(ctx, b.responseBytes)
	var usage backend.Usage
	finish := ""
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
	if !b.stream {
		readTrace := backend.NewOwnedTrace(ctx, "wire", "anthropic-http")
		defer readTrace.Clear()
		raw, e := backend.ReadBoundedObserved(resp.Body, limit, readTrace)
		if e != nil {
			return nil, e
		}
		e = backend.RunBuild(ctx, func() error {
			var reply messageWire
			if e := backend.DecodeWire(raw, limit, &reply); e != nil {
				return e
			}
			finish = reply.StopReason
			usage = backend.Usage{PromptTokens: reply.Usage.InputTokens, CompletionTokens: reply.Usage.OutputTokens, TotalTokens: reply.Usage.InputTokens + reply.Usage.OutputTokens}
			for _, block := range reply.Content {
				if tools && block.Type == "tool_use" && block.Name == toolName && !toolFound {
					toolFound = true
					plain.ClearObserved()
					plain = nil
					if len(block.Input) > 0 && string(block.Input) != "null" {
						if e = tool.AppendBytes(block.Input); e != nil {
							return e
						}
					}
				}
				if block.Type == "text" && !toolFound {
					if e = plain.Append(block.Text); e != nil {
						return e
					}
				}
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
	} else {
		toolIndex := -1
		activeTextIndex := -1
		toolStartedEmptyObject := false
		err = backend.ReadSSEContext(ctx, resp.Body, limit, func(event []byte) error {
			return backend.RunBuild(ctx, func() error {
				var item messageEvent
				if e := backend.DecodeWire(event, limit, &item); e != nil {
					return e
				}
				switch item.Type {
				case "message_start":
					if item.Message != nil {
						usage.PromptTokens = item.Message.Usage.InputTokens
						usage.CompletionTokens = item.Message.Usage.OutputTokens
						finish = item.Message.StopReason
					}
				case "content_block_start":
					if item.Index < 0 {
						return &backend.BuildCapacityError{Resource: "anthropic content block index"}
					}
					if tools && item.ContentBlock.Type == "tool_use" && item.ContentBlock.Name == toolName && !toolFound {
						toolFound = true
						plain.ClearObserved()
						plain = nil
						toolIndex = item.Index
						if string(item.ContentBlock.Input) == "{}" {
							toolStartedEmptyObject = true
						} else if len(item.ContentBlock.Input) > 0 {
							return tool.AppendBytes(item.ContentBlock.Input)
						}
					}
					if item.ContentBlock.Type == "text" && !toolFound {
						activeTextIndex = item.Index
						return plain.Append(item.ContentBlock.Text)
					}
				case "content_block_delta":
					if item.Index == toolIndex && item.Delta.Type == "input_json_delta" {
						return tool.Append(item.Delta.PartialJSON)
					}
					if activeTextIndex == item.Index && item.Delta.Type == "text_delta" && !toolFound {
						return plain.Append(item.Delta.Text)
					}
				case "content_block_stop":
					if activeTextIndex == item.Index {
						activeTextIndex = -1
					}
				case "message_delta":
					if item.Delta.StopReason != "" {
						finish = item.Delta.StopReason
					}
					usage.CompletionTokens = item.Usage.OutputTokens
				case "error":
					return backend.ProviderStreamError("anthropic", item.Error.Type, resp.Header)
				}
				return nil
			})
		})
		if err != nil {
			return nil, err
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		if toolFound && toolStartedEmptyObject {
			if tool.Len() == 0 {
				if e = tool.Append("{}"); e != nil {
					return nil, e
				}
			}
		}
	}
	selected := plain
	if tools && toolFound {
		selected = tool
	}
	out, e := backend.FinishResponseText(ctx, selected)
	if e != nil {
		return nil, e
	}
	if out == "" {
		return nil, &backend.EmptyResponseError{BackendName: b.Name(), Model: model, FinishReason: finish, PromptTokens: usage.PromptTokens}
	}
	return &backend.Response{Text: out, Usage: usage, Truncated: finish == "max_tokens"}, nil
}
