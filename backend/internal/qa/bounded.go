package qa

import (
	"context"
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func init() {
	for _, re := range []*regexp.Regexp{htmlTagRe, numberTokenRe, urlTokenRe, emailTokenRe, xmlTagTokenRe, xmlTagNameRe, leftoverPlaceholderRe, placeholderRe} {
		backend.RegisterFixedRegexp(re)
	}
}

// RunBounded runs the finite built-in rules under the caller's construction
// budget. It does not acquire a CPU permit or invoke arbitrary checker plugins.
func (e *Engine) RunBounded(ctx context.Context, b *backend.BuildBounds, inputs []CheckInput) ([]QualityIssue, error) {
	if !e.config.Enabled || len(inputs) == 0 || len(e.checkers) == 0 {
		return nil, nil
	}
	if b == nil {
		return nil, backend.ErrBuildCapacity
	}
	if err := b.ReserveNodes(int64(len(inputs)), backend.MaxNodeAllocationBytes); err != nil {
		return nil, err
	}
	if err := b.ReserveNodes(3, backend.MaxNodeAllocationBytes); err != nil {
		return nil, err
	}
	q := boundedQA{ctx: ctx, b: b, inputs: inputs, prepared: make([]boundedInput, len(inputs))}
	for _, checker := range e.checkers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q.check(checker)
		if q.err != nil {
			return nil, q.err
		}
	}
	return q.issues, nil
}

type boundedQA struct {
	ctx            context.Context
	b              *backend.BuildBounds
	inputs         []CheckInput
	prepared       []boundedInput
	issues         []QualityIssue
	err            error
	visitRemainder int64
}
type boundedInput struct {
	sourceRegions, targetRegions                                               [][2]int
	sourcePlain, targetPlain, sourceClean, targetClean, sourceBase, targetBase string
	regionsReady, plainReady, cleanReady, baseReady                            bool
}

func (q *boundedQA) nodes(n int64) bool {
	if q.err == nil {
		q.err = q.b.ReserveNodes(n, backend.MaxNodeAllocationBytes)
	}
	return q.err == nil
}
func (q *boundedQA) message(parts ...string) string {
	if q.err != nil {
		return ""
	}
	var n int64
	for _, p := range parts {
		if int64(len(p)) > math.MaxInt64-n {
			q.err = backend.ErrBuildCapacity
			return ""
		}
		n += int64(len(p))
	}
	if n > int64(math.MaxInt) {
		q.err = backend.ErrBuildCapacity
		return ""
	}
	if q.err = q.b.ReserveText(n, n); q.err != nil {
		return ""
	}
	var out strings.Builder
	out.Grow(int(n))
	for _, p := range parts {
		out.WriteString(p)
	}
	return out.String()
}
func (q *boundedQA) decimal(n int) string {
	if q.err != nil {
		return ""
	}
	if q.err = q.b.ReserveText(20, 20); q.err != nil {
		return ""
	}
	return strconv.Itoa(n)
}
func (q *boundedQA) floating(n float64, precision int) string {
	if q.err != nil {
		return ""
	}
	if !q.nodes(3) {
		return ""
	}
	if precision < 0 || precision > 2 {
		q.err = backend.ErrBuildCapacity
		return ""
	}
	// float64 has at most 309 decimal integer digits. With sign/dot and the
	// fixed <=2 fractional digits, U=320 covers every output. Append growth
	// (<2U), its old backing (<=U), and FormatFloat's final string (<=U)
	// each receive their separately rounded credit before strconv runs.
	for _, capacity := range [...]int64{640, 320, 320} {
		if q.err = q.b.ReserveText(capacity, capacity); q.err != nil {
			return ""
		}
	}
	return strconv.FormatFloat(n, 'f', precision, 64)
}
func (q *boundedQA) quote(s string) string {
	if q.err != nil {
		return ""
	}
	if len(s) > 4 {
		q.err = backend.ErrBuildCapacity
		return ""
	}
	for range 3 {
		if q.err = q.b.ReserveText(32, 32); q.err != nil {
			return ""
		}
	}
	return strconv.Quote(s)
}
func (q *boundedQA) lower(s string) string {
	if q.err != nil {
		return ""
	}
	n, changed := 0, false
	for _, r := range s {
		v := unicode.ToLower(r)
		size := utf8.RuneLen(v)
		if n > math.MaxInt-size {
			q.err = backend.ErrBuildCapacity
			return ""
		}
		n += size
		changed = changed || v != r
	}
	if !changed && utf8.ValidString(s) {
		return s
	}
	if q.err = q.b.ReserveText(int64(n), int64(n)); q.err != nil {
		return ""
	}
	var out strings.Builder
	out.Grow(n)
	for _, r := range s {
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}
func (q *boundedQA) matches(re *regexp.Regexp, text string, visit func(string, int, int)) {
	for from := 0; from <= len(text) && q.err == nil; {
		if q.err = q.ctx.Err(); q.err != nil {
			return
		}
		var loc []int
		loc, q.err = backend.FindRegexpIndex(q.b, re, text[from:])
		if q.err != nil || loc == nil {
			return
		}
		start, end := from+loc[0], from+loc[1]
		visit(text[start:end], start, end)
		if end <= from {
			q.err = backend.ErrBuildCapacity
			return
		}
		from = end
	}
}
func isCapacity(err error) bool { return errors.Is(err, backend.ErrBuildCapacity) }

// Bound non-allocating product searches too: each 64 UTF-8 comparisons consume
// one logical visit from the caller's existing node budget, without allocating
// an object or introducing another ledger. At most 63 comparisons remain.
func (q *boundedQA) visit(n int64) bool {
	if q.err != nil {
		return false
	}
	if q.err = q.ctx.Err(); q.err != nil {
		return false
	}
	if n < 0 || n > math.MaxInt64-q.visitRemainder {
		q.err = backend.ErrBuildCapacity
		return false
	}
	total := n + q.visitRemainder
	q.visitRemainder = total % 64
	if total >= 64 {
		q.err = q.b.ReserveNodes(total/64, 0)
	}
	return q.err == nil
}
func (q *boundedQA) appendRegion(out [][2]int, start, end int) [][2]int {
	if q.err == nil {
		out, q.err = backend.AppendNode(q.b, out, [2]int{start, end})
	}
	return out
}
func (q *boundedQA) regions(text string, protected map[string]string) [][2]int {
	if !q.nodes(int64(len(protected))) || !q.nodes(2) {
		return nil
	}
	seen := make(map[string]struct{}, len(protected))
	var out [][2]int
	for _, value := range protected {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		from, runeFrom := 0, 0
		for from <= len(text) && q.err == nil {
			if !q.visit(int64(len(text) - from)) {
				return nil
			}
			rel := strings.Index(text[from:], value)
			if rel < 0 {
				break
			}
			start := from + rel
			end := start + len(value)
			runeStart := runeFrom + utf8.RuneCountInString(text[from:start])
			runeEnd := runeStart + utf8.RuneCountInString(value)
			out = q.appendRegion(out, runeStart, runeEnd)
			from, runeFrom = end, runeEnd
		}
	}
	var rubySpans [][2]int
	if q.err == nil {
		rubySpans, q.err = ruby.ElementSpansBounded(q.b, text)
	}
	from, runeFrom := 0, 0
	for _, span := range rubySpans {
		start := runeFrom + utf8.RuneCountInString(text[from:span[0]])
		end := start + utf8.RuneCountInString(text[span[0]:span[1]])
		out = q.appendRegion(out, start, end)
		from, runeFrom = span[1], end
	}
	from, runeFrom = 0, 0
	q.matches(htmlTagRe, text, func(_ string, start, end int) {
		runeStart := runeFrom + utf8.RuneCountInString(text[from:start])
		runeEnd := runeStart + utf8.RuneCountInString(text[start:end])
		out = q.appendRegion(out, runeStart, runeEnd)
		from, runeFrom = end, runeEnd
	})
	slices.SortFunc(out, func(a, z [2]int) int {
		if a[0] < z[0] {
			return -1
		}
		if a[0] > z[0] {
			return 1
		}
		if a[1] > z[1] {
			return -1
		}
		if a[1] < z[1] {
			return 1
		}
		return 0
	})
	at := 0
	for _, r := range out {
		if at == 0 || r[0] >= out[at-1][1] {
			out[at] = r
			at++
		} else if r[1] > out[at-1][1] {
			out[at-1][1] = r[1]
		}
	}
	return out[:at]
}
func (q *boundedQA) strip(text string, regions [][2]int) string {
	if len(regions) == 0 {
		return text
	}
	count := 0
	region := 0
	index := 0
	for _, r := range text {
		for region < len(regions) && index >= regions[region][1] {
			region++
		}
		if region == len(regions) || index < regions[region][0] {
			size := utf8.RuneLen(r)
			if count > math.MaxInt-size {
				q.err = backend.ErrBuildCapacity
				return ""
			}
			count += size
		}
		index++
	}
	if q.err != nil {
		return ""
	}
	if q.err = q.b.ReserveText(int64(count), int64(count)); q.err != nil {
		return ""
	}
	var out strings.Builder
	out.Grow(count)
	region, index = 0, 0
	for _, r := range text {
		for region < len(regions) && index >= regions[region][1] {
			region++
		}
		if region == len(regions) || index < regions[region][0] {
			out.WriteRune(r)
		}
		index++
	}
	return out.String()
}
func (q *boundedQA) noPlaceholders(text string) string {
	var spans [][2]int
	q.matches(placeholderRe, text, func(_ string, start, end int) { spans = q.appendRegion(spans, start, end) })
	if q.err != nil {
		return ""
	}
	if len(spans) == 0 {
		return text
	}
	n := len(text)
	for _, r := range spans {
		n -= r[1] - r[0]
	}
	if q.err = q.b.ReserveText(int64(n), int64(n)); q.err != nil {
		return ""
	}
	var out strings.Builder
	out.Grow(n)
	from := 0
	for _, r := range spans {
		out.WriteString(text[from:r[0]])
		from = r[1]
	}
	out.WriteString(text[from:])
	return out.String()
}
func (q *boundedQA) plain(i int) *boundedInput {
	p := &q.prepared[i]
	if p.plainReady || q.err != nil {
		return p
	}
	in := q.inputs[i]
	p.sourceRegions = q.regions(in.SourceText, in.Protected)
	p.targetRegions = q.regions(in.TargetText, in.Protected)
	p.regionsReady = true
	p.sourcePlain = q.strip(in.SourceText, p.sourceRegions)
	p.targetPlain = q.strip(in.TargetText, p.targetRegions)
	p.plainReady = true
	return p
}
func (q *boundedQA) clean(i int) *boundedInput {
	p := q.plain(i)
	if p.cleanReady || q.err != nil {
		return p
	}
	p.sourceClean = q.lower(q.noPlaceholders(p.sourcePlain))
	p.targetClean = q.lower(q.noPlaceholders(p.targetPlain))
	p.cleanReady = true
	return p
}
func (q *boundedQA) base(i int) *boundedInput {
	p := &q.prepared[i]
	if p.baseReady || q.err != nil {
		return p
	}
	p.sourceBase, q.err = ruby.StripRubyTagsBounded(q.b, q.inputs[i].SourceText)
	if q.err == nil {
		p.targetBase, q.err = ruby.StripRubyTagsBounded(q.b, q.inputs[i].TargetText)
	}
	p.baseReady = true
	return p
}
func (q *boundedQA) emit(i int, code string, severity IssueSeverity, message, matched string, regions [][2]int) {
	if !q.nodes(10) {
		return
	}
	var span *Span
	if matched != "" {
		span = q.locate(q.inputs[i].TargetText, matched, regions)
		if span == nil {
			span = &Span{MatchedText: matched}
		}
	}
	q.issues, q.err = backend.AppendNode(q.b, q.issues, QualityIssue{SegmentIndex: q.inputs[i].Index, Code: code, Severity: severity, Message: message, Span: span})
}

func (q *boundedQA) locate(target, matched string, regions [][2]int) *Span {
	matched = strings.TrimSpace(matched)
	if matched == "" {
		return nil
	}
	// Exact search, fallback search, and UTF-8 offset counting each scan at
	// most the target once. Charge per issue before these repeated searches.
	if int64(len(target)) > math.MaxInt64/3 || !q.visit(3*int64(len(target))) {
		q.err = backend.ErrBuildCapacity
		return nil
	}
	span := &Span{MatchedText: matched}
	from, runeFrom, region := 0, 0, 0
	for from <= len(target) {
		rel := strings.Index(target[from:], matched)
		if rel < 0 {
			break
		}
		start := from + rel
		end := start + len(matched)
		runeStart := runeFrom + utf8.RuneCountInString(target[from:start])
		runeEnd := runeStart + utf8.RuneCountInString(target[start:end])
		for region < len(regions) && regions[region][1] <= runeStart {
			region++
		}
		covered := region < len(regions) && runeEnd > regions[region][0]
		if !covered {
			startOffset, endOffset := runeStart, runeEnd
			span.TargetStart = &startOffset
			span.TargetEnd = &endOffset
			return span
		}
		from, runeFrom = end, runeEnd
	}
	start := strings.Index(target, matched)
	end := start + len(matched)
	if start < 0 {
		n := utf8.RuneCountInString(matched)
		if int64(n) > math.MaxInt64/2 {
			q.err = backend.ErrBuildCapacity
			return nil
		}
		found := false
		for pos := range target {
			if !q.visit(int64(n) * 2) {
				return nil
			}
			last := pos
			for range n {
				if last >= len(target) {
					last = -1
					break
				}
				_, size := utf8.DecodeRuneInString(target[last:])
				last += size
			}
			if last >= 0 && strings.EqualFold(target[pos:last], matched) {
				start, end, found = pos, last, true
				break
			}
		}
		if !found {
			return span
		}
		span.MatchedText = target[start:end]
	}
	first := utf8.RuneCountInString(target[:start])
	last := first + utf8.RuneCountInString(target[start:end])
	span.TargetStart = &first
	span.TargetEnd = &last
	return span
}
