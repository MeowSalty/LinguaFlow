package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// restoreSegmentRuby 对单个段落执行注音还原：提取统一 items → 过滤 → 还原，
// 未对齐条目存在且配置了重试后端时执行定向对齐重试（仅发未对齐条目）。
func restoreSegmentRuby(
	ctx context.Context,
	seg *Segment,
	restorer *ruby.Restorer,
	keepSet map[string]bool,
	backends []backend.Backend,
	retryPolicy backend.RetryPolicy,
	logger *slog.Logger,
	reporter progress.Reporter,
	isTextMode bool,
	roundIndex int,
	repairOpt repair.Options,
	rubyRetryAttempts int,
) {
	items := extractRubyItemsFromSeg(seg)
	if len(items) == 0 {
		return
	}

	// inline：marker 位置天然自洽，按标记就地组装，不参与 id 关联与定向重试
	if strings.Contains(seg.Target, "⟦ruby:") {
		markers := ruby.ParseInlineMarkers(seg.Target)
		filtered := filterByKinds(markers, keepSet)
		restorer.RestoreInlineMarkers(seg, filtered)
		return
	}

	translation := seg.Target // 干净译文快照（未插标签）
	filtered := filterItemsByKind(items, keepSet)
	if len(filtered) > 0 {
		result, err := restorer.RestoreItems(seg, filtered)
		if err != nil {
			logger.Warn("ruby restore failed, will retry alignment", "seg", seg.ID, "err", err)
		} else if result.IsFull() && len(ruby.Unaligned(items)) == 0 {
			return
		}
		// 回退到干净译文：定向重试的 prompt 必须看到未插标签的译文
		seg.Target = translation
	}

	attempts := rubyRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	if len(backends) > 0 && ctx.Err() == nil && attempts > 0 && len(ruby.Unaligned(items)) > 0 {
		retryAlignSegmentDirected(ctx, seg, items, translation, restorer, keepSet, backends,
			retryPolicy, logger, reporter, isTextMode, roundIndex, repairOpt, attempts)
	}

	// 最终还原：以最后一次合并后的 items 为准
	filtered = filterItemsByKind(items, keepSet)
	if len(filtered) > 0 {
		restorer.RestoreItems(seg, filtered)
	}
}

// extractRubyItemsFromSeg 从 Segment.Meta 提取统一注音条目。
// 优先 seg.Meta["ruby_items"]（[]ruby.Item，主路径）；
// 回退旧 key seg.Meta["ruby_output"]（[]ruby.OutputEntry，测试/兼容，条目视为已对齐）。
func extractRubyItemsFromSeg(seg *Segment) []ruby.Item {
	if raw, ok := seg.Meta["ruby_items"]; ok {
		if items, ok := raw.([]ruby.Item); ok {
			return items
		}
	}
	if raw, ok := seg.Meta["ruby_output"]; ok {
		if entries, ok := raw.([]ruby.OutputEntry); ok {
			items := make([]ruby.Item, len(entries))
			for i, e := range entries {
				items[i] = ruby.Item{
					ID:         e.ID,
					TargetBase: e.Base,
					TargetText: e.Text,
					Kind:       e.Kind,
					Aligned:    true,
				}
			}
			return items
		}
	}
	return nil
}

// filterItemsByKind 按 preserve_kinds 过滤统一 items（filterByKinds 的 Item 版本）。
// Kind 为空字符串的条目视为未分类，保留不过滤（向后兼容旧数据）。
func filterItemsByKind(items []ruby.Item, keep map[string]bool) []ruby.Item {
	var result []ruby.Item
	for _, it := range items {
		if it.Kind == "" || keep[it.Kind] {
			result = append(result, it)
		}
	}
	return result
}

// retryAlignSegmentDirected 对单个段落执行定向对齐重试：每轮只下发仍未对齐的条目
// （{id, source_base, source_text}），用 LLM 返回的 OutputEntry 按 id 合并回 items。
// 一轮无新增对齐（含空输出/后端全失败）即提前停，避免空转。
func retryAlignSegmentDirected(
	ctx context.Context,
	seg *Segment,
	items []ruby.Item,
	translation string,
	restorer *ruby.Restorer,
	keepSet map[string]bool,
	backends []backend.Backend,
	retryPolicy backend.RetryPolicy,
	logger *slog.Logger,
	reporter progress.Reporter,
	isTextMode bool,
	roundIndex int,
	repairOpt repair.Options,
	attempts int,
) {
	for attempt := 0; attempt < attempts; attempt++ {
		missing := ruby.Unaligned(items)
		if len(missing) == 0 {
			return
		}

		var sys, user string
		var schema map[string]any
		if isTextMode {
			sys, user = buildDirectedAlignmentPromptText(seg, missing, translation)
		} else {
			sys, user, schema = buildDirectedAlignmentPrompt(seg, missing, translation)
		}
		req := backend.Request{
			System:     sys,
			User:       user,
			JSONSchema: schema,
		}
		if isTextMode {
			req.ResponseFormat = "none"
		}

		var resp *backend.Response
		var callErr error
		var triedBackends []string
		attemptMs := int64(0)
		for _, b := range backends {
			triedBackends = append(triedBackends, b.Name())
			callStart := time.Now()
			resp, callErr = callRubyBackend(ctx, b, req, retryPolicy)
			attemptMs = time.Since(callStart).Milliseconds()
			if callErr != nil {
				emitRubyAlignmentBatchEvent(reporter, seg, b.Name(), append([]string(nil), triedBackends...),
					"failed", "backend_error", callErr.Error(), attemptMs, 0, 0, user, "",
					rubyHTTPStatusFromErr(callErr), roundIndex, attempt, sys, user, req.ResponseFormat, req.JSONSchema)
				logger.Warn("ruby alignment call failed, trying next backend",
					"seg", seg.ID, "backend", b.Name(), "err", callErr)
				resp = nil
				continue
			}
			break
		}
		if callErr != nil && len(triedBackends) > 0 {
			logger.Warn("ruby alignment directed retry exhausted all backends",
				"seg", seg.ID, "err", callErr)
			return
		}

		var newOutput []ruby.OutputEntry
		if isTextMode {
			newOutput = parseAlignmentResponseText(resp.Text, len(missing))
		} else {
			newOutput, _ = parseAlignmentResponse(resp.Text, repairOpt)
		}

		before := len(ruby.Unaligned(items))
		if len(newOutput) > 0 {
			ruby.MergeByOutput(items, newOutput)
		}
		after := len(ruby.Unaligned(items))

		status := "success"
		errorType := ""
		errorMsg := ""
		if len(newOutput) == 0 {
			status = "partial"
			errorType = "empty_output"
			logger.Warn("ruby alignment directed retry: empty output",
				"seg", seg.ID, "resp_head", headSnippet(resp.Text, 200))
		} else if after >= before {
			status = "partial"
			errorType = "no_match"
			logger.Warn("ruby alignment directed retry: no new alignment",
				"seg", seg.ID, "before", before, "after", after)
		} else if after == 0 {
			logger.Info("ruby alignment directed retry succeeded", "seg", seg.ID)
		} else {
			status = "partial"
			errorType = "partial_match"
			logger.Warn("ruby alignment directed retry: partial match",
				"seg", seg.ID, "before", before, "after", after)
		}

		emitRubyAlignmentBatchEvent(reporter, seg, triedBackends[len(triedBackends)-1],
			append([]string(nil), triedBackends...),
			status, errorType, errorMsg, attemptMs,
			resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
			user, resp.Text, 0, roundIndex, attempt, sys, user, req.ResponseFormat, req.JSONSchema)

		if after >= before || after == 0 {
			return
		}
	}
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

func rubyHTTPStatusFromErr(err error) int {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		return hsErr.HTTPStatus()
	}
	return 0
}

func emitRubyAlignmentBatchEvent(
	reporter progress.Reporter,
	seg *Segment,
	backendName string,
	triedBackends []string,
	status, errorType, errorMsg string,
	durationMs int64,
	inputTokens, outputTokens int64,
	sentContent, receivedContent string,
	httpStatus int,
	roundIndex int,
	attempt int,
	sys string,
	usr string,
	responseFormat string,
	jsonSchema map[string]any,
) {
	if reporter == nil {
		return
	}
	obs, ok := reporter.(progress.BatchObserver)
	if !ok {
		return
	}
	evt := progress.BatchEvent{
		Stage:           "ruby_alignment",
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
		HTTPStatus:      httpStatus,
		TriedBackends:   triedBackends,
		RoundIndex:      roundIndex,
		Attempt:         attempt,
		SystemPrompt:    sys,
		UserMessage:     usr,
		ResponseFormat:  responseFormat,
		JSONSchema:      jsonSchema,
		ResponseContent: receivedContent,
	}
	obs.OnBatchEvent(evt)
}

// rubyTagRe 用于从 OriginalSource 中剥离 ruby 标签。
var rubyTagRe = regexp.MustCompile(`<ruby>(.*?)<rt>(.*?)</rt>(.*?)</ruby>`)

// buildDirectedAlignmentPrompt 构建定向注音对齐的 system/user 消息和 JSON Schema。
// 只下发仍未对齐的条目（missing，带 id/source_base/source_text），要求 LLM 回显 id。
func buildDirectedAlignmentPrompt(seg *Segment, missing []ruby.Item, translation string) (string, string, map[string]any) {
	sys := `你是注音对齐工具。给定原文、译文和尚未对齐的注音条目，确定每个条目在译文中对应的文本。

规则：
- "id" 必须回显输入条目的 id；无法在译文中找到对应文本的条目可省略 id。
- "base" 必须是译文中实际出现的文本（不是原文基底），专有名词等未翻译的词除外。
- "text" 是标注文本：phonetic/semantic 保留原文（不翻译），creative 需要翻译。
- "kind" 是注音分类：
  · phonetic（音注）：纯读音标注。
  · semantic（义训）：语义解释标注，基底与标注语意一致或相近。
  · creative（创意注音）：基底与标注存在语义落差。
- 仅输出 JSON，无额外文字。`

	// 取原文（优先 OriginalSource，去掉 ruby 标签）
	source := seg.OriginalSource
	if source == "" {
		source = seg.Source
	}
	source = stripRubyTagsForAlignment(source)

	type missIn struct {
		ID         string `json:"id"`
		SourceBase string `json:"source_base"`
		SourceText string `json:"source_text"`
	}
	miss := make([]missIn, len(missing))
	for i, it := range missing {
		miss[i] = missIn{ID: it.ID, SourceBase: it.SourceBase, SourceText: it.SourceText}
	}

	userMsg := struct {
		Source      string   `json:"source"`
		Translation string   `json:"translation"`
		Missing     []missIn `json:"missing"`
	}{
		Source:      source,
		Translation: translation,
		Missing:     miss,
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
						"id":   map[string]any{"type": "string"},
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

// buildDirectedAlignmentPromptText 构建 text 模式的定向注音对齐提示词。
// 用户消息列出仍未对齐的条目（id / source_base / source_text），
// LLM 输出每行一条 "base | text | kind[ | id]"（id 可选）。
func buildDirectedAlignmentPromptText(seg *Segment, missing []ruby.Item, translation string) (string, string) {
	sys := `你是注音对齐工具。给定原文、译文和尚未对齐的注音条目，确定每个条目在译文中对应的文本。

规则：
- "base" 必须是译文中实际出现的文本（不是原文基底），专有名词等未翻译的词除外。
- "text" 是标注文本：phonetic/semantic 保留原文（不翻译），creative 需要翻译。
- "kind" 是注音分类：
  · phonetic（音注）：纯读音标注。
  · semantic（义训）：语义解释标注，基底与标注语意一致或相近。
  · creative（创意注音）：基底与标注存在语义落差。
- 每行输出一条，格式为：base | text | kind | id
  （id 可省略：无法在译文中找到对应文本的条目不输出 id）
- 仅输出对齐结果，无额外文字。`

	source := seg.OriginalSource
	if source == "" {
		source = seg.Source
	}
	source = stripRubyTagsForAlignment(source)

	var sb strings.Builder
	sb.WriteString("原文：")
	sb.WriteString(source)
	sb.WriteString("\n译文：")
	sb.WriteString(translation)
	sb.WriteString("\n未对齐条目：\n")
	for i, it := range missing {
		sb.WriteString(strconv.Itoa(i + 1))
		sb.WriteString(". ")
		sb.WriteString(it.ID)
		sb.WriteString(" / ")
		sb.WriteString(it.SourceBase)
		sb.WriteString(" / ")
		sb.WriteString(it.SourceText)
		sb.WriteString("\n")
	}

	return sys, sb.String()
}

// parseAlignmentResponseText 解析 text 模式的对齐响应。
// 每行格式：base | text | kind（3 字段）或 base | text | kind | id（4 字段）。
// 委托 ruby.ParseSectionLine 做右侧分割解析（与 section 路径同源），
// 避免两套解析器在容错与 id 提取规则上漂移。trim/空 base 过滤统一走 ruby.NormalizeOutputEntries。
func parseAlignmentResponseText(text string, expectedCount int) []ruby.OutputEntry {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	entries := make([]ruby.OutputEntry, 0, expectedCount)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		base, entryText, kind, id, ok := ruby.ParseSectionLine(line)
		if !ok {
			continue
		}
		entries = append(entries, ruby.OutputEntry{
			Base: base,
			Text: entryText,
			Kind: kind,
			ID:   id,
		})
	}
	return ruby.NormalizeOutputEntries(entries)
}

// parseAlignmentResponse 从 LLM 响应中解析 ruby_output。
// 委托 repair.TryRepairRubyAlignment 做多层结构修复（结巴/截断/尾随逗号/BOM/控制字符），
// 失败或空返回 nil。第二个返回值为修复算子链，便于日志诊断。
func parseAlignmentResponse(text string, opt repair.Options) ([]ruby.OutputEntry, []string) {
	entries, repaired, err := repair.TryRepairRubyAlignment(text, opt)
	if err != nil {
		return nil, nil
	}
	return entries, repaired
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
func filterByKinds(output []ruby.OutputEntry, keep map[string]bool) []ruby.OutputEntry {
	var result []ruby.OutputEntry
	for _, entry := range output {
		if entry.Kind == "" || keep[entry.Kind] {
			result = append(result, entry)
		}
	}
	return result
}
