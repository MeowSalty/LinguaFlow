package qa

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func (q *boundedQA) check(checker Checker) {
	if _, ok := checker.(*DuplicateTranslationChecker); ok {
		q.duplicates()
		return
	}
	for i, in := range q.inputs {
		if q.err != nil {
			return
		}
		src, tgt := in.SourceText, in.TargetText
		switch c := checker.(type) {
		case *UntranslatedChecker:
			p := q.base(i)
			source, target := strings.TrimSpace(p.sourceBase), strings.TrimSpace(p.targetBase)
			if source == "" || target == "" || source != target {
				continue
			}
			exempt := q.lower(q.noPlaceholders(q.strip(source, q.regions(source, in.Protected))))
			if isExempt(exempt) {
				continue
			}
			severity, msg := untranslatedVerdict(c, target)
			q.emit(i, c.Name(), severity, msg, strings.TrimSpace(tgt), nil)
		case *LengthRatioChecker:
			p := q.base(i)
			if p.sourceBase == "" || p.targetBase == "" {
				continue
			}
			sourceLen := c.measure(p.sourceBase)
			if sourceLen < 5 {
				continue
			}
			ratio := float64(c.measure(p.targetBase)) / float64(sourceLen)
			if ratio < c.minRatio {
				q.emit(i, c.Name(), SeverityWarning, q.message("译文过短 (", q.floating(ratio*100, 1), "%)，长度比 ", q.floating(ratio, 2)), "", nil)
			} else if ratio > c.maxRatio {
				q.emit(i, c.Name(), SeverityWarning, q.message("译文过长 (", q.floating(ratio*100, 0), "%)，长度比 ", q.floating(ratio, 2)), "", nil)
			}
		case *SourceResidualChecker:
			q.residual(i, c)
		case *PunctuationPairingChecker:
			if strings.TrimSpace(tgt) == "" {
				continue
			}
			p := q.plain(i)
			hit, detail := q.pairing(p.targetPlain, c.pairs)
			if hit != "" {
				q.emit(i, c.Name(), SeverityWarning, q.message("标点配对异常：", detail), hit, p.targetRegions)
			}
		case *PunctuationMissingChecker:
			if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
				continue
			}
			p := q.clean(i)
			for _, cat := range punctMissingCategories {
				var hit string
				if cat.name == "引号" {
					hit = q.quotePair(p.sourceClean)
				} else if countCategory(p.sourceClean, cat.set) >= 2 {
					hit = q.message(firstCategoryBorrowed(p.sourceClean, cat.set, 0), firstCategoryBorrowed(p.sourceClean, cat.set, 1))
				}
				if hit != "" && countCategory(p.targetClean, cat.set) == 0 {
					q.emit(i, c.Name(), SeverityWarning, q.message("译文缺失源文的", cat.name, "标点：", hit), hit, p.targetRegions)
				}
			}
		case *PunctuationSurplusChecker:
			if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
				continue
			}
			p := q.clean(i)
			for _, cat := range punctMissingCategories {
				if countCategory(p.sourceClean, cat.set) == 0 && countCategory(p.targetClean, cat.set) >= 2 {
					hit := q.message(firstCategoryBorrowed(p.targetClean, cat.set, 0), firstCategoryBorrowed(p.targetClean, cat.set, 1))
					q.emit(i, c.Name(), SeverityWarning, q.message("译文多出源文没有的", cat.name, "标点：", hit), hit, p.targetRegions)
				}
			}
		case *PunctuationWrapLossChecker:
			if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
				continue
			}
			p := q.clean(i)
			source, target := strings.TrimSpace(p.sourceClean), strings.TrimSpace(p.targetClean)
			first, ok := firstRuneOf(source)
			close, paired := directionalQuotePairs[first]
			last, _ := lastRuneOf(source)
			if ok && paired && last == close && runeLenOf(source) > 2 && runeCount(source, first)+runeCount(source, close) == 2 && target != "" && targetEdgesBare(target) {
				hit := q.message(firstRuneBorrowed(source), lastRuneBorrowed(source))
				q.emit(i, c.Name(), SeverityWarning, q.message("源文整体被引号包裹，译文丢失外层包裹：", hit), hit, p.targetRegions)
			}
		case *WhitespaceIrregularChecker:
			if tgt == "" {
				continue
			}
			p := q.plain(i)
			hit := irregularBorrowed(p.targetPlain)
			if hit != "" {
				if !q.nodes(2) {
					return
				}
				var described string
				if q.err = q.b.ReserveFormatWorkspace(); q.err == nil {
					q.err = q.b.ReserveText(64, 64)
				}
				if q.err == nil {
					described = describeWhitespace(hit)
				}
				q.emit(i, c.Name(), SeverityWarning, q.message("存在异常空白字符：", described), hit, p.targetRegions)
			}
		case *RepeatedSpaceChecker:
			q.repeated(i, c.cjkTarget)
		case *WidthMixChecker:
			if strings.TrimSpace(tgt) == "" {
				continue
			}
			p := q.plain(i)
			hit := widthBorrowed(p.targetPlain, c.cjkTarget)
			if hit != "" {
				label := "拉丁译文中混入全角字符："
				if c.cjkTarget {
					label = "CJK 译文中混入半角标点："
				}
				q.emit(i, c.Name(), SeverityWarning, q.message(label, q.quote(hit)), hit, p.targetRegions)
			}
		case *ScriptMismatchChecker:
			if !c.active || strings.TrimSpace(tgt) == "" {
				continue
			}
			p := q.plain(i)
			ev, ok, err := c.profile.ScanBounded(q.b, p.targetPlain)
			if err != nil {
				q.err = err
				return
			}
			if !ok {
				continue
			}
			sample := ev.Sample
			if !strings.Contains(tgt, sample) {
				sample = firstRuneBorrowed(sample)
			}
			q.emit(i, c.Name(), SeverityWarning, q.message("译文使用了", ev.Script.DisplayName(), "（如「", sample, "」），目标语言 ", c.targetLang, " 期望", c.profile.Expected.DisplayName()), sample, p.targetRegions)
		case *NumberMismatchChecker:
			q.numbers(i)
		case *URLEmailMismatchChecker:
			q.urlEmail(i)
		case *SubtitleLineCountChecker:
			if !c.enabled || strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
				continue
			}
			source, target := boundedLineCount(src), boundedLineCount(tgt)
			if source != target {
				q.emit(i, c.Name(), SeverityWarning, q.message("字幕行数不一致：原文 ", q.decimal(source), " 行，译文 ", q.decimal(target), " 行"), "", nil)
			}
		case *ForbiddenTermChecker:
			q.terms(i, c.gls, c.srcLang, c.tgtLang, true)
		case *TermInconsistencyChecker:
			q.terms(i, c.gls, c.srcLang, c.tgtLang, false)
		case *LeftoverPlaceholderChecker:
			q.placeholders(i)
		case *XMLTagMismatchChecker:
			q.xml(i)
		default:
			q.err = backend.ErrBuildCapacity
		}
	}
}

func (q *boundedQA) duplicates() {
	if !q.nodes(int64(len(q.inputs))) || !q.nodes(1) {
		return
	}
	type entry struct {
		index  int
		source string
	}
	seen := make(map[string]entry, len(q.inputs))
	for i, in := range q.inputs {
		if in.TargetText == "" {
			continue
		}
		if previous, ok := seen[in.TargetText]; ok {
			if previous.source != in.SourceText {
				q.emit(i, CheckDuplicate, SeverityWarning, q.message("译文与段落 ", q.decimal(previous.index), " 重复（原文不同）"), "", nil)
			}
		} else {
			seen[in.TargetText] = entry{in.Index, in.SourceText}
		}
	}
}
func firstRuneBorrowed(s string) string {
	if s == "" {
		return ""
	}
	_, n := utf8.DecodeRuneInString(s)
	return s[:n]
}
func lastRuneBorrowed(s string) string {
	if s == "" {
		return ""
	}
	_, n := utf8.DecodeLastRuneInString(s)
	return s[len(s)-n:]
}
func runeCount(s string, want rune) int {
	n := 0
	for _, r := range s {
		if r == want {
			n++
		}
	}
	return n
}
func firstCategoryBorrowed(s string, set map[rune]struct{}, occurrence int) string {
	for pos, r := range s {
		if _, ok := set[r]; ok {
			if occurrence == 0 {
				_, n := utf8.DecodeRuneInString(s[pos:])
				return s[pos : pos+n]
			}
			occurrence--
		}
	}
	return ""
}
func irregularBorrowed(s string) string {
	for pos, r := range s {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff', '\u00a0', '\t', '\u2028', '\u2029', '\u3000':
			return firstRuneBorrowed(s[pos:])
		}
		if unicode.Is(unicode.Cf, r) && unicode.IsSpace(r) {
			return firstRuneBorrowed(s[pos:])
		}
	}
	inner := strings.TrimSpace(s)
	for pos, r := range inner {
		if r != '\n' && r != '\r' && unicode.IsSpace(r) && r != ' ' {
			return firstRuneBorrowed(inner[pos:])
		}
	}
	return ""
}
func widthBorrowed(s string, cjk bool) string {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		next := i + n
		if !cjk {
			if isFullwidthChar(r) {
				return s[i:next]
			}
			i = next
			continue
		}
		if !isHalfwidthPunct(r) {
			i = next
			continue
		}
		previous, _ := utf8.DecodeLastRuneInString(s[:i])
		after, _ := utf8.DecodeRuneInString(s[next:])
		switch r {
		case ',', ':':
			if i > 0 && next < len(s) && isASCIIDigit(previous) && isASCIIDigit(after) {
				i = next
				continue
			}
		case '!', '?':
			if i > 0 && isASCIIDigit(previous) {
				for next < len(s) && (s[next] == '!' || s[next] == '?') {
					next++
				}
				i = next
				continue
			}
		}
		return s[i : i+n]
	}
	return ""
}
func boundedLineCount(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' {
			n++
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		} else if s[i] == '\n' {
			n++
		}
	}
	return n
}

type boundedPairFrame struct {
	open, close rune
	symmetric   bool
}

func (q *boundedQA) pairing(s string, pairs []pairDef) (string, string) {
	var stack []boundedPairFrame
	for pos, r := range s {
		var pair pairDef
		opening, closing := false, false
		for _, candidate := range pairs {
			if candidate.open == r {
				pair = candidate
				opening = true
				break
			}
			if candidate.close == r {
				pair = candidate
				closing = true
			}
		}
		hit := firstRuneBorrowed(s[pos:])
		if opening {
			if pair.open == pair.close && len(stack) > 0 && stack[len(stack)-1].symmetric && stack[len(stack)-1].open == r {
				stack = stack[:len(stack)-1]
				continue
			}
			if len(stack) >= backend.MaxBuildDepth {
				q.err = backend.ErrBuildCapacity
				return "", ""
			}
			stack, q.err = backend.AppendNode(q.b, stack, boundedPairFrame{pair.open, pair.close, pair.open == pair.close})
			if q.err != nil {
				return "", ""
			}
			continue
		}
		if closing {
			if len(stack) == 0 {
				return hit, q.message("多余的闭合标点 ", q.quote(hit))
			}
			top := stack[len(stack)-1]
			if top.open != pair.open {
				want := q.runeText(top.close)
				return hit, q.message("标点错嵌套：期望闭合 ", q.quote(want), "，实际 ", q.quote(hit))
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		hit := q.runeText(stack[len(stack)-1].open)
		return hit, q.message("未闭合的标点 ", q.quote(hit))
	}
	return "", ""
}
func (q *boundedQA) runeText(r rune) string {
	if q.err != nil {
		return ""
	}
	if q.err = q.b.ReserveText(4, 4); q.err != nil {
		return ""
	}
	return string(r)
}
func (q *boundedQA) quotePair(s string) string {
	var stack []boundedPairFrame
	for pos, r := range s {
		previous, _ := utf8.DecodeLastRuneInString(s[:pos])
		_, n := utf8.DecodeRuneInString(s[pos:])
		after, _ := utf8.DecodeRuneInString(s[pos+n:])
		if r == '’' && pos > 0 && pos+n < len(s) && unicode.IsLetter(previous) && unicode.IsLetter(after) {
			continue
		}
		if r == '"' {
			if len(stack) > 0 && stack[len(stack)-1].symmetric {
				return `""`
			}
			if len(stack) >= backend.MaxBuildDepth {
				q.err = backend.ErrBuildCapacity
				return ""
			}
			stack, q.err = backend.AppendNode(q.b, stack, boundedPairFrame{r, r, true})
			continue
		}
		if close, ok := directionalQuotePairs[r]; ok {
			if len(stack) >= backend.MaxBuildDepth {
				q.err = backend.ErrBuildCapacity
				return ""
			}
			stack, q.err = backend.AppendNode(q.b, stack, boundedPairFrame{r, close, false})
			continue
		}
		if len(stack) > 0 && stack[len(stack)-1].close == r {
			return q.message(q.runeText(stack[len(stack)-1].open), q.runeText(r))
		}
		if q.err != nil {
			return ""
		}
	}
	return ""
}
func (q *boundedQA) repeated(i int, cjk bool) {
	in := q.inputs[i]
	if in.TargetText == "" {
		return
	}
	p := q.plain(i)
	s := in.TargetText
	runeIndex := 0
	for pos := 0; pos < len(s) && q.err == nil; {
		r, n := utf8.DecodeRuneInString(s[pos:])
		if r != ' ' {
			pos += n
			runeIndex++
			continue
		}
		start, first := pos, runeIndex
		for pos < len(s) && s[pos] == ' ' {
			pos++
			runeIndex++
		}
		if runeIndex-first >= 2 && !regionCovers(p.targetRegions, first, runeIndex) {
			q.repeatedIssue(i, s[start:pos], first, runeIndex)
		}
	}
	if !cjk {
		return
	}
	runeIndex = 0
	for pos, r := range s {
		_, n := utf8.DecodeRuneInString(s[pos:])
		if isCJK(r) && pos+n < len(s) && s[pos+n] == ' ' {
			after, size := utf8.DecodeRuneInString(s[pos+n+1:])
			if isCJK(after) && !regionCovers(p.targetRegions, runeIndex, runeIndex+3) {
				q.repeatedIssue(i, s[pos:pos+n+1+size], runeIndex, runeIndex+3)
			}
		}
		runeIndex++
	}
}
func (q *boundedQA) repeatedIssue(i int, text string, start, end int) {
	if !q.nodes(4) {
		return
	}
	startOffset, endOffset := start, end
	span := &Span{MatchedText: text, TargetStart: &startOffset, TargetEnd: &endOffset}
	q.issues, q.err = backend.AppendNode(q.b, q.issues, QualityIssue{SegmentIndex: q.inputs[i].Index, Severity: SeverityWarning, Code: CheckRepeatedSpace, Message: "存在连续空格或 CJK 字符间多余空格", Span: span})
}

func ReconcileIssuesBounded(b *backend.BuildBounds, fresh, existing []QualityIssue) ([]QualityIssue, error) {
	if b == nil {
		return nil, backend.ErrBuildCapacity
	}
	q := boundedQA{b: b}
	if len(existing) == 0 {
		if fresh == nil {
			return nil, nil
		}
		if !q.nodes(int64(len(fresh))) || !q.nodes(1) {
			return nil, q.err
		}
		out := make([]QualityIssue, len(fresh))
		copy(out, fresh)
		for i := range out {
			if out[i].Disposition == "" {
				out[i].Disposition = DispositionPending
			}
		}
		return out, nil
	}
	if !q.nodes(int64(len(existing))) || !q.nodes(1) {
		return nil, q.err
	}
	previous := make(map[string]QualityIssue, len(existing))
	for _, issue := range existing {
		key := q.message(issue.Code, ":", MatchedText(issue))
		if q.err != nil {
			return nil, q.err
		}
		previous[key] = issue
	}
	if !q.nodes(int64(len(fresh))) || !q.nodes(1) {
		return nil, q.err
	}
	out := make([]QualityIssue, 0, len(fresh))
	for _, issue := range fresh {
		key := q.message(issue.Code, ":", MatchedText(issue))
		if q.err != nil {
			return nil, q.err
		}
		if old, ok := previous[key]; ok && !old.IsPending() {
			issue.Disposition = old.Disposition
			issue.DecidedBy = old.DecidedBy
			issue.DecidedAt = old.DecidedAt
			issue.Note = old.Note
		} else if issue.Disposition == "" {
			issue.Disposition = DispositionPending
		}
		out = append(out, issue)
	}
	return out, nil
}

func ReviseFinalIssuesBounded(b *backend.BuildBounds, existing, fresh []QualityIssue, targetedCodes []string, qaRan bool) ([]QualityIssue, error) {
	if b == nil {
		return nil, backend.ErrBuildCapacity
	}
	q := boundedQA{b: b}
	if !q.nodes(int64(len(targetedCodes))) || !q.nodes(1) || !q.nodes(int64(len(existing))) || !q.nodes(1) {
		return nil, q.err
	}
	drop := make(map[string]struct{}, len(targetedCodes))
	for _, code := range targetedCodes {
		drop[code] = struct{}{}
	}
	kept := make([]QualityIssue, 0, len(existing))
	for _, issue := range existing {
		if _, ok := drop[issue.Code]; ok && !issue.Dismissed() {
			continue
		}
		kept = append(kept, issue)
	}
	if !qaRan {
		return kept, nil
	}
	out, err := ReconcileIssuesBounded(b, fresh, kept)
	if err != nil {
		return nil, err
	}
	for _, issue := range kept {
		if IsSemanticQACode(issue.Code) {
			out, err = backend.AppendNode(b, out, issue)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
