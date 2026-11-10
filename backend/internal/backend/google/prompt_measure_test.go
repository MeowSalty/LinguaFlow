package google

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"testing"
)

func TestBoundedMeasuredPromptMatchesRealWire(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, format := range []string{"text", "none", "json_object", "json_schema"} {
			b := &Backend{model: "default", stream: stream, responseFormat: format}
			temperature, topP := .25, .7
			req := backend.Request{Model: "override", System: "<system>\n海\"\\", User: "{\"x\":\"<&>海\"}", MaxTokens: 4096, Temperature: &temperature, TopP: &topP, JSONSchema: map[string]any{"type": "object", "required": []string{"x"}, "properties": map[string]any{"x": map[string]any{"type": "string"}}, "additionalProperties": false}}
			system, _ := backend.MeasureJSONString(req.System)
			user, _ := backend.MeasureJSONString(req.User)
			m := backend.PromptMeasure{SystemBytes: int64(len(req.System)), UserBytes: int64(len(req.User)), SystemJSONBytes: system, UserJSONBytes: user}
			actual, e := b.MeasureRequest(req)
			if e != nil {
				t.Fatal(e)
			}
			req.System, req.User = "", ""
			got, e := b.MeasureRequestPrompt(req, m)
			if e != nil || got != actual {
				t.Fatalf("format=%s stream=%v got=%d want=%d err=%v", format, stream, got, actual, e)
			}
		}
	}
}
