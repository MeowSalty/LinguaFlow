package ruby

import (
	"regexp"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func init() {
	for _, re := range []*regexp.Regexp{rubyElementSpanRe, rubyRTSpanRe, htmlTagRe, alignmentTagRE, inlineMarkerRe, sectionRubyLineRe} {
		backend.RegisterFixedRegexp(re)
	}
}

func scanElementsBounded(b *backend.BuildBounds, source string) ([]rubyElement, error) {
	var out []rubyElement
	for from := 0; from < len(source); {
		if e := b.ReserveNodes(1, 256); e != nil {
			return nil, e
		}
		el, findErr := backend.FindRegexpSubmatch(b, rubyElementSpanRe, source[from:])
		if findErr != nil {
			return nil, findErr
		}
		if el == nil {
			break
		}
		start, end := from+el[0], from+el[1]
		innerStart, innerEnd := from+el[2], from+el[3]
		var spans []rtSpan
		for pos := innerStart; pos < innerEnd; {
			if e := b.ReserveNodes(1, 256); e != nil {
				return nil, e
			}
			loc, findErr := backend.FindRegexpSubmatch(b, rubyRTSpanRe, source[pos:innerEnd])
			if findErr != nil {
				return nil, findErr
			}
			if loc == nil {
				break
			}
			var e error
			spans, e = backend.AppendNode(b, spans, rtSpan{pos + loc[0], pos + loc[1], pos + loc[2], pos + loc[3]})
			if e != nil {
				return nil, e
			}
			pos += loc[1]
		}
		if len(spans) > 0 {
			var e error
			out, e = backend.AppendNode(b, out, rubyElement{start, end, spans})
			if e != nil {
				return nil, e
			}
		}
		from = end
	}
	return out, nil
}

// ElementSpansBounded returns complete ruby element byte spans in ascending
// order. The shared bounded scanner omits elements without a complete rt pair.
func ElementSpansBounded(b *backend.BuildBounds, source string) ([][2]int, error) {
	elements, err := scanElementsBounded(b, source)
	if err != nil {
		return nil, err
	}
	var spans [][2]int
	for _, element := range elements {
		spans, err = backend.AppendNode(b, spans, [2]int{element.start, element.end})
		if err != nil {
			return nil, err
		}
	}
	return spans, nil
}
func stripPatternBounded(b *backend.BuildBounds, s string, re *regexp.Regexp) (string, error) {
	if e := b.ReserveNodes(1, 256); e != nil {
		return "", e
	}
	loc, findErr := backend.FindRegexpIndex(b, re, s)
	if findErr != nil {
		return "", findErr
	}
	if loc == nil {
		return s, nil
	}
	w := backend.NewBoundedWriter(b, int64(len(s)))
	from := 0
	for loc != nil {
		if _, e := w.WriteString(s[from : from+loc[0]]); e != nil {
			return "", e
		}
		from += loc[1]
		if from == len(s) {
			break
		}
		if e := b.ReserveNodes(1, 256); e != nil {
			return "", e
		}
		var e error
		loc, e = backend.FindRegexpIndex(b, re, s[from:])
		if e != nil {
			return "", e
		}
	}
	if _, e := w.WriteString(s[from:]); e != nil {
		return "", e
	}
	if e := b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
		return "", e
	}
	return string(w.Bytes()), nil
}

func ExtractBounded(b *backend.BuildBounds, source string) ([]Item, string, error) {
	els, e := scanElementsBounded(b, source)
	if e != nil {
		return nil, "", e
	}
	if len(els) == 0 {
		return nil, source, nil
	}
	var matches []rubyMatch
	for _, el := range els {
		for i, rt := range el.rts {
			start := el.start
			if i > 0 {
				start = el.rts[i-1].end
			}
			base, e := stripPatternBounded(b, source[start:rt.start], htmlTagRe)
			if e != nil {
				return nil, "", e
			}
			end := el.end
			if i < len(el.rts)-1 {
				end = rt.end
			}
			matches, e = backend.AppendNode(b, matches, rubyMatch{annotation{base, source[rt.textStart:rt.textEnd]}, start, end})
			if e != nil {
				return nil, "", e
			}
		}
	}
	var items []Item
	for i := 0; i < len(matches); {
		end := i + 1
		if isPerKanji(matches[i].Base) {
			for end < len(matches) && isPerKanji(matches[end].Base) && matches[end].start == matches[end-1].end {
				end++
			}
		}
		base, text := matches[i].Base, matches[i].Text
		if end > i+1 {
			var nb, nt int64
			for _, v := range matches[i:end] {
				nb += int64(len(v.Base))
				nt += int64(len(v.Text))
			}
			bw, e := backend.NewFixedBoundedWriter(b, nb)
			if e != nil {
				return nil, "", e
			}
			tw, e := backend.NewFixedBoundedWriter(b, nt)
			if e != nil {
				return nil, "", e
			}
			for _, v := range matches[i:end] {
				_, _ = bw.WriteString(v.Base)
				_, _ = tw.WriteString(v.Text)
			}
			if e = b.ReserveText(nb+nt, nb+nt); e != nil {
				return nil, "", e
			}
			base, text = string(bw.Bytes()), string(tw.Bytes())
		}
		if e = b.ReserveNodes(1, 64); e != nil {
			return nil, "", e
		}
		if e = b.ReserveText(16, 16); e != nil {
			return nil, "", e
		}
		items, e = backend.AppendNode(b, items, Item{ID: strconv.Itoa(len(items) + 1), SourceBase: base, SourceText: text})
		if e != nil {
			return nil, "", e
		}
		i = end
	}
	w := backend.NewBoundedWriter(b, int64(len(source)))
	from := 0
	for _, el := range els {
		if _, e = w.WriteString(source[from:el.start]); e != nil {
			return nil, "", e
		}
		pos := el.start + len("<ruby>")
		for _, rt := range el.rts {
			plain, e := stripPatternBounded(b, source[pos:rt.start], htmlTagRe)
			if e != nil {
				return nil, "", e
			}
			if _, e = w.WriteString(plain); e != nil {
				return nil, "", e
			}
			pos = rt.end
		}
		plain, e := stripPatternBounded(b, source[pos:el.end-len("</ruby>")], htmlTagRe)
		if e != nil {
			return nil, "", e
		}
		if _, e = w.WriteString(plain); e != nil {
			return nil, "", e
		}
		from = el.end
	}
	if _, e = w.WriteString(source[from:]); e != nil {
		return nil, "", e
	}
	if e = b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
		return nil, "", e
	}
	return items, string(w.Bytes()), nil
}

func StripRubyTagsBounded(b *backend.BuildBounds, source string) (string, error) {
	_, s, e := ExtractBounded(b, source)
	return s, e
}
