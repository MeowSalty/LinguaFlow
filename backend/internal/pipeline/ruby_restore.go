package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// restoreSegmentRuby 对单个段落执行注音还原：过滤 → 还原 → 失败则 LLM 对齐重试。
func restoreSegmentRuby(
	ctx context.Context,
	seg *Segment,
	restorer *protect.RubyRestorer,
	keepSet map[string]bool,
	backends []backend.Backend,
	retryPolicy backend.RetryPolicy,
	logger *slog.Logger,
	reporter progress.Reporter,
) {
	rubyOutput := extractRubyOutputFromSeg(seg)
	originals := extractRubyAnnotationsFromSeg(seg)

	if len(rubyOutput) > 0 {
		filtered := filterByKinds(rubyOutput, keepSet)
		if len(filtered) == 0 {
			return
		}
		before := seg.Target
		if err := restorer.Restore(seg, filtered, originals); err != nil {
			logger.Warn("ruby restore failed, will retry alignment", "seg", seg.ID, "err", err)
		} else if seg.Target == before {
			logger.Warn("ruby restore: no base matched", "seg", seg.ID)
		} else {
			return
		}
		// 还原失败，尝试 LLM 对齐重试
		if len(backends) > 0 && ctx.Err() == nil {
			retryAlignSegment(ctx, seg, originals, restorer, keepSet, backends, retryPolicy, logger, reporter)
		}
		return
	}

	if len(originals) > 0 && len(backends) > 0 && ctx.Err() == nil {
		retryAlignSegment(ctx, seg, originals, restorer, keepSet, backends, retryPolicy, logger, reporter)
	}
}

// retryAlignSegment 对单个段落执行 LLM 注音对齐重试：LLM 分类 → 过滤 → 还原。
func retryAlignSegment(
	ctx context.Context,
	seg *Segment,
	originals []protect.RubyAnnotation,
	restorer *protect.RubyRestorer,
	keepSet map[string]bool,
	backends []backend.Backend,
	retryPolicy backend.RetryPolicy,
	logger *slog.Logger,
	reporter progress.Reporter,
) {
	if len(originals) == 0 {
		return
	}

	sys, user, schema := buildAlignmentPrompt(seg, originals)
	req := backend.Request{
		System:     sys,
		User:       user,
		JSONSchema: schema,
	}

	start := time.Now()
	var resp *backend.Response
	var callErr error
	var triedBackends []string
	for _, b := range backends {
		triedBackends = append(triedBackends, b.Name())
		resp, callErr = callRubyBackend(ctx, b, req, retryPolicy)
		if callErr == nil {
			break
		}
		logger.Warn("ruby alignment call failed, trying next backend",
			"seg", seg.ID, "backend", b.Name(), "err", callErr)
	}
	durationMs := time.Since(start).Milliseconds()

	status := "failed"
	errorType := "backend_error"
	errorMsg := ""
	receivedContent := ""

	if callErr != nil {
		errorMsg = callErr.Error()
		logger.Warn("ruby alignment retry exhausted all backends",
			"seg", seg.ID, "err", callErr)
	} else {
		receivedContent = resp.Text
		newOutput := parseAlignmentResponse(resp.Text)
		if len(newOutput) == 0 {
			status = "partial"
			errorType = "empty_output"
			logger.Warn("ruby alignment: empty output", "seg", seg.ID, "resp_head", headSnippet(resp.Text, 200))
		} else {
			if seg.Meta == nil {
				seg.Meta = make(map[string]any)
			}
			seg.Meta["ruby_output"] = newOutput

			filtered := filterByKinds(newOutput, keepSet)
			if len(filtered) == 0 {
				status = "partial"
				errorType = "filtered_out"
				logger.Info("alignment output filtered out by preserve_kinds", "seg", seg.ID)
			} else {
				before := seg.Target
				if err := restorer.Restore(seg, filtered, originals); err != nil {
					status = "partial"
					errorType = "restore_error"
					errorMsg = err.Error()
					logger.Warn("ruby restore after alignment retry failed",
						"seg", seg.ID, "err", err)
				} else if seg.Target == before {
					status = "partial"
					errorType = "no_match"
					logger.Warn("ruby restore after alignment retry: still no match",
						"seg", seg.ID)
				} else {
					status = "success"
					errorType = ""
					logger.Info("ruby restore succeeded after alignment retry",
						"seg", seg.ID)
				}
			}
		}
	}

	emitRubyAlignmentEvent(reporter, seg, triedBackends, resp, status, errorType,
		errorMsg, durationMs, user, receivedContent)
}

// callRubyBackend 调用后端并重试。
func callRubyBackend(ctx context.Context, b backend.Backend, req backend.Request, retryPolicy backend.RetryPolicy) (*backend.Response, error) {
	var resp *backend.Response
	err := backend.WithRetry(ctx, retryPolicy, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	return resp, err
}

// emitRubyAlignmentEvent 发送注音对齐 SSE 事件。
func emitRubyAlignmentEvent(
	reporter progress.Reporter,
	seg *Segment,
	triedBackends []string,
	resp *backend.Response,
	status, errorType, errorMsg string,
	durationMs int64,
	sentContent, receivedContent string,
) {
	if reporter == nil {
		return
	}
	obs, ok := reporter.(progress.BatchObserver)
	if !ok {
		return
	}

	var inputTokens, outputTokens int64
	var backendName string
	if resp != nil {
		inputTokens = resp.Usage.PromptTokens
		outputTokens = resp.Usage.CompletionTokens
	}
	if len(triedBackends) > 0 {
		backendName = triedBackends[len(triedBackends)-1]
	}

	evt := progress.BatchEvent{
		Stage:           "ruby_alignment",
		BatchIndex:      0,
		SegmentIDs:      []string{seg.ID},
		SegmentCount:    1,
		BackendName:     backendName,
		Status:          status,
		DurationMs:      durationMs,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		SentContent:     sentContent,
		ReceivedContent: receivedContent,
		ErrorType:       errorType,
		ErrorMessage:    errorMsg,
		TriedBackends:   triedBackends,
	}
	obs.OnBatchEvent(evt)
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
- "text" 是标注文本：phonetic/semantic 保留原文（不翻译），creative 需要翻译。
- "kind" 是注音分类：
  · phonetic（音注）：纯读音标注。
  · semantic（义训）：语义解释标注，基底与标注语意一致或相近。
  · creative（创意注音）：基底与标注存在语义落差。
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
		Kind string `json:"kind"`
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
						"kind": map[string]any{
							"type": "string",
							"enum": []string{"phonetic", "semantic", "creative"},
						},
					},
					"required":             []string{"base", "text", "kind"},
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

// kindSet 将 kind 列表转为 set，用于快速查找。
// nil（旧记录/未设置）时返回默认全集，保证向后兼容；
// 空非 nil 切片（用户显式传 []）返回空集，允许用户选择不保留任何注音。
func kindSet(kinds []string) map[string]bool {
	if kinds == nil {
		return map[string]bool{"phonetic": true, "semantic": true, "creative": true}
	}
	s := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		s[k] = true
	}
	return s
}

// filterByKinds 按 preserve_kinds 过滤注音条目。
// Kind 为空字符串的条目视为未分类，保留不过滤（向后兼容旧数据）。
func filterByKinds(output []protect.RubyOutputEntry, keep map[string]bool) []protect.RubyOutputEntry {
	var result []protect.RubyOutputEntry
	for _, entry := range output {
		if entry.Kind == "" || keep[entry.Kind] {
			result = append(result, entry)
		}
	}
	return result
}
