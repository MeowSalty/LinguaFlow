package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// ReviseHandler 实现 RoundHandler，对带待处理语义问题的译文做 LLM 修订。
// 不改译文、不改段落状态；内存与数据库写回由批次处理器负责。
//
// 输入保护与 translate 轮同构：修订 prompt 中的 target 为占位符化、注音剥离形态，
// LLM 返回后经占位符完整性校验 → 还原 → 注音对齐还原，最终译文才进入 callback。
// 保护/还原全部在批内局部状态完成，doc 的 Source/Target/Protected/Meta 全程不被
// 污染（worker 的 CAS 写回以 doc 原始 Target 为 baseline）。
type ReviseHandler struct {
	Backend          backend.Backend
	Renderer         *prompt.ReviseRenderer
	BatchSize        int
	MaxWordsPerBatch int
	// MaxBatchIndexSpan 同批段落文档索引跨度上限（max-min）；<=0 不限制（默认）。
	MaxBatchIndexSpan int
	Retry             backend.RetryPolicy
	ResponseMode      string
	Repair            repair.Options
	IssueCodes        []string
	Reporter          progress.Reporter
	Logger            *slog.Logger
	RoundIndex        int // execution plan round index, set by caller

	// Protector/Ruby* 与 TranslateHandler 同名同语义（protect 规则与 ruby 配置
	// 借用计划内 translate 轮策略）；零值时降级为原文直发（无 translate 轮的计划）。
	Protector         protect.Protector
	RubyEnabled       bool
	RubyPreserveKinds []string
	RubyMode          string
	RubyRestorer      *ruby.Restorer
	RubyRetryBackends []backend.Backend
	RubyRetryAttempts int // 注音对齐定向重试轮数；<=0 兜底为 1（仅 backends 非空时生效）
}

func (h *ReviseHandler) ModeName() string { return RoundModeRevise }

func (h *ReviseHandler) Finalize(_ context.Context, _ *Document, _ []int) error { return nil }

func (h *ReviseHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *ReviseHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
}

func (h *ReviseHandler) emitBatchOutcome(evt progress.BatchEvent) {
	if h.Reporter == nil {
		return
	}
	obs, ok := h.Reporter.(progress.BatchObserver)
	if ok {
		obs.OnBatchEvent(evt)
	}
}

func (h *ReviseHandler) issueCodeSet() map[string]struct{} {
	codes := h.IssueCodes
	if len(codes) == 0 {
		codes = qa.SemanticQACodes()
	}
	set := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		set[code] = struct{}{}
	}
	return set
}

func reviseSegmentInScope(seg Segment, codes map[string]struct{}) bool {
	for _, issue := range seg.Issues {
		if issue.IsPending() {
			if _, ok := codes[issue.Code]; ok {
				return true
			}
		}
	}
	return false
}

// reviseProtectState 是单段在修订批内的保护上下文（批内局部状态，不落 doc）。
type reviseProtectState struct {
	mapping   map[string]string // target 侧「占位符 → 原片段」映射
	rubyItems []ruby.Item       // 从 target 提取的注音条目（尚无 LLM 回填）
	// strippedSource 是剥离注音标签后的 source，供注音对齐定向重试引用原文。
	strippedSource string
}

// buildProtectedInputs 构建修订 prompt 的输入段：target 经注音剥离 + 占位符化，
// source 仅剥离注音标签（source 仅供参考无守恒约束，占位符化反而会引入"LLM 拷贝
// source 占位符进 target"的 invented 误判风险），snippet 同步映射到保护形态以保
// 证在 prompt 内可定位。全部变换只落在返回的局部状态上，doc 不被修改。
func (h *ReviseHandler) buildProtectedInputs(
	doc *Document,
	idxs []int,
	codes map[string]struct{},
) ([]prompt.ReviseSegment, []reviseProtectState, map[string][]prompt.RubyAnnotation, error) {
	segments := make([]prompt.ReviseSegment, 0, len(idxs))
	states := make([]reviseProtectState, len(idxs))
	var rubyAnns map[string][]prompt.RubyAnnotation
	for k, idx := range idxs {
		seg := &doc.Segments[idx]
		st := &states[k]

		target := seg.Target
		if h.RubyEnabled {
			var items []ruby.Item
			target, items = extractTargetRuby(target)
			if len(items) > 0 {
				st.rubyItems = items
				anns := make([]prompt.RubyAnnotation, len(items))
				for i, it := range items {
					anns[i] = prompt.RubyAnnotation{ID: it.ID, Base: it.SourceBase, Text: it.SourceText}
				}
				if rubyAnns == nil {
					rubyAnns = make(map[string][]prompt.RubyAnnotation)
				}
				rubyAnns[seg.ID] = anns
			}
		}

		protectedTarget, mapping, err := protect.ProtectText(h.Protector, target)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("protect target segment %d: %w", idx, err)
		}
		st.mapping = mapping

		source := seg.Source
		if h.RubyEnabled {
			source = ruby.StripRubyTags(source)
			st.strippedSource = source
		}

		issues := make([]prompt.ReviseIssue, 0)
		for _, issue := range seg.Issues {
			if !issue.IsPending() {
				continue
			}
			if _, ok := codes[issue.Code]; !ok {
				continue
			}
			snippet := ""
			if issue.Span != nil {
				snippet = mapSnippetToProtectedForm(issue.Span.MatchedText, mapping)
			}
			issues = append(issues, prompt.ReviseIssue{Code: issue.Code, Message: issue.Message, Snippet: snippet})
		}
		segments = append(segments, prompt.ReviseSegment{ID: seg.ID, Source: source, Target: protectedTarget, Issues: issues})
	}
	return segments, states, rubyAnns, nil
}

// finalizeRevision 对单条修订结果做占位符完整性校验与两级还原：
// 变体归一 → PlaceholderViolations（违规即拒绝，由调用方计入 unresolved）→
// RestoreText 还原保护片段 → 注音对齐还原（复用 restoreSegmentRuby，获得与
// translate 轮一致的 inline 标记容错与定向对齐重试）。
// 返回最终译文；ok=false 表示占位符守恒被破坏或注音回填不完整，该段不可采信。
func (h *ReviseHandler) finalizeRevision(
	ctx context.Context,
	seg *Segment,
	revision prompt.ReviseRevision,
	st *reviseProtectState,
	rubyOutput map[string][]ruby.OutputEntry,
	isTextMode bool,
	logger *slog.Logger,
) (string, bool) {
	text := revision.Target
	if h.Repair.PlaceholderNormalize {
		if normText, normalized := repair.NormalizePlaceholders(text, st.mapping); len(normalized) > 0 {
			logger.Info("revise placeholders normalized", "seg", seg.ID, "normalized", normalized)
			text = normText
		}
	}
	vseg := &model.Segment{Target: text, Protected: st.mapping}
	missingPH, duplicatedPH, inventedPH := protect.PlaceholderViolations(vseg)
	if len(missingPH) > 0 || len(duplicatedPH) > 0 || len(inventedPH) > 0 {
		logger.Warn("revise segment placeholder integrity violation",
			"seg", seg.ID,
			"missing", missingPH,
			"duplicated", duplicatedPH,
			"invented", inventedPH)
		return "", false
	}
	text = protect.RestoreText(text, st.mapping)

	if h.RubyEnabled && h.RubyRestorer != nil && len(st.rubyItems) > 0 {
		// 协议禁止在 target 中内联 ⟦ruby:...⟧ 标记：translate 轮将其作为容错通道，
		// 但标记计数由 LLM 自证、无法与存量条目交叉核验，容忍即开放守卫绕过
		// （伪造标记凑数丢弃存量注音）——检测到即按占位符违规同语义拒绝。
		if strings.Contains(text, "⟦ruby:") {
			logger.Warn("revise segment returned forbidden inline ruby marker", "seg", seg.ID)
			return "", false
		}
		items := append([]ruby.Item(nil), st.rubyItems...)
		if ro, ok := rubyOutput[revision.ID]; ok && len(ro) > 0 {
			ruby.MergeByOutput(items, ro)
		}
		// 临时段副本驱动 restoreSegmentRuby：Meta 用全新 map，不共享/污染原段。
		tmp := &Segment{
			ID:             seg.ID,
			Source:         st.strippedSource,
			OriginalSource: st.strippedSource,
			Target:         text,
			Meta:           map[string]any{"ruby_items": items},
		}
		keep := kindSet(h.RubyPreserveKinds)
		restored := restoreSegmentRuby(ctx, tmp, h.RubyRestorer, keep,
			h.RubyRetryBackends, h.Retry, logger, h.Reporter, isTextMode, h.RoundIndex, h.Repair,
			h.RubyRetryAttempts)
		// 注音守恒守卫：修订前的 target 带注音，剥离后必须完整回填后才能采信。
		// want 口径刻意不依赖 LLM 事后回填的 Kind（提取期 Kind 恒空，LLM 把存量
		// 条目重分类到 keep 外不得把条目挤出守恒口径——那会退回静默丢失），并经
		// ruby.Item.Restorable 排除双 base 皆空的退化条目（与 RestoreItems 的
		// total 单源对齐，永不可还原）；keep 为空集 = 用户显式全剥离，want=0。
		// 判据用还原器实际插入数而非子串计数，LLM 写字面量 <ruby> 文本或
		// ⟦ruby:⟧ 标记均无法凑数（后者直接在上方拒绝）。不完整即拒绝该段修订
		// （计入 unresolved），防止剥离形态译文被静默写回导致存量注音丢失
		// （translate 轮无此守卫：其注音为新增而非存量）。
		want := 0
		if len(keep) > 0 {
			for _, it := range items {
				if it.Restorable() {
					want++
				}
			}
		}
		if restored < want {
			logger.Warn("revise segment ruby realignment incomplete",
				"seg", seg.ID, "expected", want, "restored", restored)
			return "", false
		}
		text = tmp.Target
	}
	return text, true
}

// extractTargetRuby 提取译文中的注音条目并返回剥离标签后的译文。
// 复用 ruby.Extractor 于临时段副本，与 translate 轮对 source 的提取同源；
// 文本不含 ruby 标签时原样返回（快速路径，避免整段正则扫描）。
func extractTargetRuby(target string) (string, []ruby.Item) {
	if !strings.Contains(target, "<ruby>") {
		return target, nil
	}
	tmp := &model.Segment{Source: target, Meta: map[string]any{}}
	if err := (&ruby.Extractor{}).Protect(tmp); err != nil {
		return target, nil
	}
	items, _ := tmp.Meta["ruby_items"].([]ruby.Item)
	return tmp.Source, items
}

// mapSnippetToProtectedForm 把 issue snippet（原始文本片段）映射到与 prompt 内
// target 一致的形态：剥离注音标签后，把已被保护的原文片段替换回占位符。
// 按「原片段长度降序」替换：映射值之间可能存在包含关系（如 "code" 与 "encode"），
// 先替换更长原片段可避免误替换（restoreAll 的 key 长降序同理，方向相反）。
func mapSnippetToProtectedForm(snippet string, mapping map[string]string) string {
	if snippet == "" {
		return snippet
	}
	if strings.Contains(snippet, "<ruby>") {
		snippet = ruby.StripRubyTags(snippet)
	}
	if len(mapping) == 0 {
		return snippet
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		vi, vj := mapping[keys[i]], mapping[keys[j]]
		if len(vi) != len(vj) {
			return len(vi) > len(vj)
		}
		return keys[i] > keys[j]
	})
	for _, k := range keys {
		v := mapping[k]
		if v != "" && strings.Contains(snippet, v) {
			snippet = strings.ReplaceAll(snippet, v, k)
		}
	}
	return snippet
}

// BuildBatches 选 Translate 标记、status∈{translated,edited}、Target 非空且含待修订
// 语义问题的段，按约束分批。pending==nil 时自行扫描 doc 并排除跨轮已解决段。
func (h *ReviseHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	logger := h.logger()
	if h.Renderer == nil {
		logger.Warn("revise handler: renderer is nil, skipping")
		return nil, nil
	}
	if h.Backend == nil {
		logger.Warn("revise handler: backend is nil, skipping")
		return nil, nil
	}

	codes := h.issueCodeSet()
	var scan []int
	if pending != nil {
		// 池>0：仅处理给定 unresolved，避免全量重扫。
		scan = pending
	} else {
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if !seg.Translate || (seg.Status != "translated" && seg.Status != "edited") || seg.Target == "" {
				continue
			}
			if !reviseSegmentInScope(*seg, codes) {
				continue
			}
			if doc.ResolvedIndices != nil {
				if _, ok := doc.ResolvedIndices[i]; ok {
					continue
				}
			}
			scan = append(scan, i)
		}
	}
	if len(scan) == 0 {
		logger.Info("revise handler: no segments with pending issues")
		return nil, nil
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
		WordCount: func(seg Segment) int {
			return CountWords(seg.Source) + CountWords(seg.Target)
		},
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 && h.MaxBatchIndexSpan <= 0 {
		logger.Info("revise handler: no batch limit, sending all segments at once", "segments", len(scan))
		return [][]int{scan}, nil
	}
	batches := BuildPackedPendingBatches(doc, scan, constraint, h.MaxBatchIndexSpan)
	logger.Info("revise handler: batches built", "segments", len(scan), "batches", len(batches),
		"batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch,
		"max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}

// ProcessBatch 渲染修订 prompt → 调用 LLM → 解析整段修订译文。
// target 与 translate 轮同构地先行保护：注音剥离 + 占位符化后下发；LLM 返回经
// 占位符完整性校验、还原与注音对齐还原，最终译文才进入 callback。
// 失败时不产出 callbackResult；成功时 callback 仅包含 LLM 返回的合法段。
func (h *ReviseHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	batchStart := time.Now()
	rep := h.reporter()
	tried := []string{h.Backend.Name()}
	codes := h.issueCodeSet()
	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()

	segments, states, rubyAnns, protectErr := h.buildProtectedInputs(doc, idxs, codes)
	if protectErr != nil {
		logger.Error("revise protect failed", "err", protectErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs),
			BackendName: h.Backend.Name(), Status: "failed", DurationMs: time.Since(batchStart).Milliseconds(),
			TriedBackends: tried, ErrorType: "protect_error", ErrorMessage: protectErr.Error(), RoundIndex: h.RoundIndex,
		})
		return h.terminalFailure(doc, idxs, rep)
	}

	sys, usr, renderErr := h.Renderer.Render(prompt.ReviseData{
		SourceLang:      doc.SourceLang,
		TargetLang:      doc.TargetLang,
		Segments:        segments,
		Protocol:        proto,
		RubyAnnotations: rubyAnns,
		RubyMode:        h.RubyMode,
	})
	if renderErr != nil {
		logger.Error("revise render failed", "err", renderErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs),
			BackendName: h.Backend.Name(), Status: "failed", DurationMs: time.Since(batchStart).Milliseconds(),
			TriedBackends: tried, ErrorType: "render_error", ErrorMessage: renderErr.Error(), RoundIndex: h.RoundIndex,
		})
		return h.terminalFailure(doc, idxs, rep)
	}

	req := backend.Request{System: sys, User: usr}
	if isTextMode {
		req.ResponseFormat = "none"
	} else {
		revisionIDs := make([]string, len(segments))
		for i, s := range segments {
			revisionIDs[i] = s.ID
		}
		req.JSONSchema = prompt.ReviseRevisionSchema(h.RubyMode != "", revisionIDs)
	}

	callStart := time.Now()
	resp, callErr := h.Backend.Translate(ctx, req)
	if callErr != nil {
		isExternalInterrupt := errors.Is(callErr, context.Canceled) ||
			(errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() != nil)
		if isExternalInterrupt {
			logger.Info("revise backend interrupted by context", "backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(backendErrorBatchEvent(RoundModeRevise, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			return h.preserveResult(doc, idxs, rep)
		}
		isLocalTimeout := errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() == nil
		if isFatalBackendError(callErr) {
			logger.Warn("revise backend fatal error, deferring to cross-round", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
			h.emitBatchOutcome(backendErrorBatchEvent(RoundModeRevise, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			for range idxs {
				rep.SegmentDone()
			}
			return batchResult{fatalUnresolved: idxs}
		}
		h.emitBatchOutcome(backendErrorBatchEvent(RoundModeRevise, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
		if (isLocalTimeout || backend.IsRetryable(callErr)) && attempt+1 < transientBudgetFor(h.Retry) {
			logger.Warn("revise backend error, will backoff and retry", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
			wait := backoffDuration(attempt, h.Retry, callErr)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return h.preserveResult(doc, idxs, rep)
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		logger.Warn("revise backend failed terminally, deferring to next pool", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
		return batchResult{unresolved: idxs}
	}

	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)
	revisions, rubyOutput, parseRepaired, parseErr := repair.ParseReviseByMode(resp.Text, isTextMode, h.Repair)
	if parseErr != nil {
		logger.Warn("revise parse failed", "backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr, "attempt", attempt, "resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		h.emitBatchOutcome(progress.BatchEvent{
			Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs), BackendName: h.Backend.Name(), Status: "failed",
			DurationMs: time.Since(callStart).Milliseconds(), InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
			SentContent: usr, ReceivedContent: resp.Text, TriedBackends: tried, ErrorType: "parse_error", ErrorMessage: parseErr.Error(), RoundIndex: h.RoundIndex,
			Attempt: attempt, SystemPrompt: sys, UserMessage: usr, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, ResponseContent: resp.Text,
		})
		if attempt+1 < transientBudgetFor(h.Retry) {
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		return batchResult{unresolved: idxs}
	}
	if len(parseRepaired) > 0 {
		logger.Info("revise response repaired", "backend", h.Backend.Name(), "ops", parseRepaired)
	}

	segByID := make(map[string]*Segment, len(idxs))
	stateByID := make(map[string]*reviseProtectState, len(idxs))
	for k, idx := range idxs {
		segByID[doc.Segments[idx].ID] = &doc.Segments[idx]
		stateByID[doc.Segments[idx].ID] = &states[k]
	}
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	returned := make(map[int]struct{}, len(idxs))
	seen := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		seg, ok := segByID[revision.ID]
		if !ok {
			continue
		}
		if _, duplicate := seen[revision.ID]; duplicate {
			continue
		}
		seen[revision.ID] = struct{}{}
		text, ok := h.finalizeRevision(ctx, seg, revision, stateByID[revision.ID], rubyOutput, isTextMode, logger)
		if !ok {
			// 占位符守恒被破坏：不进入 callback，落入 missing 计入 unresolved，
			// 与 translate 轮违规即拒的语义一致。
			continue
		}
		for _, idx := range idxs {
			if doc.Segments[idx].ID == revision.ID {
				callbackSegs = append(callbackSegs, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: text})
				returned[idx] = struct{}{}
				break
			}
		}
	}
	// LLM 漏返的段计入 unresolved（与 translate 轮 trans 缺失即 unresolved 的约定
	// 一致），交由下一池重试、末轮跨轮传播并触发软警告。若只按返回段回调，
	// computeResolved 会把漏返段计为已解决，后续同模式轮将跳过它们，pending
	// issue 无人修订且不产生任何失败信号。
	missing := make([]int, 0)
	for _, idx := range idxs {
		if _, ok := returned[idx]; ok {
			rep.SegmentDone()
			continue
		}
		missing = append(missing, idx)
	}
	if len(missing) > 0 {
		logger.Warn("revise response missing segment revisions", "backend", h.Backend.Name(), "batch_size", len(idxs), "missing", len(missing))
	}

	logger.Info("revise batch ok", "backend", h.Backend.Name(), "segments", len(idxs), "revised", len(callbackSegs),
		"missing", len(missing),
		"prompt_tokens", resp.Usage.PromptTokens, "completion_tokens", resp.Usage.CompletionTokens)
	h.emitBatchOutcome(progress.BatchEvent{
		Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs), BackendName: h.Backend.Name(), Status: "success",
		DurationMs: time.Since(batchStart).Milliseconds(), InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
		SentContent: usr, ReceivedContent: resp.Text, TriedBackends: tried, RoundIndex: h.RoundIndex, Attempt: attempt,
		SystemPrompt: sys, UserMessage: usr, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, ResponseContent: resp.Text,
	})
	return batchResult{callbackResult: &BatchResult{Segments: callbackSegs}, unresolved: missing}
}

// preserveResult 保留原译文；修订 handler 不在内存中改写段落。
func (h *ReviseHandler) preserveResult(doc *Document, idxs []int, rep progress.Reporter) batchResult {
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		callbackSegs = append(callbackSegs, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: seg.Target})
		rep.SegmentDone()
	}
	return batchResult{callbackResult: &BatchResult{Segments: callbackSegs}}
}

// terminalFailure 在 preserveResult 基础上标记 failedSegments，供 RunRound 累计为软警告。
func (h *ReviseHandler) terminalFailure(doc *Document, idxs []int, rep progress.Reporter) batchResult {
	result := h.preserveResult(doc, idxs, rep)
	result.failedSegments = idxs
	return result
}
