package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// RubyRestore 在 Unprotect 之后执行，将 LLM 输出的注音还原为 <ruby> 标签。
// 当首轮还原失败时，可选择使用 LLM 注音对齐进行重试。
type RubyRestore struct {
	Restorer         *protect.RubyRestorer
	Logger           *slog.Logger
	Backends         []backend.Backend
	Retry            backend.RetryPolicy
	RubyOutputFormat string
}

// NewRubyRestore 创建 RubyRestore stage 实例。
func NewRubyRestore(restorer *protect.RubyRestorer, logger *slog.Logger, backends []backend.Backend, retry backend.RetryPolicy, rubyOutputFormat string) *RubyRestore {
	return &RubyRestore{
		Restorer:         restorer,
		Logger:           logger,
		Backends:         backends,
		Retry:            retry,
		RubyOutputFormat: rubyOutputFormat,
	}
}

func (*RubyRestore) Name() string { return "ruby_restore" }

func (s *RubyRestore) Run(ctx context.Context, doc *Document) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// 第一轮：用已有的 ruby_output 还原（含双源匹配回退）
	var failedSegs []*Segment
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		rubyOutput := extractRubyOutputFromSeg(seg)
		if len(rubyOutput) > 0 {
			originals := extractRubyAnnotationsFromSeg(seg)
			before := seg.Target
			if err := s.Restorer.Restore(seg, rubyOutput, originals); err != nil {
				logger.Warn("ruby restore failed, keeping translated text as-is",
					"seg", seg.ID, "err", err)
				failedSegs = append(failedSegs, seg)
			} else if seg.Target == before {
				logger.Warn("ruby restore: no base text matched in translation, annotations lost",
					"seg", seg.ID, "entries", len(rubyOutput))
				failedSegs = append(failedSegs, seg)
			}
		}
	}

	// 第二轮：对失败段使用 LLM 注音对齐重试
	if len(failedSegs) > 0 && s.RubyOutputFormat == "ruby_output" && len(s.Backends) > 0 {
		retried, recovered := s.retryFailedSegments(ctx, failedSegs, logger)
		if retried > 0 {
			logger.Info("ruby alignment retry completed",
				"retried", retried, "recovered", recovered)
		}
	}

	return nil
}

// retryFailedSegments 对注音还原失败的段落执行 LLM 注音对齐重试。
// 返回 (尝试数, 成功数)。
func (s *RubyRestore) retryFailedSegments(ctx context.Context, failedSegs []*Segment, logger *slog.Logger) (int, int) {
	retried := 0
	recovered := 0

	for _, seg := range failedSegs {
		if ctx.Err() != nil {
			break
		}

		originals := extractRubyAnnotationsFromSeg(seg)
		if len(originals) == 0 {
			continue
		}

		retried++

		// 构建注音对齐 prompt
		sys, user, schema := buildAlignmentPrompt(seg, originals)
		req := backend.Request{
			System:     sys,
			User:       user,
			JSONSchema: schema,
		}

		// 依次尝试各后端
		var resp *backend.Response
		var callErr error
		for _, b := range s.Backends {
			resp, callErr = s.callBackend(ctx, b, req)
			if callErr == nil {
				break
			}
			logger.Warn("ruby alignment call failed, trying next backend",
				"seg", seg.ID, "backend", b.Name(), "err", callErr)
		}
		if callErr != nil {
			logger.Warn("ruby alignment retry exhausted all backends",
				"seg", seg.ID, "err", callErr)
			continue
		}

		// 解析响应
		newOutput := parseAlignmentResponse(resp.Text)
		if len(newOutput) == 0 {
			logger.Warn("ruby alignment: empty output", "seg", seg.ID)
			continue
		}

		// 更新 seg.Meta 中的 ruby_output
		if seg.Meta == nil {
			seg.Meta = make(map[string]any)
		}
		seg.Meta["ruby_output"] = newOutput

		// 第二轮还原
		before := seg.Target
		if err := s.Restorer.Restore(seg, newOutput, originals); err != nil {
			logger.Warn("ruby restore after alignment retry failed",
				"seg", seg.ID, "err", err)
		} else if seg.Target == before {
			logger.Warn("ruby restore after alignment retry: still no match",
				"seg", seg.ID)
		} else {
			logger.Info("ruby restore succeeded after alignment retry",
				"seg", seg.ID)
			recovered++
		}
	}

	return retried, recovered
}

// callBackend 调用后端并重试。
func (s *RubyRestore) callBackend(ctx context.Context, b backend.Backend, req backend.Request) (*backend.Response, error) {
	var resp *backend.Response
	err := backend.WithRetry(ctx, s.Retry, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	return resp, err
}

// extractRubyOutputFromSeg 从 Segment.Meta 中提取 ruby_output。
func extractRubyOutputFromSeg(seg *Segment) []protect.RubyOutputEntry {
	raw, ok := seg.Meta["ruby_output"]
	if !ok {
		return nil
	}
	entries, ok := raw.([]protect.RubyOutputEntry)
	if !ok {
		return nil
	}
	return entries
}

// extractRubyAnnotationsFromSeg 从 Segment.Meta 中提取 ruby_annotations。
func extractRubyAnnotationsFromSeg(seg *Segment) []protect.RubyAnnotation {
	raw, ok := seg.Meta["ruby_annotations"]
	if !ok {
		return nil
	}
	annots, ok := raw.([]protect.RubyAnnotation)
	if !ok {
		return nil
	}
	return annots
}

// rubyTagRe 用于从 OriginalSource 中剥离 ruby 标签。
var rubyTagRe = regexp.MustCompile(`<ruby>(.*?)<rt>(.*?)</rt>(.*?)</ruby>`)

// buildAlignmentPrompt 构建注音对齐的 system/user 消息和 JSON Schema。
func buildAlignmentPrompt(seg *Segment, originals []protect.RubyAnnotation) (string, string, map[string]any) {
	sys := `你是注音对齐工具。给定原文、译文和注音元数据，确定每个注音条目在译文中对应的文本。

规则：
- "base" 必须是译文中实际出现的文本（不是原文基底），专有名词等未翻译的词除外。
- "text" 保留原文读音（不翻译）。
- 条目顺序与输入 annotations 顺序一致。
- 仅输出 JSON，无额外文字。`

	// 取原文（优先 OriginalSource，去掉 ruby 标签）
	source := seg.OriginalSource
	if source == "" {
		source = seg.Source
	}
	source = stripRubyTagsForAlignment(source)

	type annIn struct {
		Base string `json:"base"`
		Text string `json:"text"`
	}
	anns := make([]annIn, len(originals))
	for i, a := range originals {
		anns[i] = annIn{Base: a.Base, Text: a.Text}
	}

	userMsg := struct {
		Source      string  `json:"source"`
		Translation string  `json:"translation"`
		Annotations []annIn `json:"annotations"`
	}{
		Source:      source,
		Translation: seg.Target,
		Annotations: anns,
	}
	userBytes, _ := json.Marshal(userMsg)

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ruby_output": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"base": map[string]any{"type": "string"},
						"text": map[string]any{"type": "string"},
					},
					"required":             []string{"base", "text"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"ruby_output"},
		"additionalProperties": false,
	}

	return sys, string(userBytes), schema
}

// stripRubyTagsForAlignment 剥离 ruby 标签，只保留基底文本和尾部文本。
func stripRubyTagsForAlignment(s string) string {
	return rubyTagRe.ReplaceAllStringFunc(s, func(match string) string {
		m := rubyTagRe.FindStringSubmatch(match)
		return m[1] + m[3]
	})
}

// parseAlignmentResponse 从 LLM 响应中解析 ruby_output。
func parseAlignmentResponse(text string) []protect.RubyOutputEntry {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil
	}
	var resp struct {
		RubyOutput []protect.RubyOutputEntry `json:"ruby_output"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil
	}
	return resp.RubyOutput
}
