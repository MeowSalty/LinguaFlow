package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Translate 对每个 Segment 调用 Backend。具备：
//   - worker pool（Concurrency）
//   - 令牌桶限速（Limiter）
//   - 指数退避重试（Retry）
//   - 批量翻译（BatchSize > 1 时把多段拼成一次 LLM 调用）
//   - 批失败时按 FallbackShrink 系数递归缩小子批并发重试（直到收敛到单段）
//   - 占位符完整性校验 + 单段补救重试
//   - 单段失败时保留原文 + warn 日志，不阻塞整体
//   - 段级进度上报（Reporter；nil 时 fallback 为 progress.Nop）
//
// 协议：user message 是 JSON envelope（见 prompt 包），模型回复 {"translations":{"<id>":"<text>"}}。
// 当 InlineBootstrap=true 时，回复同时携带 {"glossary":[{"source","target","notes"},...]}，
// 解析后立刻 Add 到运行时 Glossary；严格合并去重，已存在的 source 不会被覆盖。
type Translate struct {
	Selector       backend.Selector
	Renderer       *prompt.Renderer
	Glossary       glossary.Glossary
	TM             tm.TranslationMemory
	Limiter        backend.RateLimiter
	Retry          backend.RetryPolicy
	Concurrency    int
	BatchSize      int     // <=1 表示禁用批量
	FallbackShrink float64 // (0,1) 启用递归缩小；0 表示失败后直接降到单段
	Logger         *slog.Logger
	Reporter       progress.Reporter

	// Inline 模式：翻译时同时让 LLM 抽术语。
	InlineBootstrap           bool
	MaxBootstrapTermsPerBatch int // 给 prompt 的术语数量上限；<=0 默认 20
	MinBootstrapSourceLen     int // 抽出的术语短于此值则丢弃；<=0 默认 2
	// InlineConflictStrategy 控制并发下后到 worker 提交同 source 不同 target 时的处理：
	//   - config.InlineConflictRewriteLocal（默认）：把本批译文里的冲突 target 字面值
	//     替换为权威表中已有版本，CJK 直替、拉丁系按词边界、歧义仅 Warn 不动。
	//   - config.InlineConflictOff：完全不处理，沿用旧行为。
	// 空字符串视同 off（防止配置未透传时崩溃）。
	InlineConflictStrategy string
}

func (*Translate) Name() string { return "translate" }

// reporter 返回非 nil 的 progress.Reporter；Reporter 字段为空时回退 Nop。
func (s *Translate) reporter() progress.Reporter {
	if s.Reporter == nil {
		return progress.Nop{}
	}
	return s.Reporter
}

func (s *Translate) Run(ctx context.Context, doc *pipeline.Document) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if s.Renderer == nil {
		return errors.New("translate: renderer is nil")
	}
	if s.Selector == nil {
		return errors.New("translate: selector is nil")
	}

	// 先把跳过段（Skip / 空白）直接落 Target，并收集需要翻译的 idx 列表。
	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Skip || strings.TrimSpace(seg.Source) == "" {
			seg.Target = seg.Source
			continue
		}
		pending = append(pending, i)
	}

	bs := max(s.BatchSize, 1)

	// 按 batchSize 切批。批内段在 doc.Segments 中不必连续——
	// 上下文（prev/next）取整批 idx 的最小/最大邻接段。
	var batches [][]int
	for i := 0; i < len(pending); i += bs {
		end := min(i+bs, len(pending))
		batches = append(batches, pending[i:end])
	}

	logger.Info("translating",
		"segments", len(pending),
		"batches", len(batches),
		"concurrency", s.Concurrency,
		"batch_size", bs)

	rep := s.reporter()
	rep.StageStart("translate", len(pending))
	defer rep.StageDone()

	return runConcurrent(ctx, len(batches), s.Concurrency, func(ctx context.Context, bidx int) error {
		return s.processBatchAtSize(ctx, doc, batches[bidx], bs, logger)
	})
}

// processBatchAtSize 处理一批 idx（len(idxs) <= curSize）。len==1 或 BatchSize<=1 时走单段路径；
// 否则尝试批量发送，失败时按 FallbackShrink 缩小子批并发递归，直到收敛到单段。
func (s *Translate) processBatchAtSize(ctx context.Context, doc *pipeline.Document, idxs []int, curSize int, logger *slog.Logger) error {
	if len(idxs) == 1 || s.BatchSize <= 1 {
		return s.translateSingle(ctx, doc, idxs[0], logger)
	}

	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return err
		}
	}

	glos, tmHints := s.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	wantIDs := make([]string, len(idxs))
	for k, idx := range idxs {
		id := strconv.Itoa(k + 1)
		inputs[k] = prompt.SegmentInput{ID: id, Source: doc.Segments[idx].Source}
		wantIDs[k] = id
	}

	minIdx, maxIdx := idxs[0], idxs[len(idxs)-1]
	prev, next := prompt.BuildContextRange(doc, minIdx, maxIdx)

	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Segments:          inputs,
		PrevContext:       prev,
		NextContext:       next,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   s.InlineBootstrap,
		MaxBootstrapTerms: s.maxBootstrapTerms(),
	}
	sys, usr, err := s.Renderer.Render(data)
	if err != nil {
		return fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}

	b, err := s.Selector.Pick(ctx, "")
	if err != nil {
		return err
	}
	req := backend.Request{
		System:     sys,
		User:       usr,
		JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap),
	}

	var resp *backend.Response
	err = backend.WithRetry(ctx, s.Retry, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	if err != nil {
		logger.Warn("batch translate failed, shrinking or falling back",
			"backend", b.Name(), "batch_size", len(idxs), "err", err)
		return s.shrinkOrFallback(ctx, doc, idxs, curSize, logger)
	}

	trans, glosEntries, perr := parseBatchResponse(resp.Text, wantIDs)
	if perr != nil {
		logger.Warn("batch response parse failed, shrinking or falling back",
			"backend", b.Name(), "batch_size", len(idxs), "err", perr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		return s.shrinkOrFallback(ctx, doc, idxs, curSize, logger)
	}

	logger.Debug("batch translated",
		"backend", b.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries))

	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	// 写回并对每段做占位符校验；缺失的段单独补救（走 translateSingle 的 S5 路径）。
	rep := s.reporter()
	for k, idx := range idxs {
		seg := &doc.Segments[idx]
		seg.Target = trans[wantIDs[k]]
		if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
			logger.Warn("batch segment placeholders missing, single-retry",
				"seg", seg.ID, "missing", missing)
			// translateSingle 内部会在结束时上报本段进度；此处不发，避免双计数。
			if err := s.translateSingle(ctx, doc, idx, logger); err != nil {
				return err
			}
			continue
		}
		s.addTM(ctx, doc, seg, logger)
		rep.SegmentDone()
	}
	return nil
}

// shrinkOrFallback 根据 FallbackShrink 决定：
//   - 缩小到 >=2 的子批并发递归（每个子批又可能继续缩小）
//   - 否则坍缩到 fallbackSingles（顺序单段）
func (s *Translate) shrinkOrFallback(ctx context.Context, doc *pipeline.Document, idxs []int, curSize int, logger *slog.Logger) error {
	nextSize := shrinkNext(curSize, s.FallbackShrink)
	if nextSize < 2 {
		return s.fallbackSingles(ctx, doc, idxs, logger)
	}
	var sub [][]int
	for i := 0; i < len(idxs); i += nextSize {
		end := min(i+nextSize, len(idxs))
		sub = append(sub, idxs[i:end])
	}
	logger.Info("shrinking batch and retrying",
		"from", curSize, "to", nextSize, "sub_batches", len(sub), "shrink", s.FallbackShrink)
	return runConcurrent(ctx, len(sub), s.Concurrency, func(ctx context.Context, bidx int) error {
		return s.processBatchAtSize(ctx, doc, sub[bidx], nextSize, logger)
	})
}

// shrinkNext 计算下一级 batch 大小。
//   - shrink <= 0 或 NaN/Inf：返回 0（调用方据此走 fallbackSingles）
//   - shrink >= 1：返回 0（Validate 本应已拦截，但保险起见）
//   - 否则 next = floor(cur * shrink)；若 >= cur 则强制 cur-1，避免不收敛
//   - next < 2 也返回 0（再缩等同单段，由调用方坍缩处理）
func shrinkNext(cur int, shrink float64) int {
	if shrink <= 0 || shrink >= 1 || math.IsNaN(shrink) || math.IsInf(shrink, 0) {
		return 0
	}
	next := int(math.Floor(float64(cur) * shrink))
	if next >= cur {
		next = cur - 1
	}
	if next < 2 {
		return 0
	}
	return next
}

// fallbackSingles 顺序对 idxs 中每段调 translateSingle。
func (s *Translate) fallbackSingles(ctx context.Context, doc *pipeline.Document, idxs []int, logger *slog.Logger) error {
	for _, idx := range idxs {
		if err := s.translateSingle(ctx, doc, idx, logger); err != nil {
			return err
		}
	}
	return nil
}

// translateSingle 翻译单段（走 JSON 协议，含 S5 占位符补救）。
// 任何 return nil 路径都表示这段处理结束（无论译完、保留原文，还是补救失败），
// 因此函数末尾通过 defer 上报一次进度；返回非 nil error 则不计入进度（stage 终止）。
func (s *Translate) translateSingle(ctx context.Context, doc *pipeline.Document, idx int, logger *slog.Logger) (retErr error) {
	defer func() {
		if retErr == nil {
			s.reporter().SegmentDone()
		}
	}()

	seg := &doc.Segments[idx]

	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return err
		}
	}

	glos, tmHints := s.lookupHints(ctx, doc, []int{idx}, logger)
	prev, next := prompt.BuildContext(doc, idx)

	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Source:            seg.Source,
		PrevContext:       prev,
		NextContext:       next,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   s.InlineBootstrap,
		MaxBootstrapTerms: s.maxBootstrapTerms(),
	}
	sys, usr, err := s.Renderer.Render(data)
	if err != nil {
		return fmt.Errorf("render prompt for seg %s: %w", seg.ID, err)
	}

	b, err := s.Selector.Pick(ctx, "")
	if err != nil {
		return err
	}
	wantIDs := []string{prompt.SingleID}
	req := backend.Request{
		System:     sys,
		User:       usr,
		JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap),
	}

	resp, err := s.callOnce(ctx, b, req)
	if err != nil {
		logger.Warn("translate failed, keep source",
			"seg", seg.ID, "backend", b.Name(), "err", err)
		seg.Target = seg.Source
		return nil
	}

	trans, glosEntries, perr := parseBatchResponse(resp.Text, wantIDs)
	if perr != nil {
		logger.Warn("single response parse failed, keep source",
			"seg", seg.ID, "backend", b.Name(), "err", perr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		seg.Target = seg.Source
		return nil
	}
	logger.Debug("segment translated",
		"seg", seg.ID, "backend", b.Name(),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries))
	// 先吸收术语并就地修正冲突，再写回 seg.Target——保证 absorbInlineGlossary 能
	// 对 trans 做并发冲突修正，避免文档内同一术语翻译不一致。
	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)
	seg.Target = trans[prompt.SingleID]

	// 占位符完整性校验：缺失则追加补救指令重试一次。
	if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
		logger.Warn("placeholders missing in translation, retrying with reminder",
			"seg", seg.ID, "backend", b.Name(), "missing", missing)
		reminder := fmt.Sprintf(
			"\n\nIMPORTANT: your previous JSON translation omitted these placeholders: %s. "+
				"Reproduce ALL of them verbatim in the translation, preserving their original positions. "+
				"Reply with the same JSON envelope schema as before.",
			strings.Join(missing, ", "))
		req2 := req
		req2.System = req.System + reminder

		resp2, err2 := s.callOnce(ctx, b, req2)
		if err2 != nil {
			logger.Warn("placeholder retry failed, keep source",
				"seg", seg.ID, "backend", b.Name(), "err", err2)
			seg.Target = seg.Source
			return nil
		}
		trans2, glos2, perr2 := parseBatchResponse(resp2.Text, wantIDs)
		if perr2 != nil {
			logger.Warn("placeholder retry response parse failed, keep source",
				"seg", seg.ID, "backend", b.Name(), "err", perr2)
			seg.Target = seg.Source
			return nil
		}
		s.absorbInlineGlossary(ctx, glos2, trans2, doc.TargetLang, logger)
		seg.Target = trans2[prompt.SingleID]
		if still := protect.MissingPlaceholders(seg); len(still) > 0 {
			logger.Warn("placeholders still missing after retry, keep source",
				"seg", seg.ID, "backend", b.Name(), "missing", still)
			seg.Target = seg.Source
			return nil
		}
	}

	s.addTM(ctx, doc, seg, logger)
	return nil
}

func (s *Translate) callOnce(ctx context.Context, b backend.Backend, req backend.Request) (*backend.Response, error) {
	var resp *backend.Response
	err := backend.WithRetry(ctx, s.Retry, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	return resp, err
}

// lookupHints 为 idxs 中每段查 glossary / TM 并合并去重。
// glossary 以 source+target 为键；TM 同样以 source+target 去重，保留最高分。
func (s *Translate) lookupHints(ctx context.Context, doc *pipeline.Document, idxs []int, logger *slog.Logger) ([]prompt.GlossaryEntry, []prompt.TMHint) {
	var (
		glosOrder []string
		glosMap   = map[string]prompt.GlossaryEntry{}
		tmOrder   []string
		tmMap     = map[string]prompt.TMHint{}
	)
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		if s.Glossary != nil {
			hits, err := s.Glossary.Lookup(ctx, seg.Source, doc.SourceLang, doc.TargetLang)
			if err != nil {
				logger.Warn("glossary lookup failed", "err", err, "seg", seg.ID)
			}
			for _, h := range hits {
				key := h.Source + "\x00" + h.Target
				if _, ok := glosMap[key]; !ok {
					glosOrder = append(glosOrder, key)
				}
				glosMap[key] = prompt.GlossaryEntry{Source: h.Source, Target: h.Target, Notes: h.Notes}
			}
		}
		if s.TM != nil {
			ms, err := s.TM.Search(ctx, seg.Source, doc.SourceLang, doc.TargetLang)
			if err != nil {
				logger.Warn("tm search failed", "err", err, "seg", seg.ID)
			}
			for _, m := range ms {
				key := m.Source + "\x00" + m.Target
				if old, ok := tmMap[key]; !ok {
					tmOrder = append(tmOrder, key)
					tmMap[key] = prompt.TMHint{Source: m.Source, Target: m.Target, Score: m.Score}
				} else if m.Score > old.Score {
					tmMap[key] = prompt.TMHint{Source: m.Source, Target: m.Target, Score: m.Score}
				}
			}
		}
	}
	glos := make([]prompt.GlossaryEntry, 0, len(glosOrder))
	for _, k := range glosOrder {
		glos = append(glos, glosMap[k])
	}
	hints := make([]prompt.TMHint, 0, len(tmOrder))
	for _, k := range tmOrder {
		hints = append(hints, tmMap[k])
	}
	return glos, hints
}

func (s *Translate) addTM(ctx context.Context, doc *pipeline.Document, seg *pipeline.Segment, logger *slog.Logger) {
	if s.TM == nil {
		return
	}
	if err := s.TM.Add(ctx, seg.Source, seg.Target, doc.SourceLang, doc.TargetLang); err != nil {
		logger.Debug("tm add failed", "err", err)
	}
}

// parseBatchResponse 解析 {"translations":{"<id>":"<text>", ...}} 并校验 wantIDs 完整。
// 当响应携带 inline 抽取的 {"glossary":[...]} 时，一并解析返回；缺失视作空切片。
// 容错：模型有时把 JSON 包在 ```json … ``` 围栏里或夹带前后说明文字，
// 这里用 jsonObjectSlice 抽出第一段完整的 JSON 对象。
func parseBatchResponse(text string, wantIDs []string) (map[string]string, []prompt.BootstrapEntry, error) {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil, nil, fmt.Errorf("no JSON object found in response")
	}
	var env struct {
		Translations map[string]string       `json:"translations"`
		Glossary     []prompt.BootstrapEntry `json:"glossary"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, nil, fmt.Errorf("unmarshal translations: %w", err)
	}
	if env.Translations == nil {
		return nil, nil, errors.New("response missing \"translations\" field")
	}
	for _, id := range wantIDs {
		if _, ok := env.Translations[id]; !ok {
			return nil, nil, fmt.Errorf("missing translation for id %q", id)
		}
	}
	if len(env.Translations) != len(wantIDs) {
		return nil, nil, fmt.Errorf("expected %d translations, got %d", len(wantIDs), len(env.Translations))
	}
	return env.Translations, env.Glossary, nil
}

// jsonObjectSlice 从 text 中截取首个 { 到与之配对的 } 之间的子串。
// 支持字符串里的转义和大括号，跳过 ``` 围栏；找不到返回空串。
func jsonObjectSlice(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}

// translationsSchema 按 wantIDs 生成 OpenAI 严格 JSON Schema：
// 要求 translations 下的属性集合与 wantIDs 完全一致。
// 当 includeGlossary=true 时，在外层属性里再加一个 "glossary" 数组，要求 items 严格匹配
// {source,target,notes}；外层 required 同步加入 "glossary"。
func translationsSchema(wantIDs []string, includeGlossary bool) map[string]any {
	props := make(map[string]any, len(wantIDs))
	for _, id := range wantIDs {
		props[id] = map[string]any{"type": "string"}
	}
	required := make([]string, len(wantIDs))
	copy(required, wantIDs)
	outerProps := map[string]any{
		"translations": map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		},
	}
	outerRequired := []string{"translations"}
	if includeGlossary {
		outerProps["glossary"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source": map[string]any{"type": "string"},
					"target": map[string]any{"type": "string"},
					"notes":  map[string]any{"type": "string"},
				},
				"required":             []string{"source", "target", "notes"},
				"additionalProperties": false,
			},
		}
		outerRequired = append(outerRequired, "glossary")
	}
	return map[string]any{
		"type":                 "object",
		"properties":           outerProps,
		"required":             outerRequired,
		"additionalProperties": false,
	}
}

// maxBootstrapTerms 返回传给 prompt 的 inline 术语上限；<=0 用默认 20。
func (s *Translate) maxBootstrapTerms() int {
	if s.MaxBootstrapTermsPerBatch > 0 {
		return s.MaxBootstrapTermsPerBatch
	}
	return 20
}

// absorbInlineGlossary 把 LLM 在 translate 响应中携带的 glossary 条目写入运行时 Glossary，
// 并在并发冲突时就地修正本批 translations，避免文档内同一术语翻译不一致。
//
// 工作流：过滤候选 → 批量 Add → 处理 Skipped。FileGlossary 的 First-Wins 严格合并会让
// 后到 worker 提交的 source 被丢弃，但其本批译文已经写了被丢弃的 target；这里通过
// glossary.SafeReplace 把这些字面值改写为权威表里的版本。CJK 直替、拉丁系按词边界、
// 歧义场景仅 Warn 不动。InlineConflictStrategy == off 时跳过修正，沿用旧行为。
//
// translations 会被原地改写——调用方必须在拿到本函数返回后再写回 doc.Segments[*].Target。
func (s *Translate) absorbInlineGlossary(
	ctx context.Context,
	entries []prompt.BootstrapEntry,
	translations map[string]string,
	targetLang string,
	logger *slog.Logger,
) {
	if !s.InlineBootstrap || len(entries) == 0 || s.Glossary == nil {
		return
	}
	minLen := s.MinBootstrapSourceLen
	if minLen < 1 {
		minLen = 2
	}
	candidates := make([]glossary.Entry, 0, len(entries))
	for _, e := range entries {
		if len([]rune(e.Source)) < minLen {
			continue
		}
		if e.Source == "" || e.Target == "" {
			continue
		}
		candidates = append(candidates, glossary.Entry{
			Source: e.Source,
			Target: e.Target,
			Notes:  e.Notes,
		})
	}
	if len(candidates) == 0 {
		return
	}
	result, err := s.Glossary.Add(ctx, candidates...)
	if err != nil {
		// FileGlossary 现实现不会返 error，但为接口健壮考虑保留分支：err 不阻断翻译。
		logger.Warn("inline glossary add failed", "err", err)
	}
	if len(result.Added) > 0 {
		logger.Debug("inline glossary absorbed",
			"added", len(result.Added),
			"skipped", len(result.Skipped),
			"received", len(entries))
	}

	if s.InlineConflictStrategy != config.InlineConflictRewriteLocal {
		return
	}
	if len(result.Skipped) == 0 || len(translations) == 0 {
		return
	}
	s.rewriteConflictsInBatch(result.Skipped, translations, targetLang, logger)
}

// rewriteConflictsInBatch 遍历 Skipped 列表，把本批译文里 worker 自己用的 target 字面值
// 替换为权威表里已有的版本。仅处理 Reason == SkipReasonExists 且 target 不同的项。
func (s *Translate) rewriteConflictsInBatch(
	skipped []glossary.SkippedEntry,
	translations map[string]string,
	targetLang string,
	logger *slog.Logger,
) {
	for _, sk := range skipped {
		if sk.Reason != glossary.SkipReasonExists {
			continue
		}
		from := sk.Proposed.Target
		to := sk.Existing.Target
		if from == "" || from == to {
			continue
		}
		rewrote := 0
		var warns []string
		for id, text := range translations {
			newText, replaced, warn := glossary.SafeReplace(text, from, to, targetLang)
			if replaced {
				translations[id] = newText
				rewrote++
			}
			if warn != "" {
				warns = append(warns, warn)
			}
		}
		if rewrote > 0 {
			logger.Info("inline glossary conflict: rewrote local target",
				"source", sk.Proposed.Source,
				"from", from,
				"to", to,
				"rewrites", rewrote)
		}
		if len(warns) > 0 {
			logger.Warn("inline glossary conflict: ambiguous match",
				"source", sk.Proposed.Source,
				"proposed_target", from,
				"authoritative_target", to,
				"details", warns)
		}
	}
}

func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
