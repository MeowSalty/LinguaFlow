package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

const (
	RoundModeTranslate = "translate"
)

// PostprocessConfig 是 pipeline 级别的后处理配置。
type PostprocessConfig struct {
	TrimSpaces bool
}

// Round 描述一轮翻译的执行配置（纯数据，无后端名称引用）。
type Round struct {
	Name             string
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	Concurrency      int
	FallbackShrink   float64
	Retry            backend.RetryPolicy

	Renderer *prompt.Renderer
	Repair   *repair.Options

	ResponseMode string

	Mode              string
	Protector         protect.Protector
	RubyEnabled       bool
	RubyPreserveKinds []string
	RubyMode          string
	Context           *ContextConfig
	Postprocess       *PostprocessConfig
}

// batchJob 描述一个待处理的批次任务。
type batchJob struct {
	idxs    []int
	attempt int // 已消耗的重试次数
}

// batchResult 描述一个批次的处理结果。
type batchResult struct {
	unresolved []int     // 需要下一轮处理
	missing    []int     // 需要 round 级重新分批
	retry      *batchJob // 需要重新入队（缩批或退避后重试）
}

// RoundExecutor 对每个 Segment 调用 Backend，执行单轮翻译。
type RoundExecutor struct {
	Round    Round
	Renderer *prompt.Renderer
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
	Logger   *slog.Logger
	Reporter progress.Reporter

	RubyRestorer      *ruby.Restorer
	RubyRetryBackends []backend.Backend

	InlineBootstrap        bool
	MaxTermsPer1000Chars   float64
	MinBootstrapSourceLen  int
	InlineConflictStrategy string

	Repair  repair.Options
	Context ContextConfig // 全局兜底

	BatchHandler func(ctx context.Context, result BatchResult) error
}

// reporter 返回非 nil 的 progress.Reporter；Reporter 字段为空时回退 Nop。
func (s *RoundExecutor) reporter() progress.Reporter {
	if s.Reporter == nil {
		return progress.Nop{}
	}
	return s.Reporter
}

func (s *RoundExecutor) Run(ctx context.Context, doc *Document) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if s.Renderer == nil {
		return errors.New("round_executor: renderer is nil")
	}

	round := s.Round
	mode := round.Mode
	if mode == "" {
		mode = RoundModeTranslate
	}

	// 1. 收集 pending（Translate=true 的段落）
	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Skip {
			seg.Target = seg.Source
			continue
		}
		if !seg.Translate {
			continue
		}
		if strings.TrimSpace(seg.Source) == "" || isPlaceholderOnly(seg) || isDecorativeSeparator(seg) {
			seg.Target = seg.Source
			continue
		}
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		return nil
	}

	rep := s.reporter()
	rep.StageStart(mode, len(pending))
	defer rep.StageDone()

	// 2. Protect（仅 translate 模式）
	switch mode {
	case RoundModeTranslate:
		if round.Protector != nil {
			for _, idx := range pending {
				seg := &doc.Segments[idx]
				if seg.OriginalSource == "" {
					seg.OriginalSource = seg.Source
				}
				if err := round.Protector.Protect(seg); err != nil {
					return fmt.Errorf("protect segment %d: %w", idx, err)
				}
			}
		}
	default:
		return fmt.Errorf("unsupported round mode: %s", mode)
	}

	// 3. 上下文窗口
	ctxConfig := s.Context
	if round.Context != nil {
		ctxConfig = *round.Context
	}
	ctxWindow := max(ctxConfig.Before, ctxConfig.After)
	if !ctxConfig.Enabled {
		ctxWindow = 0
	}

	// 4. 分批
	constraint := BatchConstraint{
		MaxSegments: round.BatchSize,
		MaxWords:    round.MaxWordsPerBatch,
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
		constraint.MaxSegments = 1
	}
	batches := BuildContextAwareBatches(doc, pending, constraint, ctxWindow, ctxConfig.Enabled)
	logger.Info("round start",
		"name", round.Name,
		"pending", len(pending), "batches", len(batches),
		"batch_size", round.BatchSize, "max_words_per_batch", round.MaxWordsPerBatch,
		"concurrency", round.Concurrency,
		"context_enabled", ctxConfig.Enabled, "context_window", ctxWindow)

	// 5. 执行
	nextPending, missingSegs, roundErr := s.runRound(ctx, doc, batches, round, logger, ctxWindow)
	if roundErr != nil {
		return roundErr
	}

	// 6. 缺失段重新分批
	if len(missingSegs) > 0 && ctx.Err() == nil {
		sort.Ints(missingSegs)
		logger.Info("retrying missing segments", "missing", len(missingSegs))
		retryBatches := BuildContextAwareBatches(doc, missingSegs, constraint, ctxWindow, ctxConfig.Enabled)
		retryPending, retryMissing, retryErr := s.runRound(ctx, doc, retryBatches, round, logger, ctxWindow)
		if retryErr != nil {
			return retryErr
		}
		nextPending = append(nextPending, retryPending...)
		nextPending = append(nextPending, retryMissing...)
	}

	sort.Ints(nextPending)
	logger.Info("round done",
		"name", round.Name,
		"resolved", len(pending)-len(nextPending),
		"pending_next", len(nextPending))

	if len(nextPending) > 0 {
		failedIndices := make([]string, 0, len(nextPending))
		for _, idx := range nextPending {
			failedIndices = append(failedIndices, strconv.Itoa(idx))
		}
		if doc.Vars == nil {
			doc.Vars = map[string]any{}
		}
		doc.Vars["_translate_failed_indices"] = strings.Join(failedIndices, ",")
		logger.Warn("translate round exhausted", "count", len(nextPending))
	} else {
		if doc.Vars != nil {
			delete(doc.Vars, "_translate_failed_indices")
		}
	}
	return nil
}

// expandBatchWithContext 为批次扩展上下文段落。
func expandBatchWithContext(doc *Document, idxs []int, totalSegments, ctxWindow int) []int {
	if ctxWindow <= 0 || len(idxs) == 0 {
		return idxs
	}
	firstIdx, lastIdx := idxs[0], idxs[len(idxs)-1]
	expandFrom := max(firstIdx-ctxWindow, 0)
	expandTo := min(lastIdx+ctxWindow, totalSegments-1)
	expanded := make([]int, 0, expandTo-expandFrom+1)
	for i := expandFrom; i <= expandTo; i++ {
		seg := &doc.Segments[i]
		if seg.Skip {
			continue
		}
		if isPlaceholderOnly(seg) || isDecorativeSeparator(seg) || strings.TrimSpace(seg.Source) == "" {
			continue
		}
		expanded = append(expanded, i)
	}
	return expanded
}

func buildContextSet(expandedIdxs []int, batchSet map[int]struct{}) map[int]struct{} {
	ctxSet := make(map[int]struct{})
	for _, idx := range expandedIdxs {
		if _, inBatch := batchSet[idx]; !inBatch {
			ctxSet[idx] = struct{}{}
		}
	}
	return ctxSet
}

func buildBatchResult(doc *Document, idxs []int, contextSet map[int]struct{}) BatchResult {
	translated := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		if isContext(contextSet, idx) {
			continue
		}
		source := seg.OriginalSource
		if source == "" {
			source = seg.Source
		}
		translated = append(translated, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: source,
			TargetText: seg.Target,
			Failed:     seg.Target == "",
			Meta:       seg.Meta,
		})
	}
	return BatchResult{Segments: translated}
}

func (s *RoundExecutor) resolveRoundRenderer(round Round) *prompt.Renderer {
	if round.Renderer != nil {
		return round.Renderer
	}
	return s.Renderer
}

func (s *RoundExecutor) resolveRoundRepair(round Round) repair.Options {
	if round.Repair != nil {
		return *round.Repair
	}
	return s.Repair
}

func (s *RoundExecutor) callOnce(ctx context.Context, b backend.Backend, req backend.Request) (*backend.Response, error) {
	return b.Translate(ctx, req)
}

func (s *RoundExecutor) runRound(ctx context.Context, doc *Document, batches [][]int,
	round Round, logger *slog.Logger, contextWindow int) (nextPending []int, missingSegs []int, err error) {

	totalAttempts := round.Retry.MaxAttempts + 1
	jobs := make(chan batchJob, round.Concurrency*2)
	results := make(chan batchResult, round.Concurrency*2)

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	var handlerErr atomic.Value
	var pendingMu sync.Mutex

	var wg sync.WaitGroup
	for w := 0; w < round.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				if runCtx.Err() != nil {
					pendingMu.Lock()
					nextPending = append(nextPending, filterPendingIdxs(job.idxs, nil)...)
					pendingMu.Unlock()
					continue
				}

				batchSet := make(map[int]struct{}, len(job.idxs))
				for _, idx := range job.idxs {
					batchSet[idx] = struct{}{}
				}

				expandedIdxs := expandBatchWithContext(doc, job.idxs, len(doc.Segments), contextWindow)
				contextSet := buildContextSet(expandedIdxs, batchSet)

				result := s.processBatchAttempt(runCtx, doc, job, round, logger, contextSet, expandedIdxs)

				if s.BatchHandler != nil {
					handlerBatchResult := buildBatchResult(doc, expandedIdxs, contextSet)
					if herr := s.BatchHandler(runCtx, handlerBatchResult); herr != nil {
						logger.Error("batch handler error, terminating round", "err", herr)
						handlerErr.Store(herr)
						runCancel()
						results <- batchResult{unresolved: filterPendingIdxs(job.idxs, contextSet)}
						continue
					}
				}

				results <- result
			}
		}()
	}

	done := make(chan struct{})
	var submitWg sync.WaitGroup
	submitWg.Add(1)
	go func() {
		defer submitWg.Done()
		for _, batch := range batches {
			select {
			case <-done:
				return
			case jobs <- batchJob{idxs: batch, attempt: 0}:
			}
		}
	}()

	active := len(batches)
	for active > 0 {
		select {
		case <-runCtx.Done():
			goto cleanup
		case result := <-results:
			pendingMu.Lock()
			nextPending = append(nextPending, result.unresolved...)
			pendingMu.Unlock()

			if result.retry != nil && result.retry.attempt < totalAttempts {
				select {
				case <-runCtx.Done():
					pendingMu.Lock()
					nextPending = append(nextPending, result.retry.idxs...)
					pendingMu.Unlock()
					missingSegs = append(missingSegs, result.missing...)
					active--
				case jobs <- *result.retry:
				}
			} else {
				if result.retry != nil {
					pendingMu.Lock()
					nextPending = append(nextPending, result.retry.idxs...)
					pendingMu.Unlock()
				}
				missingSegs = append(missingSegs, result.missing...)
				active--
			}
		}
	}

cleanup:
	close(done)
	submitWg.Wait()
	close(jobs)
	wg.Wait()
	if v := handlerErr.Load(); v != nil {
		return nextPending, missingSegs, v.(error)
	}
	return nextPending, missingSegs, nil
}

func CountWords(text string) int {
	count := 0
	inWord := false
	for _, r := range text {
		if isCJK(r) {
			count++
			inWord = false
		} else if unicode.IsSpace(r) {
			inWord = false
		} else {
			if !inWord {
				count++
				inWord = true
			}
		}
	}
	return count
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

func (s *RoundExecutor) calcMaxBootstrapTerms(segments []string) int {
	coeff := s.MaxTermsPer1000Chars
	if coeff <= 0 {
		coeff = 3.0
	}
	totalWords := 0
	for _, seg := range segments {
		totalWords += CountWords(seg)
	}
	maxTerms := int(math.Ceil(float64(totalWords) / 1000.0 * coeff))
	return max(maxTerms, 1)
}

func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func isDecorativeSeparator(seg *Segment) bool {
	text := strings.TrimSpace(seg.Source)
	if text == "" {
		return false
	}
	text = strings.ReplaceAll(text, " ", "")
	text = strings.ReplaceAll(text, "\t", "")
	text = strings.ReplaceAll(text, "\n", "")
	text = strings.ReplaceAll(text, "\r", "")
	if text == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isPlaceholderOnly(seg *Segment) bool {
	if len(seg.Protected) == 0 {
		return false
	}
	text := seg.Source
	for key := range seg.Protected {
		text = strings.ReplaceAll(text, key, "")
	}
	return strings.TrimSpace(text) == ""
}

func extractRubyAnnotationsFromDoc(doc *Document, idxs []int, idMap map[int]string) map[string][]prompt.RubyAnnotation {
	result := make(map[string][]prompt.RubyAnnotation)
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		raw, ok := seg.Meta["ruby_annotations"]
		if !ok {
			continue
		}
		annots, ok := raw.([]ruby.Annotation)
		if !ok {
			continue
		}
		converted := make([]prompt.RubyAnnotation, len(annots))
		for i, a := range annots {
			converted[i] = prompt.RubyAnnotation{Base: a.Base, Text: a.Text}
		}
		if len(converted) > 0 {
			key := seg.ID
			if idMap != nil {
				if mapped, ok := idMap[idx]; ok {
					key = mapped
				}
			}
			result[key] = converted
		}
	}
	return result
}
