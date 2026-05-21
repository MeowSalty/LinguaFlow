package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
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
//   - 占位符完整性校验 + 单段补救重试
//   - 单段失败时保留原文 + warn 日志，不阻塞整体
//   - 段级进度上报（Reporter；nil 时 fallback 为 progress.Nop）
//
// 协议：user message 是 JSON envelope（见 prompt 包），模型回复 {"translations":{"<id>":"<text>"}}。
type Translate struct {
	Selector    backend.Selector
	Renderer    *prompt.Renderer
	Glossary    glossary.Glossary
	TM          tm.TranslationMemory
	Limiter     backend.RateLimiter
	Retry       backend.RetryPolicy
	Concurrency int
	BatchSize   int // <=1 表示禁用批量
	Logger      *slog.Logger
	Reporter    progress.Reporter
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
		return s.processBatch(ctx, doc, batches[bidx], logger)
	})
}

// processBatch 处理一批 idx。len==1 或 BatchSize<=1 时走单段路径；
// 否则尝试批量发送，失败则降级为顺序单段。
func (s *Translate) processBatch(ctx context.Context, doc *pipeline.Document, idxs []int, logger *slog.Logger) error {
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
		SourceLang:  doc.SourceLang,
		TargetLang:  doc.TargetLang,
		Segments:    inputs,
		PrevContext: prev,
		NextContext: next,
		Glossary:    glos,
		TMHints:     tmHints,
		Vars:        doc.Vars,
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
		JSONSchema: translationsSchema(wantIDs),
	}

	var resp *backend.Response
	err = backend.WithRetry(ctx, s.Retry, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	if err != nil {
		logger.Warn("batch translate failed, falling back to single-segment",
			"backend", b.Name(), "batch_size", len(idxs), "err", err)
		return s.fallbackSingles(ctx, doc, idxs, logger)
	}

	trans, perr := parseBatchResponse(resp.Text, wantIDs)
	if perr != nil {
		logger.Warn("batch response parse failed, falling back to single-segment",
			"backend", b.Name(), "batch_size", len(idxs), "err", perr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		return s.fallbackSingles(ctx, doc, idxs, logger)
	}

	logger.Debug("batch translated",
		"backend", b.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

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
		SourceLang:  doc.SourceLang,
		TargetLang:  doc.TargetLang,
		Source:      seg.Source,
		PrevContext: prev,
		NextContext: next,
		Glossary:    glos,
		TMHints:     tmHints,
		Vars:        doc.Vars,
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
		JSONSchema: translationsSchema(wantIDs),
	}

	resp, err := s.callOnce(ctx, b, req)
	if err != nil {
		logger.Warn("translate failed, keep source",
			"seg", seg.ID, "backend", b.Name(), "err", err)
		seg.Target = seg.Source
		return nil
	}

	trans, perr := parseBatchResponse(resp.Text, wantIDs)
	if perr != nil {
		logger.Warn("single response parse failed, keep source",
			"seg", seg.ID, "backend", b.Name(), "err", perr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		seg.Target = seg.Source
		return nil
	}
	seg.Target = trans[prompt.SingleID]
	logger.Debug("segment translated",
		"seg", seg.ID, "backend", b.Name(),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

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
		trans2, perr2 := parseBatchResponse(resp2.Text, wantIDs)
		if perr2 != nil {
			logger.Warn("placeholder retry response parse failed, keep source",
				"seg", seg.ID, "backend", b.Name(), "err", perr2)
			seg.Target = seg.Source
			return nil
		}
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
// 容错：模型有时把 JSON 包在 ```json … ``` 围栏里或夹带前后说明文字，
// 这里用 jsonObjectSlice 抽出第一段完整的 JSON 对象。
func parseBatchResponse(text string, wantIDs []string) (map[string]string, error) {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil, fmt.Errorf("no JSON object found in response")
	}
	var env struct {
		Translations map[string]string `json:"translations"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, fmt.Errorf("unmarshal translations: %w", err)
	}
	if env.Translations == nil {
		return nil, errors.New("response missing \"translations\" field")
	}
	for _, id := range wantIDs {
		if _, ok := env.Translations[id]; !ok {
			return nil, fmt.Errorf("missing translation for id %q", id)
		}
	}
	if len(env.Translations) != len(wantIDs) {
		return nil, fmt.Errorf("expected %d translations, got %d", len(wantIDs), len(env.Translations))
	}
	return env.Translations, nil
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
func translationsSchema(wantIDs []string) map[string]any {
	props := make(map[string]any, len(wantIDs))
	for _, id := range wantIDs {
		props[id] = map[string]any{"type": "string"}
	}
	required := make([]string, len(wantIDs))
	copy(required, wantIDs)
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"translations": map[string]any{
				"type":                 "object",
				"properties":           props,
				"required":             required,
				"additionalProperties": false,
			},
		},
		"required":             []string{"translations"},
		"additionalProperties": false,
	}
}

func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
