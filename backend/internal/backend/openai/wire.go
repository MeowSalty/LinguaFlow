package openai

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/openai/openai-go/v3/option"
)

func (b *Backend) wireParams(req backend.Request) (map[string]any, error) {
	return b.wireParamsPrompt(req, nil)
}
func (b *Backend) wireParamsPrompt(req backend.Request, measure *backend.PromptMeasure) (map[string]any, error) {
	system, user, e := backend.PromptValues(req, measure, "")
	if e != nil {
		return nil, e
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = b.maxTokens
	}
	p := map[string]any{"model": model, "messages": []any{map[string]any{"role": "system", "content": system}, map[string]any{"role": "user", "content": user}}}
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
	if maxTok > 0 {
		p["max_tokens"] = maxTok
	}
	rf := req.ResponseFormat
	if rf == "" {
		rf = b.responseFormat
	}
	switch rf {
	case respFmtJSONSchema:
		p["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "linguaflow_translations", "strict": true, "schema": req.JSONSchema}}
	case respFmtJSONObject:
		p["response_format"] = map[string]any{"type": "json_object"}
	case respFmtText, respFmtNone, "":
	default:
		return nil, fmt.Errorf("openai: unknown response_format")
	}
	if b.thinking.Set {
		effort := string(b.thinking.Level)
		if b.thinking.Level == backend.ThinkingOff {
			effort = "none"
		}
		p["reasoning_effort"] = effort
	}
	if b.stream {
		p["stream"] = true
		p["stream_options"] = map[string]any{"include_usage": true}
	}
	return p, nil
}
func (b *Backend) MeasureRequest(req backend.Request) (int64, error) {
	return backend.MeasureWireBuild(func() (any, error) { return b.wireParams(req) })
}
func (b *Backend) MeasureRequestPrompt(req backend.Request, m backend.PromptMeasure) (int64, error) {
	return backend.MeasureWireBuild(func() (any, error) { return b.wireParamsPrompt(req, &m) })
}

type completionWire struct {
	Error *struct {
		Type string `json:"type"`
	} `json:"error"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		TotalTokens      int64 `json:"total_tokens"`
	} `json:"usage"`
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	data, err := backend.EncodeWireBuild(ctx, func() (any, error) { return b.wireParams(req) })
	if err != nil {
		return nil, err
	}
	inputTrace := backend.NewOwnedTrace(ctx, "wire", "openai-input")
	inputTrace.Set(int64(cap(data)))
	defer inputTrace.Clear()
	opts := b.callOpts()
	opts = append(opts, option.WithMaxRetries(0), option.WithMiddleware(backend.GuardProviderErrors))
	var resp *http.Response
	if err = b.client.Post(ctx, "chat/completions", data, &resp, opts...); err != nil {
		return nil, wrapOpenAIError(err)
	}
	defer resp.Body.Close()
	limit := backend.ResponseBudget(ctx, b.responseBytes)
	var usage backend.Usage
	finish := ""
	model := req.Model
	if model == "" {
		model = b.model
	}
	if !b.stream {
		readTrace := backend.NewOwnedTrace(ctx, "wire", "openai-http")
		defer readTrace.Clear()
		raw, e := backend.ReadBoundedObserved(resp.Body, limit, readTrace)
		if e != nil {
			return nil, e
		}
		var result *backend.Response
		e = backend.RunBuild(ctx, func() error {
			var reply completionWire
			if e := backend.DecodeWire(raw, limit, &reply); e != nil {
				return e
			}
			usage = backend.Usage{PromptTokens: reply.Usage.PromptTokens, CompletionTokens: reply.Usage.CompletionTokens, TotalTokens: reply.Usage.TotalTokens}
			if len(reply.Choices) == 0 {
				return emptyChoicesError(b.Name(), model, "", usage.PromptTokens)
			}
			choice := reply.Choices[0]
			if choice.Message.Content == "" {
				return emptyChoicesError(b.Name(), model, choice.FinishReason, usage.PromptTokens)
			}
			result = &backend.Response{Text: choice.Message.Content, Usage: usage, Truncated: choice.FinishReason == finishReasonLength}
			return nil
		})
		return result, e
	}
	text, e := backend.NewResponseTextObserved(ctx, limit, "openai-text")
	if e != nil {
		return nil, e
	}
	defer text.ClearObserved()
	choiceIndex := -1
	err = backend.ReadSSEContext(ctx, resp.Body, limit, func(event []byte) error {
		return backend.RunBuild(ctx, func() error {
			var chunk completionWire
			if e := backend.DecodeWire(event, limit, &chunk); e != nil {
				return e
			}
			if chunk.Error != nil {
				return backend.ProviderStreamError("openai", chunk.Error.Type, resp.Header)
			}
			if chunk.Usage.TotalTokens != 0 || chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
				usage = backend.Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
			}
			for _, c := range chunk.Choices {
				if choiceIndex < 0 {
					choiceIndex = c.Index
				}
				if c.Index != choiceIndex {
					continue
				}
				if c.FinishReason != "" {
					finish = c.FinishReason
				}
				if e := text.Append(c.Delta.Content); e != nil {
					return e
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	out, e := backend.FinishResponseText(ctx, text)
	if e != nil {
		return nil, e
	}
	if out == "" {
		return nil, emptyChoicesError(b.Name(), model, finish, usage.PromptTokens)
	}
	return &backend.Response{Text: out, Usage: usage, Truncated: finish == finishReasonLength}, nil
}
