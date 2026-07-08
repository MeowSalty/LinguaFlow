package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// buildRequest 构建翻译请求的 prompt 和 backend.Request。
func (s *RoundExecutor) buildRequest(
	ctx context.Context,
	doc *Document,
	idxs []int,
	round Round,
	contextSet map[int]struct{},
	logger *slog.Logger,
) (string, string, backend.Request, []string, map[int]string, []prompt.GlossaryEntry, error) {
	renderer := s.resolveRoundRenderer(round)
	isTextMode := round.ResponseMode == "text"

	glos, tmHints := s.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	idMap := make(map[int]string, len(idxs))
	var wantIDs []string
	batchSources := make([]string, 0, len(idxs))
	transIdx := 0
	for k, idx := range idxs {
		seg := doc.Segments[idx]
		source := seg.Source
		isCtx := isContext(contextSet, idx)
		if isCtx && seg.OriginalSource != "" {
			source = seg.OriginalSource
		}

		var id string
		if isTextMode {
			if isCtx {
				id = "*"
			} else {
				transIdx++
				id = strconv.Itoa(transIdx)
			}
		} else {
			id = strconv.Itoa(k + 1)
		}
		idMap[idx] = id
		inputs[k] = prompt.SegmentInput{ID: id, Source: source, Translate: !isCtx}
		if !isCtx {
			wantIDs = append(wantIDs, id)
			batchSources = append(batchSources, seg.Source)
		}
	}

	rubyAnns := extractRubyAnnotationsFromDoc(doc, idxs, idMap)
	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Segments:          inputs,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   s.InlineBootstrap,
		MaxBootstrapTerms: s.calcMaxBootstrapTerms(batchSources),
		StrictSchema:      !isTextMode,
		TextMode:          isTextMode,
		RubyAnnotations:   rubyAnns,
		RubyMode:          round.RubyMode,
	}
	sys, usr, err := renderer.Render(data)
	if err != nil {
		return "", "", backend.Request{}, nil, nil, nil, fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}

	req := backend.Request{
		System: sys,
		User:   usr,
	}
	if !isTextMode {
		req.JSONSchema = translationsSchema(wantIDs, s.InlineBootstrap, round.RubyMode != "")
	} else {
		req.ResponseFormat = "none"
	}

	return sys, usr, req, wantIDs, idMap, glos, nil
}

// isRetryableByBackoff 判断错误是否为 429/503 限流错误，应通过退避等待后重试。
func isRetryableByBackoff(err error) bool {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code == 429 || code == 503
	}
	return false
}

// backoffDuration 计算退避等待时间。
func backoffDuration(attempt int, retry backend.RetryPolicy, lastErr error) time.Duration {
	wait := retry.Backoff << attempt
	if wait < minRateLimitBackoff {
		wait = minRateLimitBackoff
	}

	var raErr backend.RetryAfterError
	if errors.As(lastErr, &raErr) && raErr.HTTPStatus() == 429 {
		if ra := raErr.GetRetryAfter(); ra > wait {
			wait = ra
		}
	}

	if retry.Jitter {
		wait += time.Duration(rand.Int63n(int64(wait) + 1))
	}
	return wait
}

// minRateLimitBackoff 是 429 错误的最小退避时间。
const minRateLimitBackoff = 5 * time.Second

// shrinkTo 计算缩批后的大小。
func shrinkTo(idxs []int, shrink float64) int {
	if shrink <= 0 || shrink >= 1 || math.IsNaN(shrink) || math.IsInf(shrink, 0) {
		return 1
	}
	next := int(math.Floor(float64(len(idxs)) * shrink))
	if next >= len(idxs) {
		next = len(idxs) - 1
	}
	if next < 1 {
		return 1
	}
	return next
}

// tryPromptUpgrade 尝试通过附加反例 reminder 重试一次。
func (s *RoundExecutor) tryPromptUpgrade(
	ctx context.Context,
	doc *Document,
	round Round,
	req backend.Request,
	resp *backend.Response,
	res repair.Result,
	wantIDs []string,
	logger *slog.Logger,
) (*backend.Response, repair.Result, bool) {
	repairOpts := s.resolveRoundRepair(round)
	if !repairOpts.PromptUpgrade || res.ParseErr == nil {
		return resp, res, false
	}

	isTextMode := round.ResponseMode == "text"

	reminder := repair.BuildRetryReminder(nil, res.ParseErr, headSnippet(resp.Text, 200))
	req2 := req
	req2.System = req.System + reminder

	resp2, err2 := s.callOnce(ctx, round.Backend, req2)
	if err2 != nil {
		return resp, res, false
	}

	var res2 repair.Result
	if isTextMode {
		res2 = parseBatchResponseLenientText(resp2.Text, wantIDs, repairOpts)
	} else {
		res2 = parseBatchResponseLenient(resp2.Text, wantIDs, repairOpts)
	}
	if res2.ParseErr != nil {
		return resp, res, false
	}

	logger.Info("batch response recovered by prompt upgrade",
		"backend", round.Backend.Name(), "repaired", res2.Repaired)
	atomic.AddInt64(&doc.InputTokens, resp2.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp2.Usage.CompletionTokens)
	return resp2, res2, true
}

// processBatchAttempt 执行单次 LLM 调用尝试，返回 batchResult。
func (s *RoundExecutor) processBatchAttempt(
	ctx context.Context,
	doc *Document,
	job batchJob,
	round Round,
	logger *slog.Logger,
	contextSet map[int]struct{},
	expandedIdxs []int,
) batchResult {
	batchStart := time.Now()
	repairOpts := s.resolveRoundRepair(round)

	_, usr, req, wantIDs, _, glos, buildErr := s.buildRequest(ctx, doc, expandedIdxs, round, contextSet, logger)
	if buildErr != nil {
		logger.Error("build request failed", "err", buildErr)
		return batchResult{unresolved: filterPendingIdxs(job.idxs, contextSet)}
	}

	tried := []string{round.Backend.Name()}
	pendingIdxs := filterPendingIdxs(job.idxs, contextSet)

	callStart := time.Now()
	resp, callErr := s.callOnce(ctx, round.Backend, req)

	if callErr != nil {
		if isFatalBackendError(callErr) {
			logger.Error("backend returned fatal error",
				"backend", round.Backend.Name(), "batch_size", len(job.idxs), "err", callErr)
			s.emitBatchOutcome(progress.BatchEvent{
				Stage:         "translate",
				SegmentIDs:    pendingSegmentIDStrings(pendingIdxs),
				SegmentCount:  len(pendingIdxs),
				BackendName:   round.Backend.Name(),
				Status:        "failed",
				DurationMs:    time.Since(callStart).Milliseconds(),
				SentContent:   usr,
				TriedBackends: tried,
				ErrorType:     "backend_error",
				ErrorMessage:  callErr.Error(),
				HTTPStatus:    httpStatusFromErr(callErr),
			})
			return batchResult{unresolved: pendingIdxs}
		}

		if isRetryableByBackoff(callErr) {
			logger.Warn("backend returned rate limit error, will backoff and retry",
				"backend", round.Backend.Name(), "batch_size", len(job.idxs), "err", callErr)
			s.emitBatchOutcome(progress.BatchEvent{
				Stage:         "translate",
				SegmentIDs:    pendingSegmentIDStrings(pendingIdxs),
				SegmentCount:  len(pendingIdxs),
				BackendName:   round.Backend.Name(),
				Status:        "failed",
				DurationMs:    time.Since(callStart).Milliseconds(),
				SentContent:   usr,
				TriedBackends: tried,
				ErrorType:     "backend_error",
				ErrorMessage:  callErr.Error(),
				HTTPStatus:    httpStatusFromErr(callErr),
			})
			wait := backoffDuration(job.attempt, round.Retry, callErr)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return batchResult{unresolved: pendingIdxs}
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: job.idxs, attempt: job.attempt + 1}}
		}

		logger.Warn("backend failed for batch, shrinking",
			"backend", round.Backend.Name(), "batch_size", len(job.idxs), "err", callErr)
		s.emitBatchOutcome(progress.BatchEvent{
			Stage:           "translate",
			SegmentIDs:      pendingSegmentIDStrings(pendingIdxs),
			SegmentCount:    len(pendingIdxs),
			BackendName:     round.Backend.Name(),
			Status:          "failed",
			DurationMs:      time.Since(callStart).Milliseconds(),
			SentContent:     usr,
			TriedBackends:   tried,
			ErrorType:       "backend_error",
			ErrorMessage:    callErr.Error(),
			HTTPStatus:      httpStatusFromErr(callErr),
			ShrinkAttempted: len(pendingIdxs) > 1,
		})
		nextSize := shrinkTo(job.idxs, round.FallbackShrink)
		var dropped []int
		if nextSize < len(job.idxs) {
			dropped = filterPendingIdxs(job.idxs[nextSize:], contextSet)
		}
		return batchResult{
			unresolved: dropped,
			retry:      &batchJob{idxs: job.idxs[:nextSize], attempt: job.attempt + 1},
		}
	}

	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	isTextMode := round.ResponseMode == "text"
	var res repair.Result
	if isTextMode {
		res = parseBatchResponseLenientText(resp.Text, wantIDs, repairOpts)
	} else {
		res = parseBatchResponseLenient(resp.Text, wantIDs, repairOpts)
	}

	if res.ParseErr != nil {
		if upgradedResp, upgradedRes, ok := s.tryPromptUpgrade(ctx, doc, round, req, resp, res, wantIDs, logger); ok {
			resp = upgradedResp
			res = upgradedRes
		} else {
			logger.Warn("batch response parse failed, shrinking",
				"backend", round.Backend.Name(), "batch_size", len(pendingIdxs), "err", res.ParseErr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", res.Repaired)
			s.emitBatchOutcome(progress.BatchEvent{
				Stage:           "translate",
				SegmentIDs:      pendingSegmentIDStrings(pendingIdxs),
				SegmentCount:    len(pendingIdxs),
				BackendName:     round.Backend.Name(),
				Status:          "failed",
				DurationMs:      time.Since(callStart).Milliseconds(),
				InputTokens:     resp.Usage.PromptTokens,
				OutputTokens:    resp.Usage.CompletionTokens,
				SentContent:     usr,
				ReceivedContent: resp.Text,
				TriedBackends:   tried,
				ErrorType:       "parse_error",
				ErrorMessage:    res.ParseErr.Error(),
				ShrinkAttempted: len(pendingIdxs) > 1,
			})
			nextSize := shrinkTo(job.idxs, round.FallbackShrink)
			var dropped []int
			if nextSize < len(job.idxs) {
				dropped = filterPendingIdxs(job.idxs[nextSize:], contextSet)
			}
			return batchResult{
				unresolved: dropped,
				retry:      &batchJob{idxs: job.idxs[:nextSize], attempt: job.attempt + 1},
			}
		}
	}

	missingRatio := 0.0
	if len(wantIDs) > 0 {
		missingRatio = float64(len(res.Missing)) / float64(len(wantIDs))
	}
	if len(res.Missing) > 0 && (!repairOpts.Partial || missingRatio >= repairOpts.PartialThreshold) {
		logger.Warn("partial recovery exceeded threshold, using best partial result",
			"backend", round.Backend.Name(), "missing", len(res.Missing), "total", len(wantIDs),
			"threshold", s.Repair.PartialThreshold, "partial_enabled", s.Repair.Partial)
	}

	if len(res.Repaired) > 0 {
		logger.Info("batch response repaired", "backend", round.Backend.Name(), "ops", res.Repaired,
			"missing", len(res.Missing))
	}

	rawRespText := resp.Text
	durationMs := time.Since(batchStart).Milliseconds()

	trans, glosEntries, rubyOutputMap := res.Trans, res.Glos, res.RubyOutput

	s.emitBatchEvent(pendingIdxs, wantIDs, round.Backend.Name(), res, rawRespText, usr,
		glos, resp.Usage, durationMs, tried, logger)

	logger.Debug("batch translated",
		"backend", round.Backend.Name(), "batch_size", len(job.idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries),
		"missing", len(res.Missing))

	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	unresolved, missing := s.processTranslatedSegments(ctx, doc, expandedIdxs, wantIDs, trans, rubyOutputMap, contextSet, repairOpts, round, logger)
	return batchResult{unresolved: unresolved, missing: missing}
}

// processTranslatedSegments 处理翻译结果：写回译文、占位符校验、Unprotect/RubyRestore/TM。
func (s *RoundExecutor) processTranslatedSegments(
	ctx context.Context,
	doc *Document,
	idxs []int,
	wantIDs []string,
	trans map[string]string,
	rubyOutputMap map[string][]ruby.OutputEntry,
	contextSet map[int]struct{},
	repairOpts repair.Options,
	round Round,
	logger *slog.Logger,
) (unresolved []int, missing []int) {
	rep := s.reporter()
	wantIDIdx := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		if isContext(contextSet, idx) {
			continue
		}
		id := wantIDs[wantIDIdx]
		wantIDIdx++
		text, ok := trans[id]
		if !ok || strings.TrimSpace(text) == "" {
			missing = append(missing, idx)
			continue
		}
		if rubyOutputMap != nil {
			if ro, rok := rubyOutputMap[id]; rok && len(ro) > 0 {
				if _, hasAnns := seg.Meta["ruby_annotations"]; hasAnns {
					seg.Meta["ruby_output"] = ro
				}
			}
		}
		if repairOpts.PlaceholderNormalize {
			if normText, normalized := repair.NormalizePlaceholders(text, seg.Protected); len(normalized) > 0 {
				logger.Info("placeholders normalized",
					"seg", seg.ID, "normalized", normalized)
				text = normText
			}
		}
		seg.Target = text
		if missingPH := protect.MissingPlaceholders(seg); len(missingPH) > 0 {
			logger.Warn("batch segment placeholders missing",
				"seg", seg.ID, "missing", missingPH)
			seg.Target = ""
			unresolved = append(unresolved, idx)
			continue
		}

		// TrimSpaces（在 Unprotect 之前）
		if round.Postprocess != nil && round.Postprocess.TrimSpaces {
			seg.Target = strings.TrimSpace(seg.Target)
		}

		// Unprotect
		if round.Protector != nil {
			if err := round.Protector.Unprotect(seg); err != nil {
				logger.Warn("unprotect failed", "seg", seg.ID, "err", err)
			}
		}

		// RubyRestore
		if round.RubyEnabled && s.RubyRestorer != nil {
			keepSet := kindSet(round.RubyPreserveKinds)
			isTextMode := round.ResponseMode == "text"
			restoreSegmentRuby(ctx, seg, s.RubyRestorer, keepSet,
				s.RubyRetryBackends, round.Retry, logger, s.Reporter, isTextMode)
		}

		// TM（直接调用，使用 OriginalSource）
		if s.TM != nil {
			source := seg.OriginalSource
			if source == "" {
				source = seg.Source
			}
			if err := s.TM.Add(ctx, source, seg.Target, doc.SourceLang, doc.TargetLang); err != nil {
				logger.Debug("tm add failed", "err", err)
			}
		}

		rep.SegmentDone()
	}
	return unresolved, missing
}

// isContext 检查 idx 是否在 contextSet 中。
func isContext(contextSet map[int]struct{}, idx int) bool {
	if len(contextSet) == 0 {
		return false
	}
	_, ok := contextSet[idx]
	return ok
}

// isFatalBackendError 判断是否为不可恢复的致命错误。
func isFatalBackendError(err error) bool {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code == 401 || code == 403
	}
	return false
}

func filterPendingIdxs(idxs []int, contextSet map[int]struct{}) []int {
	if len(contextSet) == 0 {
		return idxs
	}
	var pending []int
	for _, idx := range idxs {
		if !isContext(contextSet, idx) {
			pending = append(pending, idx)
		}
	}
	return pending
}

func pendingSegmentIDStrings(pendingIdxs []int) []string {
	segIDs := make([]string, len(pendingIdxs))
	for i, idx := range pendingIdxs {
		segIDs[i] = strconv.Itoa(idx)
	}
	return segIDs
}

func httpStatusFromErr(err error) int {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		return hsErr.HTTPStatus()
	}
	return 0
}

func (s *RoundExecutor) emitBatchOutcome(evt progress.BatchEvent) {
	rep := s.Reporter
	if rep == nil {
		return
	}
	obs, ok := rep.(progress.BatchObserver)
	if !ok {
		return
	}
	obs.OnBatchEvent(evt)
}

func (s *RoundExecutor) emitBatchEvent(
	pendingIdxs []int,
	wantIDs []string,
	backendName string,
	res repair.Result,
	rawRespText string,
	sentContent string,
	usedGlossary []prompt.GlossaryEntry,
	usage backend.Usage,
	durationMs int64,
	triedBackends []string,
	logger *slog.Logger,
) {
	segIDs := pendingSegmentIDStrings(pendingIdxs)

	status := "success"
	errorType := ""
	errorMsg := ""
	if len(res.Missing) > 0 {
		status = "partial"
	}
	if res.ParseErr != nil {
		errorType = "parse_error"
		errorMsg = res.ParseErr.Error()
	}

	s.emitBatchOutcome(progress.BatchEvent{
		Stage:           "translate",
		SegmentIDs:      segIDs,
		SegmentCount:    len(pendingIdxs),
		BackendName:     backendName,
		Status:          status,
		DurationMs:      durationMs,
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		SentContent:     sentContent,
		ReceivedContent: rawRespText,
		UsedGlossary:    usedGlossary,
		AddedGlossary:   res.Glos,
		ErrorType:       errorType,
		ErrorMessage:    errorMsg,
		TriedBackends:   triedBackends,
	})
}
