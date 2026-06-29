package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// translateSingleInRound 翻译单段（走 JSON 协议，含 S5 占位符补救）。
// 成功时上报进度并返回 (true, nil)；所有后端均失败时返回 (false, nil)，由调用方 defer 到下一轮。
// 返回非 nil error 表示 stage 级别终止（如 limiter 错误）。
func (s *Translate) translateSingleInRound(ctx context.Context, doc *Document, idx int, round Round, logger *slog.Logger) (bool, error) {
	renderer := s.resolveRoundRenderer(round)
	repairOpts := s.resolveRoundRepair(round)
	batchStart := time.Now()

	seg := &doc.Segments[idx]

	glos, tmHints := s.lookupHints(ctx, doc, []int{idx}, logger)

	rubyAnns := extractRubyAnnotationsFromDoc(doc, []int{idx}, nil)
	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Source:            seg.Source,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   s.InlineBootstrap,
		MaxBootstrapTerms: s.calcMaxBootstrapTerms([]string{seg.Source}),
		StrictSchema:      true,
		RubyAnnotations:   rubyAnns,
		RubyOutputFormat:  s.RubyOutputFormat,
	}
	sys, usr, err := renderer.Render(data)
	if err != nil {
		return false, fmt.Errorf("render prompt for seg %s: %w", seg.ID, err)
	}

	wantIDs := []string{prompt.SingleID}
	req := backend.Request{
		System:     sys,
		User:       usr,
		JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap, s.RubyOutputFormat != ""),
	}
	var (
		resp        *backend.Response
		trans       map[string]string
		glosEntries []prompt.BootstrapEntry
		rubyOutput  map[string][]protect.RubyOutputEntry
		picked      backend.Backend
		tried       []string
		lastErr     error
	)
	for _, b := range round.Backends {
		tried = append(tried, b.Name())
		resp, err = s.callOnce(ctx, b, req, round.Retry)
		if err != nil {
			if isFatalBackendError(err) {
				logger.Error("backend returned fatal error",
					"backend", b.Name(), "seg", seg.ID, "err", err)
			} else {
				logger.Warn("translate failed, trying next backend",
					"seg", seg.ID, "backend", b.Name(), "err", err)
			}
			lastErr = err
			continue
		}
		var perr error
		trans, glosEntries, rubyOutput, perr = parseBatchResponse(resp.Text, wantIDs)
		if perr != nil {
			if result := parseBatchResponseLenient(resp.Text, wantIDs, repairOpts); !result.Fatal && len(result.Missing) == 0 {
				trans, glosEntries, rubyOutput = result.Trans, result.Glos, result.RubyOutput
				perr = nil
			}
		}
		if perr != nil {
			logger.Warn("single response parse failed, trying next backend",
				"seg", seg.ID, "backend", b.Name(), "err", perr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
			lastErr = perr
			continue
		}
		picked = b
		break
	}
	if picked == nil {
		durationMs := time.Since(batchStart).Milliseconds()
		s.emitSingleBatchEvent(idx, seg.ID, "", usr, glos, nil, backend.Usage{},
			"failed", durationMs, tried, s.classifySingleError(lastErr), lastErr, logger)
		seg.Target = ""
		return false, nil
	}
	logger.Debug("segment translated",
		"seg", seg.ID, "backend", picked.Name(),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries))
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	durationMs := time.Since(batchStart).Milliseconds()
	s.emitSingleBatchEvent(idx, seg.ID, picked.Name(), usr, glos, resp, resp.Usage,
		"success", durationMs, tried, "", nil, logger)
	// 存储 ruby_output 到 seg.Meta
	if rubyOutput != nil {
		if ro, ok := rubyOutput[prompt.SingleID]; ok && len(ro) > 0 {
			if seg.Meta == nil {
				seg.Meta = make(map[string]any)
			}
			seg.Meta["ruby_output"] = ro
		}
	}
	// ruby_output 条目数不足时，用 reminder 重试补全
	if s.RubyOutputFormat == "ruby_output" {
		anns, hasAnnots := seg.Meta["ruby_annotations"].([]protect.RubyAnnotation)
		existing, hasOutput := seg.Meta["ruby_output"].([]protect.RubyOutputEntry)
		if hasAnnots && len(anns) > 0 && (!hasOutput || len(existing) < len(anns)) {
			existingCount := 0
			if hasOutput {
				existingCount = len(existing)
			}
			logger.Warn("ruby_output entries missing, retrying with reminder",
				"seg", seg.ID, "annotations", len(anns), "output", existingCount)
			rubyReminder := fmt.Sprintf(
				"\n\nIMPORTANT: your previous response included only %d ruby_output entries, but there are %d annotations. "+
					"Return ALL %d entries in ruby_output, each with base, text, and kind fields. Do not omit any.",
				existingCount, len(anns), len(anns))
			reqRuby := req
			reqRuby.System = req.System + rubyReminder
			respRuby, errRuby := s.callOnce(ctx, picked, reqRuby, round.Retry)
			if errRuby != nil {
				logger.Warn("ruby_output reminder retry failed",
					"seg", seg.ID, "backend", picked.Name(), "err", errRuby)
			} else {
				atomic.AddInt64(&doc.InputTokens, respRuby.Usage.PromptTokens)
				atomic.AddInt64(&doc.OutputTokens, respRuby.Usage.CompletionTokens)
				_, _, rubyOutputRetry, perrRuby := parseBatchResponse(respRuby.Text, wantIDs)
				if perrRuby != nil {
					if result := parseBatchResponseLenient(respRuby.Text, wantIDs, repairOpts); !result.Fatal && len(result.Missing) == 0 {
						rubyOutputRetry = result.RubyOutput
						perrRuby = nil
					}
				}
				if perrRuby == nil && rubyOutputRetry != nil {
					if ro, ok := rubyOutputRetry[prompt.SingleID]; ok && len(ro) > existingCount {
						if seg.Meta == nil {
							seg.Meta = make(map[string]any)
						}
						seg.Meta["ruby_output"] = ro
						logger.Info("ruby_output recovered by reminder retry",
							"seg", seg.ID, "before", existingCount, "after", len(ro))
					}
				}
			}
		}
	}

	// 先吸收术语并就地修正冲突，再做占位符 normalize / 写回 seg.Target——保证
	// absorbInlineGlossary 能对 trans 做并发冲突修正，避免文档内同一术语翻译不一致。
	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)
	target := trans[prompt.SingleID]
	if repairOpts.PlaceholderNormalize {
		if normText, normalized := repair.NormalizePlaceholders(target, seg.Protected); len(normalized) > 0 {
			logger.Info("placeholders normalized",
				"seg", seg.ID, "normalized", normalized)
			target = normText
		}
	}
	seg.Target = target

	// 占位符完整性校验：缺失则追加补救指令重试一次。
	if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
		logger.Warn("placeholders missing in translation, retrying with reminder",
			"seg", seg.ID, "backend", picked.Name(), "missing", missing)
		var reminder string
		if repairOpts.PromptUpgrade {
			// L4：用 repair 包提供的反例 reminder，包含 missing 列表与上次响应头摘录。
			reminder = repair.BuildRetryReminder(missing, nil, headSnippet(resp.Text, 200))
		} else {
			reminder = fmt.Sprintf(
				"\n\nIMPORTANT: your previous JSON translation omitted these placeholders: %s. "+
					"Reproduce ALL of them verbatim in the translation, preserving their original positions. "+
					"Reply with the same JSON envelope schema as before.",
				strings.Join(missing, ", "))
		}
		req2 := req
		req2.System = req.System + reminder

		resp2, err2 := s.callOnce(ctx, picked, req2, round.Retry)
		if err2 != nil {
			logger.Warn("placeholder retry failed, defer to next round",
				"seg", seg.ID, "backend", picked.Name(), "err", err2)
			seg.Target = ""
			return false, nil
		}
		atomic.AddInt64(&doc.InputTokens, resp2.Usage.PromptTokens)
		atomic.AddInt64(&doc.OutputTokens, resp2.Usage.CompletionTokens)
		trans2, glos2, rubyOutput2, perr2 := parseBatchResponse(resp2.Text, wantIDs)
		if perr2 != nil {
			if result2 := parseBatchResponseLenient(resp2.Text, wantIDs, repairOpts); !result2.Fatal && len(result2.Missing) == 0 {
				trans2, glos2, rubyOutput2 = result2.Trans, result2.Glos, result2.RubyOutput
				perr2 = nil
			}
		}
		if perr2 != nil {
			logger.Warn("placeholder retry response parse failed, defer to next round",
				"seg", seg.ID, "backend", picked.Name(), "err", perr2)
			seg.Target = ""
			return false, nil
		}
		s.absorbInlineGlossary(ctx, glos2, trans2, doc.TargetLang, logger)
		// 占位符补救路径也存储 ruby_output
		if rubyOutput2 != nil {
			if ro, ok := rubyOutput2[prompt.SingleID]; ok && len(ro) > 0 {
				if seg.Meta == nil {
					seg.Meta = make(map[string]any)
				}
				seg.Meta["ruby_output"] = ro
			}
		}
		target2 := trans2[prompt.SingleID]
		if repairOpts.PlaceholderNormalize {
			if normText, normalized := repair.NormalizePlaceholders(target2, seg.Protected); len(normalized) > 0 {
				logger.Info("placeholders normalized after retry",
					"seg", seg.ID, "normalized", normalized)
				target2 = normText
			}
		}
		seg.Target = target2
		if still := protect.MissingPlaceholders(seg); len(still) > 0 {
			logger.Warn("placeholders still missing after retry, defer to next round",
				"seg", seg.ID, "backend", picked.Name(), "missing", still)
			durationMs := time.Since(batchStart).Milliseconds()
			s.emitSingleBatchEvent(idx, seg.ID, picked.Name(), usr, glos, resp, resp.Usage,
				"failed", durationMs, tried, "placeholder_error",
				fmt.Errorf("placeholders still missing: %v", still), logger)
			seg.Target = ""
			return false, nil
		}
	}

	s.addTM(ctx, doc, seg, logger)
	s.reporter().SegmentDone()
	return true, nil
}

// emitSingleBatchEvent emits a BatchEvent for single-segment translation.
func (s *Translate) emitSingleBatchEvent(
	idx int,
	segID string,
	backendName string,
	sentContent string,
	usedGlossary []prompt.GlossaryEntry,
	resp *backend.Response,
	usage backend.Usage,
	status string,
	durationMs int64,
	triedBackends []string,
	errorType string,
	err error,
	logger *slog.Logger,
) {
	rep := s.Reporter
	if rep == nil {
		return
	}
	obs, ok := rep.(progress.BatchObserver)
	if !ok {
		return
	}

	rawRespText := ""
	if resp != nil {
		rawRespText = resp.Text
	}
	var addedGlossary []prompt.BootstrapEntry
	if resp != nil {
		// For single segment, parse the response to get glossary entries.
		_, glosEntries, _, _ := parseBatchResponse(resp.Text, []string{prompt.SingleID})
		addedGlossary = glosEntries
	}

	errorMsg := ""
	if err != nil {
		errorMsg = err.Error()
	}

	evt := progress.BatchEvent{
		Stage:           "translate",
		BatchIndex:      idx,
		SegmentIDs:      []string{segID},
		SegmentCount:    1,
		BackendName:     backendName,
		Status:          status,
		DurationMs:      durationMs,
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		SentContent:     sentContent,
		ReceivedContent: rawRespText,
		UsedGlossary:    usedGlossary,
		AddedGlossary:   addedGlossary,
		ErrorType:       errorType,
		ErrorMessage:    errorMsg,
		TriedBackends:   triedBackends,
	}

	obs.OnBatchEvent(evt)
}

// classifySingleError determines the error type for a failed single-segment translation.
func (s *Translate) classifySingleError(err error) string {
	if err == nil {
		return "backend_error"
	}
	if _, ok := err.(backend.HTTPStatusError); ok {
		return "backend_error"
	}
	return "parse_error"
}
