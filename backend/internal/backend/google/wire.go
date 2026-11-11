package google

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func (b *Backend) wireParams(req backend.Request) (string, map[string]any, error) {
	return b.wireParamsPrompt(req, nil)
}
func (b *Backend) wireParamsPrompt(req backend.Request, measure *backend.PromptMeasure) (string, map[string]any, error) {
	model := req.Model
	if model == "" {
		model = b.model
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = b.maxTokens
	}
	if maxTok > 2147483647 || maxTok < 0 {
		return "", nil, fmt.Errorf("google: max_tokens exceeds int32")
	}
	rf := req.ResponseFormat
	if rf == "" {
		rf = b.responseFormat
	}
	switch rf {
	case respFmtJSONSchema, respFmtJSONObject, respFmtText, respFmtNone, "":
	default:
		return "", nil, fmt.Errorf("google: unknown response_format")
	}
	suffix := ""
	if rf == respFmtJSONObject {
		suffix = "\n\nRespond with a single valid JSON object and nothing else."
	}
	system, user, e := backend.PromptValues(req, measure, suffix)
	if e != nil {
		return "", nil, e
	}
	config := map[string]any{}
	if req.Temperature != nil {
		config["temperature"] = float32(*req.Temperature)
	} else if b.temperature != nil {
		config["temperature"] = float32(*b.temperature)
	}
	if req.TopP != nil {
		config["topP"] = float32(*req.TopP)
	} else if b.topP != nil {
		config["topP"] = float32(*b.topP)
	}
	if maxTok > 0 {
		config["maxOutputTokens"] = maxTok
	}
	if rf == respFmtJSONSchema || rf == respFmtJSONObject {
		config["responseMimeType"] = "application/json"
	}
	if rf == respFmtJSONSchema && req.JSONSchema != nil {
		config["responseJsonSchema"] = req.JSONSchema
	}
	if b.thinking.Set {
		if b.thinking.Level == backend.ThinkingOff {
			config["thinkingConfig"] = map[string]any{"thinkingBudget": 0}
		} else {
			config["thinkingConfig"] = map[string]any{"thinkingLevel": string(toGoogleThinkingLevel(b.thinking.Level))}
		}
	}
	p := map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": system}}, "role": "user"}, "contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": user}}}}, "generationConfig": config}
	return model, p, nil
}
func (b *Backend) MeasureRequest(req backend.Request) (int64, error) {
	return backend.MeasureWireBuild(func() (any, error) { _, p, e := b.wireParams(req); return p, e })
}
func (b *Backend) MeasureRequestPrompt(req backend.Request, m backend.PromptMeasure) (int64, error) {
	return backend.MeasureWireBuild(func() (any, error) { _, p, e := b.wireParamsPrompt(req, &m); return p, e })
}

type generateWire struct {
	Error *struct {
		Code int `json:"code"`
	} `json:"error"`
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	Usage struct {
		PromptTokens     int64 `json:"promptTokenCount"`
		CompletionTokens int64 `json:"candidatesTokenCount"`
		TotalTokens      int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	var model string
	data, err := backend.EncodeWireBuild(ctx, func() (any, error) { m, p, e := b.wireParams(req); model = m; return p, e })
	if err != nil {
		return nil, err
	}
	inputTrace := backend.NewOwnedTrace(ctx, "wire", "google-input")
	inputTrace.Set(int64(cap(data)))
	defer inputTrace.Clear()
	ctx, cancel := backend.ProviderCallContext(ctx, b.timeout)
	defer cancel()
	base := b.baseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/"
	}
	base = strings.TrimRight(base, "/")
	method := "generateContent"
	query := ""
	if b.stream {
		method = "streamGenerateContent"
		query = "?alt=sse"
	}
	modelPath := strings.TrimPrefix(model, "models/")
	if int64(len(modelPath)) > (math.MaxInt64-int64(len(base))-64)/3 {
		return nil, &backend.BuildCapacityError{Resource: "endpoint arithmetic"}
	}
	pathBudget, e := backend.NewBuildBounds(backend.RequestCreditsFromContext(ctx).InputBytes, int64(len(base)))
	if e != nil {
		return nil, e
	}
	if e = pathBudget.ReserveText(0, 3*int64(len(modelPath))); e != nil {
		return nil, e
	}
	if e = pathBudget.ReserveText(0, int64(len(base))+3*int64(len(modelPath))+64); e != nil {
		return nil, e
	}
	endpoint := base + "/v1beta/models/" + url.PathEscape(modelPath) + ":" + method + query
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-goog-api-key", b.apiKey)
	httpReq.Header.Set("User-Agent", backend.ClientUserAgent())
	httpReq.Header.Set("X-Client-Name", backend.ClientName())
	httpReq.Header.Set("X-Client-Version", backend.ClientVersion())
	client := b.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := backend.GuardProviderErrors(httpReq, client.Do)
	if err != nil {
		return nil, wrapGoogleError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, backend.WrapHTTPError("google generate", errors.New(http.StatusText(resp.StatusCode)), resp.StatusCode, resp.Header)
	}
	limit := backend.ResponseBudget(ctx, b.responseBytes)
	text, e := backend.NewResponseTextObserved(ctx, limit, "google-text")
	if e != nil {
		return nil, e
	}
	defer text.ClearObserved()
	var usage backend.Usage
	finish := ""
	consume := func(event []byte) error {
		return backend.RunBuild(ctx, func() error {
			var chunk generateWire
			if e := backend.DecodeWire(event, limit, &chunk); e != nil {
				return e
			}
			if chunk.Error != nil {
				return backend.WrapHTTPError("google stream", errors.New("provider stream error"), chunk.Error.Code, resp.Header)
			}
			if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 || chunk.Usage.TotalTokens != 0 {
				usage = backend.Usage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
			}
			if len(chunk.Candidates) > 0 {
				c := chunk.Candidates[0]
				if c.FinishReason != "" {
					finish = c.FinishReason
				}
				for _, part := range c.Content.Parts {
					if !part.Thought {
						if e := text.Append(part.Text); e != nil {
							return e
						}
					}
				}
			}
			return nil
		})
	}
	if b.stream {
		err = backend.ReadSSEContext(ctx, resp.Body, limit, consume)
	} else {
		var raw []byte
		readTrace := backend.NewOwnedTrace(ctx, "wire", "google-http")
		defer readTrace.Clear()
		raw, err = backend.ReadBoundedObserved(resp.Body, limit, readTrace)
		if err == nil {
			err = consume(raw)
		}
	}
	if err != nil {
		return nil, err
	}
	out, e := backend.FinishResponseText(ctx, text)
	if e != nil {
		return nil, e
	}
	if out == "" {
		return nil, &backend.EmptyResponseError{BackendName: b.Name(), Model: model, FinishReason: finish, PromptTokens: usage.PromptTokens}
	}
	return &backend.Response{Text: out, Usage: usage, Truncated: finish == "MAX_TOKENS"}, nil
}
