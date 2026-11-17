package google

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func (b *Backend) wireParams(req backend.Request) (string, map[string]any, error) {
	return b.wireParamsPrompt(req, nil)
}
func (b *Backend) wireParamsPrompt(req backend.Request, measure *backend.PromptMeasure) (string, map[string]any, error) {
	bounds, e := backend.NewBuildBounds(0, backend.DefaultMaxInputBytes)
	if e != nil {
		return "", nil, e
	}
	return b.wireParamsBounded(req, measure, bounds)
}
func (b *Backend) wireParamsBounded(req backend.Request, measure *backend.PromptMeasure, bounds *backend.BuildBounds) (string, map[string]any, error) {
	if e := backend.ReserveSlice[any](bounds, 3); e != nil {
		return "", nil, e
	}
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
	return b.MeasureRequestContext(context.Background(), req)
}
func (b *Backend) MeasureRequestContext(ctx context.Context, req backend.Request) (int64, error) {
	return backend.MeasureWireBuildContext(ctx, func(bounds *backend.BuildBounds) (any, error) {
		_, p, e := b.wireParamsBounded(req, nil, bounds)
		return p, e
	})
}
func (b *Backend) MeasureRequestPrompt(req backend.Request, m backend.PromptMeasure) (int64, error) {
	return b.MeasureRequestPromptContext(context.Background(), req, m)
}
func (b *Backend) MeasureRequestPromptContext(ctx context.Context, req backend.Request, m backend.PromptMeasure) (int64, error) {
	return backend.MeasureWireBuildContext(ctx, func(bounds *backend.BuildBounds) (any, error) {
		_, p, e := b.wireParamsBounded(req, &m, bounds)
		return p, e
	})
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

// endpoint builds one explicitly sized, immutable byte backing. The URL
// parser's private buffers are not observable and are not predicted here.
func (b *Backend) endpoint(ctx context.Context, model string) (string, *backend.OwnedAllocation, error) {
	model = strings.TrimPrefix(model, "models/")
	base := strings.TrimRight(b.baseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	method := "generateContent"
	query := ""
	if b.stream {
		method, query = "streamGenerateContent", "?alt=sse"
	}
	allowed := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~$&+:=@", rune(c))
	}
	n := int64(len(base))
	var err error
	for _, part := range []string{"/v1beta/models/", ":", method, query} {
		n, err = backend.CheckedAdd(n, int64(len(part)))
		if err != nil {
			return "", nil, err
		}
	}
	for i := range len(model) {
		unit := int64(3)
		if allowed(model[i]) {
			unit = 1
		}
		n, err = backend.CheckedAdd(n, unit)
		if err != nil {
			return "", nil, err
		}
	}
	owner, err := backend.OwnProviderResponseAllocation(ctx, n, 1)
	if err != nil {
		return "", nil, err
	}
	data := make([]byte, int(n))
	pos := copy(data, base)
	pos += copy(data[pos:], "/v1beta/models/")
	const hex = "0123456789ABCDEF"
	for i := range len(model) {
		c := model[i]
		if allowed(c) {
			data[pos] = c
			pos++
		} else {
			data[pos], data[pos+1], data[pos+2] = '%', hex[c>>4], hex[c&15]
			pos += 3
		}
	}
	data[pos] = ':'
	pos++
	pos += copy(data[pos:], method)
	copy(data[pos:], query)
	// data is never mutated again; the string borrows precisely this backing.
	return unsafe.String(unsafe.SliceData(data), len(data)), owner, nil
}

func (b *Backend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	var model string
	data, releaseInput, err := backend.EncodeWireBuild(ctx, func(bounds *backend.BuildBounds) (any, error) {
		m, p, e := b.wireParamsBounded(req, nil, bounds)
		model = m
		return p, e
	})
	if err != nil {
		return nil, err
	}
	defer func() { data = nil; releaseInput() }()
	inputTrace := backend.NewOwnedTrace(ctx, "wire", "google-input")
	inputTrace.Set(int64(cap(data)))
	defer inputTrace.Clear()
	limit := backend.ResponseBudget(ctx, b.responseBytes)
	ctx, err = backend.WithProviderResponseAllocation(ctx, limit)
	if err != nil {
		return nil, err
	}
	ctx, cancel := backend.ProviderCallContext(ctx, b.timeout)
	defer cancel()
	endpoint, endpointOwner, e := b.endpoint(ctx, model)
	if e != nil {
		return nil, e
	}
	defer endpointOwner.Release()
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
	text, e := backend.NewResponseTextObserved(ctx, limit, "google-text")
	if e != nil {
		return nil, e
	}
	defer text.ClearObserved()
	var usage backend.Usage
	var finish backend.WireStringSlot
	defer finish.Close()
	consume := func(event []byte) error {
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
				code, e := failure.Field("code")
				if e != nil {
					return e
				}
				status, e := code.Int()
				if e != nil {
					return e
				}
				return backend.WrapHTTPError("google stream", errors.New("provider stream error"), status, resp.Header)
			}
			var u backend.Usage
			for _, field := range []struct {
				name  string
				value *int64
			}{{"promptTokenCount", &u.PromptTokens}, {"candidatesTokenCount", &u.CompletionTokens}, {"totalTokenCount", &u.TotalTokens}} {
				v, e := chunk.Path("usageMetadata", field.name)
				if e != nil {
					return e
				}
				if e = v.DecodeScalar(field.value); e != nil {
					return e
				}
			}
			if u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 {
				usage = u
			}
			candidates, e := chunk.Field("candidates")
			if e != nil {
				return e
			}
			candidate, e := candidates.First()
			if e != nil {
				return e
			}
			if candidate.IsNull() {
				return nil
			}
			f, e := candidate.Field("finishReason")
			if e != nil {
				return e
			}
			if !f.IsNull() && !f.StringEquals("") {
				if e = finish.Set(f); e != nil {
					return e
				}
			}
			parts, e := candidate.Path("content", "parts")
			if e != nil {
				return e
			}
			return parts.EachArray(func(part backend.WireJSONValue) error {
				thought, e := part.Field("thought")
				if e != nil {
					return e
				}
				skip, e := thought.Bool()
				if e != nil {
					return e
				}
				if skip {
					return nil
				}
				content, e := part.Field("text")
				if e != nil {
					return e
				}
				return content.AppendString(text)
			})
		})
	}
	if b.stream {
		err = backend.ReadSSEContext(ctx, resp.Body, limit, consume)
	} else {
		readTrace := backend.NewOwnedTrace(ctx, "wire", "google-http")
		defer readTrace.Clear()
		var release func()
		var raw []byte
		raw, release, err = backend.ReadProviderBounded(ctx, resp.Body, limit, readTrace)
		defer release()
		if err == nil {
			err = consume(raw)
		}
	}
	if err != nil {
		return backend.ResponseWithPartialText(text, usage), err
	}
	out, e := backend.FinishResponseText(ctx, text)
	if e != nil {
		return backend.ResponseWithPartialText(text, usage), e
	}
	if out == "" {
		return nil, &backend.EmptyResponseError{BackendName: b.Name(), Model: model, FinishReason: backend.DiagnosticFinishReason(finish.Value), PromptTokens: usage.PromptTokens}
	}
	return &backend.Response{Text: out, Usage: usage, Truncated: finish.Value == "MAX_TOKENS"}, nil
}
