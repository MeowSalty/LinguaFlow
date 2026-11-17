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
	bounds, e := backend.NewBuildBounds(0, backend.DefaultMaxInputBytes)
	if e != nil {
		return nil, e
	}
	return b.wireParamsBounded(req, measure, bounds)
}
func (b *Backend) wireParamsBounded(req backend.Request, measure *backend.PromptMeasure, bounds *backend.BuildBounds) (map[string]any, error) {
	if e := backend.ReserveSlice[any](bounds, 2); e != nil {
		return nil, e
	}
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
	return b.MeasureRequestContext(context.Background(), req)
}
func (b *Backend) MeasureRequestPrompt(req backend.Request, m backend.PromptMeasure) (int64, error) {
	return b.MeasureRequestPromptContext(context.Background(), req, m)
}
func (b *Backend) MeasureRequestContext(ctx context.Context, req backend.Request) (int64, error) {
	return backend.MeasureWireBuildContext(ctx, func(bounds *backend.BuildBounds) (any, error) { return b.wireParamsBounded(req, nil, bounds) })
}
func (b *Backend) MeasureRequestPromptContext(ctx context.Context, req backend.Request, m backend.PromptMeasure) (int64, error) {
	return backend.MeasureWireBuildContext(ctx, func(bounds *backend.BuildBounds) (any, error) { return b.wireParamsBounded(req, &m, bounds) })
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
	data, releaseInput, err := backend.EncodeWireBuild(ctx, func(bounds *backend.BuildBounds) (any, error) { return b.wireParamsBounded(req, nil, bounds) })
	if err != nil {
		return nil, err
	}
	defer func() { data = nil; releaseInput() }()
	inputTrace := backend.NewOwnedTrace(ctx, "wire", "openai-input")
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
	if err = b.client.Post(ctx, "chat/completions", data, &resp, opts...); err != nil {
		return nil, wrapOpenAIError(err)
	}
	defer resp.Body.Close()
	var usage backend.Usage
	var finish backend.WireStringSlot
	defer finish.Close()
	model := req.Model
	if model == "" {
		model = b.model
	}
	if !b.stream {
		readTrace := backend.NewOwnedTrace(ctx, "wire", "openai-http")
		defer readTrace.Clear()
		raw, release, e := backend.ReadProviderBounded(ctx, resp.Body, limit, readTrace)
		defer release()
		if e != nil {
			return nil, e
		}
		text, e := backend.NewResponseTextObserved(ctx, limit, "openai-text")
		if e != nil {
			return nil, e
		}
		defer text.ClearObserved()
		e = backend.RunBuild(ctx, func() error {
			reply, e := backend.BorrowWireJSONContext(ctx, raw, limit)
			if e != nil {
				return e
			}
			if usage, e = completionUsage(reply); e != nil {
				return e
			}
			choices, e := reply.Field("choices")
			if e != nil {
				return e
			}
			choice, e := choices.First()
			if e != nil {
				return e
			}
			if choice.IsNull() {
				return emptyChoicesError(b.Name(), model, "", usage.PromptTokens)
			}
			f, e := choice.Field("finish_reason")
			if e != nil {
				return e
			}
			if e = finish.Set(f); e != nil {
				return e
			}
			content, e := choice.Path("message", "content")
			if e != nil {
				return e
			}
			return content.AppendString(text)
		})
		if e != nil {
			return backend.ResponseWithPartialText(text, usage), e
		}
		out, e := backend.FinishResponseText(ctx, text)
		if e != nil {
			return backend.ResponseWithPartialText(text, usage), e
		}
		if out == "" {
			return nil, emptyChoicesError(b.Name(), model, backend.DiagnosticFinishReason(finish.Value), usage.PromptTokens)
		}
		return &backend.Response{Text: out, Usage: usage, Truncated: finish.Value == finishReasonLength}, nil
	}
	text, e := backend.NewResponseTextObserved(ctx, limit, "openai-text")
	if e != nil {
		return nil, e
	}
	defer text.ClearObserved()
	choiceIndex := -1
	err = backend.ReadSSEContext(ctx, resp.Body, limit, func(event []byte) error {
		return backend.RunBuild(ctx, func() error {
			chunk, e := backend.BorrowWireJSONContext(ctx, event, limit)
			if e != nil {
				return e
			}
			failure, e := chunk.Field("error")
			if e != nil {
				return e
			}
			if !failure.IsNull() {
				kind, e := failure.Field("type")
				if e != nil {
					return e
				}
				return backend.ProviderStreamError("openai", kind.StringCode("rate_limit_error", "rate_limit_exceeded", "overloaded_error", "authentication_error", "invalid_request_error"), resp.Header)
			}
			u, e := completionUsage(chunk)
			if e != nil {
				return e
			}
			if u.TotalTokens != 0 || u.PromptTokens != 0 || u.CompletionTokens != 0 {
				usage = u
			}
			choices, e := chunk.Field("choices")
			if e != nil {
				return e
			}
			return choices.EachArray(func(c backend.WireJSONValue) error {
				index, e := c.Field("index")
				if e != nil {
					return e
				}
				i, e := index.Int()
				if e != nil {
					return e
				}
				if choiceIndex < 0 {
					choiceIndex = i
				}
				if i != choiceIndex {
					return nil
				}
				f, e := c.Field("finish_reason")
				if e != nil {
					return e
				}
				if !f.IsNull() && !f.StringEquals("") {
					if e = finish.Set(f); e != nil {
						return e
					}
				}
				content, e := c.Path("delta", "content")
				if e != nil {
					return e
				}
				return content.AppendString(text)
			})
		})
	})
	if err != nil {
		return backend.ResponseWithPartialText(text, usage), err
	}
	out, e := backend.FinishResponseText(ctx, text)
	if e != nil {
		return backend.ResponseWithPartialText(text, usage), e
	}
	if out == "" {
		return nil, emptyChoicesError(b.Name(), model, backend.DiagnosticFinishReason(finish.Value), usage.PromptTokens)
	}
	return &backend.Response{Text: out, Usage: usage, Truncated: finish.Value == finishReasonLength}, nil
}

func completionUsage(reply backend.WireJSONValue) (backend.Usage, error) {
	var usage backend.Usage
	for _, field := range []struct {
		name  string
		value *int64
	}{{"prompt_tokens", &usage.PromptTokens}, {"completion_tokens", &usage.CompletionTokens}, {"total_tokens", &usage.TotalTokens}} {
		v, e := reply.Path("usage", field.name)
		if e != nil {
			return usage, e
		}
		if e = v.DecodeScalar(field.value); e != nil {
			return usage, e
		}
	}
	return usage, nil
}
