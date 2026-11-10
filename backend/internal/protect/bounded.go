package protect

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func init() {
	for _, re := range []*regexp.Regexp{fencedBacktickRe, fencedTildeRe, inlineCodeRe, doubleBraceRe, singleBraceRe, printfVerbRe, shellVarRe, xmlTagRe, mdLinkRe, autoLinkRe, refLinkRe, placeholderLikeRE, mergeAdjacentPlaceholderRE, singlePlaceholderRE} {
		backend.RegisterFixedRegexp(re)
	}
}

func nextKeyBounded(b *backend.BuildBounds, seg *model.Segment) (string, error) {
	if seg.Protected == nil {
		if e := b.ReserveNodes(1, 1024); e != nil {
			return "", e
		}
		seg.Protected = make(map[string]string)
	}
	for i := len(seg.Protected) + 1; i <= 999999; i++ {
		if e := b.ReserveNodes(1, 64); e != nil {
			return "", e
		}
		var scratch [13]byte
		digits := strconv.AppendInt(scratch[:0], int64(i), 10)
		var key [13]byte
		copy(key[:], "__LF_000000__")
		copy(key[11-len(digits):11], digits)
		if e := b.ReserveText(13, 13); e != nil {
			return "", e
		}
		k := string(key[:])
		if _, ok := seg.Protected[k]; !ok && !strings.Contains(seg.Source, k) {
			if e := b.ReserveNodes(1, 1024); e != nil {
				return "", e
			}
			return k, nil
		}
	}
	return "", &backend.BuildCapacityError{Resource: "placeholder identity"}
}

func regexReplaceBounded(b *backend.BuildBounds, s string, re *regexp.Regexp, replace func(string, []int) (string, error), limits ...int64) (string, error) {
	if e := b.ReserveNodes(1, 256); e != nil {
		return "", e
	}
	first, findErr := backend.FindRegexpSubmatch(b, re, s)
	if findErr != nil {
		return "", findErr
	}
	if first == nil {
		return s, nil
	}
	limit := b.Snapshot().TextLimit
	if len(limits) > 0 {
		limit = limits[0]
	}
	w := backend.NewBoundedWriter(b, limit)
	from := 0
	loc := first
	for loc != nil {
		start, end := from+loc[0], from+loc[1]
		if _, e := w.WriteString(s[from:start]); e != nil {
			return "", e
		}
		value, e := replace(s[from:], loc)
		if e != nil {
			return "", e
		}
		if _, e = w.WriteString(value); e != nil {
			return "", e
		}
		from = end
		if from == len(s) {
			break
		}
		if e = b.ReserveNodes(1, 256); e != nil {
			return "", e
		}
		loc, e = backend.FindRegexpSubmatch(b, re, s[from:])
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
func protectPatternBounded(b *backend.BuildBounds, seg *model.Segment, re *regexp.Regexp, kind int, limit int64) error {
	s, e := regexReplaceBounded(b, seg.Source, re, func(t string, loc []int) (string, error) {
		k, e := nextKeyBounded(b, seg)
		if e != nil {
			return "", e
		}
		match := t[loc[0]:loc[1]]
		value := match
		prefix, open, suffix := "", "", ""
		if kind == 1 {
			end := loc[5]
			if len(loc) > 7 && loc[6] >= 0 {
				end = loc[7]
			}
			value = t[loc[4]:end]
			prefix, open = t[loc[2]:loc[3]], "("
			suffix = ")"
		} else if kind == 2 {
			value = t[loc[4]:loc[5]]
			prefix, open = t[loc[2]:loc[3]], "["
			suffix = "]"
		}
		seg.Protected[k] = value
		if open == "" {
			return k, nil
		}
		n := int64(len(prefix) + len(open) + len(k) + len(suffix))
		if n > limit {
			return "", &backend.BuildCapacityError{Resource: "protected link bytes"}
		}
		if e = b.ReserveText(n, n); e != nil {
			return "", e
		}
		return prefix + open + k + suffix, nil
	}, limit)
	if e == nil {
		seg.Source = s
	}
	return e
}

// ProtectBounded dispatches the production protectors without unrestricted
// ReplaceAll/FindAll or callbacks. The caller owns the short CPU permit.
func ProtectBounded(b *backend.BuildBounds, p Protector, seg *model.Segment) error {
	return protectBoundedLimit(b, p, seg, b.Snapshot().TextLimit)
}
func protectBoundedLimit(b *backend.BuildBounds, p Protector, seg *model.Segment, limit int64) error {
	if p == nil || seg == nil {
		return nil
	}
	switch x := p.(type) {
	case *composed:
		for _, child := range x.ps {
			if e := protectBoundedLimit(b, child, seg, limit); e != nil {
				return e
			}
		}
		return mergeAdjacentBounded(b, seg, limit)
	case *CodeProtector:
		for _, r := range []*regexp.Regexp{fencedBacktickRe, fencedTildeRe, inlineCodeRe} {
			if e := protectPatternBounded(b, seg, r, 0, limit); e != nil {
				return e
			}
		}
	case *PlaceholderProtector:
		for _, r := range []*regexp.Regexp{doubleBraceRe, singleBraceRe, printfVerbRe, shellVarRe} {
			if e := protectPatternBounded(b, seg, r, 0, limit); e != nil {
				return e
			}
		}
	case *XMLProtector:
		return protectPatternBounded(b, seg, xmlTagRe, 0, limit)
	case *LinkProtector:
		for _, step := range []struct {
			r    *regexp.Regexp
			kind int
		}{{mdLinkRe, 1}, {autoLinkRe, 0}, {refLinkRe, 2}} {
			if e := protectPatternBounded(b, seg, step.r, step.kind, limit); e != nil {
				return e
			}
		}
	case *rubyProtector:
		items, s, e := ruby.ExtractBounded(b, seg.Source)
		if e != nil {
			return e
		}
		seg.Source = s
		if len(items) > 0 {
			if e = b.ReserveNodes(1, 1024); e != nil {
				return e
			}
			if seg.Meta == nil {
				seg.Meta = make(map[string]any)
			}
			seg.Meta["ruby_items"] = items
		}
	default:
		return fmt.Errorf("unsupported bounded protector")
	}
	return nil
}
func UnprotectBounded(b *backend.BuildBounds, p Protector, seg *model.Segment) error {
	if seg == nil || p == nil {
		return nil
	}
	if _, ok := p.(*rubyProtector); ok {
		return nil
	}
	limit := b.OutputLimit()
	if limit <= 0 {
		limit = b.Snapshot().TextLimit
	}
	s, e := RestoreTextBounded(b, seg.Target, seg.Protected, limit)
	if e == nil {
		seg.Target = s
	}
	return e
}
func ProtectTextBounded(b *backend.BuildBounds, p Protector, text string, maxBytes int64) (string, map[string]string, error) {
	if e := b.ReserveNodes(1, 256); e != nil {
		return "", nil, e
	}
	seg := &model.Segment{Source: text}
	if e := protectBoundedLimit(b, p, seg, maxBytes); e != nil {
		return "", nil, e
	}
	if int64(len(seg.Source)) > maxBytes {
		return "", nil, &backend.BuildCapacityError{Resource: "protected text bytes"}
	}
	return seg.Source, seg.Protected, nil
}

func RestoreTextBounded(b *backend.BuildBounds, text string, mapping map[string]string, maxBytes int64) (string, error) {
	if e := b.ReserveNodes(int64(len(mapping))+1, 64); e != nil {
		return "", e
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] > keys[j]
	})
	for _, k := range keys {
		count := int64(strings.Count(text, k))
		if count == 0 {
			continue
		}
		delta := int64(len(mapping[k])) - int64(len(k))
		n := int64(len(text))
		if delta > 0 && count > (math.MaxInt64-n)/delta {
			return "", &backend.BuildCapacityError{Resource: "placeholder restore arithmetic"}
		}
		n += count * delta
		if n < 0 || n > maxBytes {
			return "", &backend.BuildCapacityError{Resource: "placeholder restored bytes"}
		}
		if e := b.ReserveText(n, n); e != nil {
			return "", e
		}
		text = strings.ReplaceAll(text, k, mapping[k])
	}
	return text, nil
}

func PlaceholderViolationsBounded(b *backend.BuildBounds, seg *model.Segment) (missing, duplicated, invented []string, err error) {
	if seg == nil {
		return
	}
	if err = b.ReserveNodes(int64(len(seg.Protected))*2+1, 64); err != nil {
		return
	}
	missing = make([]string, 0, len(seg.Protected))
	duplicated = make([]string, 0, len(seg.Protected))
	for k := range seg.Protected {
		n := strings.Count(seg.Target, k)
		if n == 0 {
			missing = append(missing, k)
		} else if n > 1 {
			duplicated = append(duplicated, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(duplicated)
	if err = b.ReserveNodes(1, 1024); err != nil {
		return
	}
	seen := map[string]bool{}
	for from := 0; from < len(seg.Target); {
		if err = b.ReserveNodes(1, 256); err != nil {
			return
		}
		loc, findErr := backend.FindRegexpIndex(b, placeholderLikeRE, seg.Target[from:])
		if findErr != nil {
			err = findErr
			return
		}
		if loc == nil {
			break
		}
		tok := seg.Target[from+loc[0] : from+loc[1]]
		from += loc[1]
		if _, known := seg.Protected[tok]; known || seen[tok] {
			continue
		}
		if err = b.ReserveNodes(2, 1024); err != nil {
			return
		}
		seen[tok] = true
		invented, err = backend.AppendNode(b, invented, tok)
		if err != nil {
			return
		}
	}
	sort.Strings(invented)
	return
}

func mergeAdjacentBounded(b *backend.BuildBounds, seg *model.Segment, limit int64) error {
	if len(seg.Protected) == 0 {
		return nil
	}
	s, e := regexReplaceBounded(b, seg.Source, mergeAdjacentPlaceholderRE, func(t string, loc []int) (string, error) {
		match := t[loc[0]:loc[1]]
		var keys []string
		w := backend.NewBoundedWriter(b, b.Snapshot().TextLimit)
		from := 0
		for from < len(match) {
			if e := b.ReserveNodes(1, 256); e != nil {
				return "", e
			}
			l, findErr := backend.FindRegexpIndex(b, singlePlaceholderRE, match[from:])
			if findErr != nil {
				return "", findErr
			}
			if l == nil {
				break
			}
			key := match[from+l[0] : from+l[1]]
			if _, e := w.WriteString(match[from : from+l[0]]); e != nil {
				return "", e
			}
			if v, ok := seg.Protected[key]; ok {
				if _, e := w.WriteString(v); e != nil {
					return "", e
				}
			}
			var e error
			keys, e = backend.AppendNode(b, keys, key)
			if e != nil {
				return "", e
			}
			from += l[1]
		}
		if len(keys) < 2 {
			return match, nil
		}
		k, e := nextKeyBounded(b, seg)
		if e != nil {
			return "", e
		}
		if e = b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
			return "", e
		}
		seg.Protected[k] = string(w.Bytes())
		for _, old := range keys {
			delete(seg.Protected, old)
		}
		return k, nil
	}, limit)
	if e == nil {
		seg.Source = s
	}
	return e
}
