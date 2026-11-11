package qa

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
)

func (q *boundedQA) numberWidth(s string) string {
	if q.err != nil {
		return ""
	}
	n := 0
	for _, r := range s {
		if r >= '０' && r <= '９' || r == '．' || r == '，' {
			n++
		} else {
			size := utf8.RuneLen(r)
			if n > math.MaxInt-size {
				q.err = backend.ErrBuildCapacity
				return ""
			}
			n += size
		}
	}
	if q.err = q.b.ReserveText(int64(n), int64(n)); q.err != nil {
		return ""
	}
	var out strings.Builder
	out.Grow(n)
	for _, r := range s {
		switch {
		case r >= '０' && r <= '９':
			out.WriteByte(byte(r - '０' + '0'))
		case r == '．':
			out.WriteByte('.')
		case r == '，':
			out.WriteByte(',')
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
func (q *boundedQA) normalizeNumber(raw string) string {
	digits, lastDigits, previous, afterLast := 0, 0, 0, 0
	separators, thousands := 0, true
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits++
			continue
		}
		if separators > 0 && digits-previous != 3 {
			thousands = false
		}
		previous = digits
		lastDigits = digits
		separators++
	}
	if separators == 0 {
		return raw
	}
	afterLast = digits - lastDigits
	if afterLast != 3 {
		thousands = false
	}
	decimal := !thousands && afterLast != 3
	n := digits
	if decimal {
		n++
	}
	if q.err != nil {
		return ""
	}
	if q.err = q.b.ReserveText(int64(n), int64(n)); q.err != nil {
		return ""
	}
	var out strings.Builder
	out.Grow(n)
	at := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			continue
		}
		if decimal && at == lastDigits {
			out.WriteByte('.')
		}
		out.WriteByte(byte(r))
		at++
	}
	return out.String()
}
func (q *boundedQA) addCount(counts map[string]int, key string) {
	if q.err != nil {
		return
	}
	if _, ok := counts[key]; !ok && !q.nodes(1) {
		return
	}
	if counts[key] == math.MaxInt {
		q.err = backend.ErrBuildCapacity
		return
	}
	counts[key]++
}
func (q *boundedQA) numberFacts(s string) (map[string]int, []string) {
	if !q.nodes(2) {
		return nil, nil
	}
	normalized := q.numberWidth(s)
	counts := make(map[string]int)
	var display []string
	q.matches(numberTokenRe, normalized, func(raw string, _, _ int) {
		key := q.normalizeNumber(raw)
		q.addCount(counts, key)
		if q.err == nil {
			display, q.err = backend.AppendNode(q.b, display, raw)
		}
	})
	return counts, display
}
func (q *boundedQA) list(items []string) string {
	if q.err != nil {
		return ""
	}
	n := int64(2)
	for _, item := range items {
		if int64(len(item)) > math.MaxInt64-n-1 {
			q.err = backend.ErrBuildCapacity
			return ""
		}
		n += int64(len(item)) + 1
	}
	if len(items) > 0 {
		n--
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
	out.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			out.WriteByte(' ')
		}
		out.WriteString(item)
	}
	out.WriteByte(']')
	return out.String()
}
func (q *boundedQA) numbers(i int) {
	in := q.inputs[i]
	if strings.TrimSpace(in.SourceText) == "" || strings.TrimSpace(in.TargetText) == "" {
		return
	}
	source, displaySource := q.numberFacts(in.SourceText)
	target, displayTarget := q.numberFacts(in.TargetText)
	if q.err == nil && !multisetEqual(source, target) {
		q.emit(i, CheckNumberMismatch, SeverityWarning, q.message("数字不一致：原文 ", q.list(displaySource), "，译文 ", q.list(displayTarget)), "", nil)
	}
}
func (q *boundedQA) urlFacts(s string) map[string]int {
	if !q.nodes(1) {
		return nil
	}
	counts := make(map[string]int)
	visit := func(raw string, _, _ int) { q.addCount(counts, q.lower(strings.TrimRight(raw, ".,;:!?)]}\"'>"))) }
	q.matches(urlTokenRe, s, visit)
	q.matches(emailTokenRe, s, visit)
	return counts
}
func (q *boundedQA) sortedKeys(counts map[string]int) []string {
	if !q.nodes(int64(len(counts))) || !q.nodes(1) {
		return nil
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out []string
	for _, key := range keys {
		for range counts[key] {
			if q.err != nil {
				return nil
			}
			out, q.err = backend.AppendNode(q.b, out, key)
		}
	}
	return out
}
func (q *boundedQA) urlEmail(i int) {
	in := q.inputs[i]
	if strings.TrimSpace(in.SourceText) == "" || strings.TrimSpace(in.TargetText) == "" {
		return
	}
	source, target := q.urlFacts(in.SourceText), q.urlFacts(in.TargetText)
	if q.err == nil && !multisetEqual(source, target) {
		q.emit(i, CheckURLEmailMismatch, SeverityWarning, q.message("URL/邮箱不一致：原文 ", q.list(q.sortedKeys(source)), "，译文 ", q.list(q.sortedKeys(target))), "", nil)
	}
}
func (q *boundedQA) placeholders(i int) {
	s := q.inputs[i].TargetText
	if s == "" || !strings.Contains(s, "__LF_") {
		return
	}
	if !q.nodes(1) {
		return
	}
	seen := make(map[string]struct{})
	q.matches(leftoverPlaceholderRe, s, func(hit string, _, _ int) {
		if _, ok := seen[hit]; ok {
			return
		}
		if !q.nodes(1) {
			return
		}
		seen[hit] = struct{}{}
		q.emit(i, CheckLeftoverPlaceholder, SeverityError, q.message("译文残留占位符：", hit), hit, nil)
	})
}
func (q *boundedQA) xmlName(token string) string {
	if q.err != nil {
		return ""
	}
	loc, err := backend.FindRegexpSubmatch(q.b, xmlTagNameRe, token)
	if err != nil {
		q.err = err
		return ""
	}
	if len(loc) < 4 {
		return ""
	}
	return q.lower(token[loc[2]:loc[3]])
}
func (q *boundedQA) xmlFacts(s string) map[string]int {
	if !q.nodes(1) {
		return nil
	}
	counts := make(map[string]int)
	q.matches(xmlTagTokenRe, s, func(token string, _, _ int) {
		name := q.xmlName(token)
		if _, skip := rubyTagFamily[name]; !skip {
			q.addCount(counts, token)
		}
	})
	return counts
}
func (q *boundedQA) culprit(s string) string {
	type frame struct{ name, token string }
	var stack []frame
	last := ""
	for from := 0; from <= len(s) && q.err == nil; {
		if q.err = q.ctx.Err(); q.err != nil {
			return ""
		}
		loc, err := backend.FindRegexpIndex(q.b, xmlTagTokenRe, s[from:])
		if err != nil {
			q.err = err
			return ""
		}
		if loc == nil {
			break
		}
		token := s[from+loc[0] : from+loc[1]]
		from += loc[1]
		last = token
		name := q.xmlName(token)
		if name == "" {
			continue
		}
		if strings.HasPrefix(token, "</") {
			matched := false
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].name == name {
					stack = stack[:i]
					matched = true
					break
				}
			}
			if !matched {
				return token
			}
			continue
		}
		if !strings.HasSuffix(token, "/>") {
			if len(stack) >= backend.MaxBuildDepth {
				q.err = backend.ErrBuildCapacity
				return ""
			}
			stack, q.err = backend.AppendNode(q.b, stack, frame{name, token})
		}
	}
	if len(stack) > 0 {
		return stack[len(stack)-1].token
	}
	if last != "" {
		return last
	}
	return s
}
func (q *boundedQA) xml(i int) {
	in := q.inputs[i]
	if strings.TrimSpace(in.SourceText) == "" {
		return
	}
	if err := markup.TargetRegressionBounded(q.b, in.SourceText, in.TargetText); err != nil {
		if isCapacity(err) {
			q.err = err
			return
		}
		hit := q.culprit(in.TargetText)
		message := q.message("译文 XML 结构损坏（标签未闭合或嵌套错误）：", err.Error())
		if !q.nodes(1) {
			return
		}
		q.issues, q.err = backend.AppendNode(q.b, q.issues, QualityIssue{SegmentIndex: in.Index, Severity: SeverityError, Code: CheckXMLTagMismatch, Message: message, Span: &Span{MatchedText: hit}})
		return
	}
	source := q.xmlFacts(in.SourceText)
	if len(source) == 0 || q.err != nil {
		return
	}
	target := q.xmlFacts(in.TargetText)
	if q.err == nil && !multisetEqual(source, target) {
		q.emit(i, CheckXMLTagMismatch, SeverityError, q.message("XML 标签不一致：原文 ", q.list(q.sortedKeys(source)), "，译文 ", q.list(q.sortedKeys(target))), "", nil)
	}
}
func (q *boundedQA) terms(i int, g glossary.Glossary, sourceLang, targetLang string, forbidden bool) {
	in := q.inputs[i]
	if g == nil || strings.TrimSpace(in.SourceText) == "" || strings.TrimSpace(in.TargetText) == "" {
		return
	}
	hits, err := glossary.LookupEntriesBounded(q.ctx, q.b, g, in.SourceText, sourceLang, targetLang)
	if err != nil {
		q.err = err
		return
	}
	if !q.nodes(1) {
		return
	}
	seen := make(map[string]struct{})
	for _, entry := range hits {
		if q.err != nil {
			return
		}
		if entry.Forbidden != forbidden || entry.Source == "" || entry.Target == "" {
			continue
		}
		if !q.termPresent(in.SourceText, entry.Source, entry.CaseSensitive, sourceLang) {
			continue
		}
		present := q.termPresent(in.TargetText, entry.Target, entry.CaseSensitive, targetLang)
		if forbidden && !present || !forbidden && present {
			continue
		}
		key := q.message(entry.Source, "\x00", entry.Target)
		if _, ok := seen[key]; ok {
			continue
		}
		if !q.nodes(1) {
			return
		}
		seen[key] = struct{}{}
		if forbidden {
			q.emit(i, CheckForbiddenTerm, SeverityError, q.message("译文含禁译词「", entry.Target, "」（源术语「", entry.Source, "」）"), entry.Target, nil)
		} else {
			severity := SeverityWarning
			if entry.Mandatory {
				severity = SeverityError
			}
			if !q.nodes(1) {
				return
			}
			span := &Span{MatchedText: entry.Source}
			message := q.message("术语「", entry.Source, "」未使用推荐译法「", entry.Target, "」")
			if q.err == nil {
				q.issues, q.err = backend.AppendNode(q.b, q.issues, QualityIssue{SegmentIndex: in.Index, Severity: severity, Code: CheckTermInconsistency, Message: message, Span: span})
			}
		}
	}
}
func (q *boundedQA) termPresent(text, needle string, sensitive bool, lang string) bool {
	cjk := cjkLanguageBounded(lang)
	if sensitive {
		if !q.visit(int64(len(text))) {
			return false
		}
		if cjk {
			return strings.Contains(text, needle)
		}
		return latinIndependentContains(text, needle)
	}
	// Match borrowed UTF-8 windows. Neither a rune array nor a window/prefix
	// string is constructed; byte offsets remain offsets in the original text.
	runes := utf8.RuneCountInString(needle)
	if runes == 0 {
		return false
	}
	if int64(runes) > math.MaxInt64/2 {
		q.err = backend.ErrBuildCapacity
		return false
	}
	for start := range text {
		if !q.visit(int64(runes) * 2) {
			return false
		}
		end := start
		for range runes {
			if end >= len(text) {
				return false
			}
			_, size := utf8.DecodeRuneInString(text[end:])
			end += size
		}
		same := strings.EqualFold(text[start:end], needle)
		if cjk {
			same = lowerEqual(text[start:end], needle)
		}
		if same && (cjk || isIndependentMatchLocal(text, start, end)) {
			return true
		}
		if q.err != nil {
			return false
		}
	}
	return false
}

func cjkLanguageBounded(lang string) bool {
	lang = strings.TrimSpace(lang)
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	for _, known := range [...]string{"zh", "ja", "ko", "th", "lo", "my", "km"} {
		if strings.EqualFold(lang, known) {
			return true
		}
	}
	return false
}
func lowerEqual(a, z string) bool {
	for a != "" && z != "" {
		ar, an := utf8.DecodeRuneInString(a)
		zr, zn := utf8.DecodeRuneInString(z)
		if unicode.ToLower(ar) != unicode.ToLower(zr) {
			return false
		}
		a, z = a[an:], z[zn:]
	}
	return a == "" && z == ""
}

func (q *boundedQA) residual(i int, c *SourceResidualChecker) {
	in := q.inputs[i]
	if len(c.rules) == 0 || strings.TrimSpace(in.SourceText) == "" || strings.TrimSpace(in.TargetText) == "" {
		return
	}
	base := q.base(i)
	if strings.TrimSpace(base.sourceBase) == strings.TrimSpace(base.targetBase) {
		return
	}
	p := q.clean(i)
	if !q.nodes(1) {
		return
	}
	seen := make(map[string]struct{})
	for _, rule := range c.rules {
		hitAny := false
		add := func(run string) {
			hitAny = true
			if _, ok := seen[run]; ok {
				return
			}
			if !q.nodes(1) {
				return
			}
			seen[run] = struct{}{}
			q.emit(i, CheckSourceResidual, SeverityWarning, q.message("译文残留源语片段：", run), run, p.targetRegions)
		}
		if rule.anchor == anchorRequired {
			q.scriptRuns(p.sourceClean, rule, func(run string) {
				if q.visit(int64(len(p.targetClean))) && strings.Contains(p.targetClean, run) {
					add(run)
				}
			})
			q.scriptRuns(p.targetClean, rule, func(run string) {
				if q.visit(int64(len(p.sourceClean))) && strings.Contains(p.sourceClean, run) {
					add(run)
				}
			})
		} else {
			q.scriptRuns(p.targetClean, rule, add)
		}
		if hitAny || q.err != nil {
			return
		}
	}
}
func (q *boundedQA) scriptRuns(text string, rule residualRule, visit func(string)) {
	start, runes := -1, 0
	for pos, r := range text {
		if !q.visit(1) {
			return
		}
		if ruleBelongs(r, rule.script) {
			if start < 0 {
				start = pos
			}
			runes++
		} else if start >= 0 {
			if runes >= rule.minRun {
				visit(text[start:pos])
			}
			start, runes = -1, 0
		}
	}
	if start >= 0 && runes >= rule.minRun && q.err == nil {
		visit(text[start:])
	}
}
