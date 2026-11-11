package anthropic

import (
	"bytes"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func extractResponseText(msg *messageWire, tools bool, b *Backend, finish string, inputTokens int64) (string, error) {
	limit := b.responseBytes
	if limit <= 0 {
		limit = backend.DefaultMaxResponseBytes
	}
	text, e := backend.NewResponseText(limit)
	if e != nil {
		return "", e
	}
	if tools {
		for _, block := range msg.Content {
			if block.Type == "tool_use" && block.Name == toolName {
				if len(block.Input) == 0 || bytes.Equal(block.Input, []byte("null")) {
					return "", &backend.EmptyResponseError{BackendName: b.Name(), Model: b.model, FinishReason: finish, PromptTokens: inputTokens}
				}
				if e := text.AppendBytes(block.Input); e != nil {
					return "", e
				}
				return text.String()
			}
		}
	}
	for _, block := range msg.Content {
		if block.Type == "text" {
			if e = text.Append(block.Text); e != nil {
				return "", e
			}
		}
	}
	out, e := text.String()
	if e != nil {
		return "", e
	}
	if out == "" {
		return "", &backend.EmptyResponseError{BackendName: b.Name(), Model: b.model, FinishReason: finish, PromptTokens: inputTokens}
	}
	return out, nil
}
func (b *Backend) responseFromMessage(msg *messageWire, tools bool) (*backend.Response, error) {
	text, e := extractResponseText(msg, tools, b, msg.StopReason, msg.Usage.InputTokens)
	if e != nil {
		return nil, e
	}
	return &backend.Response{Text: text, Usage: backend.Usage{PromptTokens: msg.Usage.InputTokens, CompletionTokens: msg.Usage.OutputTokens, TotalTokens: msg.Usage.InputTokens + msg.Usage.OutputTokens}, Truncated: msg.StopReason == "max_tokens"}, nil
}
