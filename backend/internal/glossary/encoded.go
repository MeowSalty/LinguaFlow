package glossary

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type EncodedAddResult struct{ Added, Skipped int }
type SourcesLookup interface {
	LookupSourcesBounded(context.Context, string, string, string, int64, func(context.Context, func() error) error) ([]string, error)
}

// AddEncoded decodes one fixed entry at a time. Full response trees do not
// accompany independent storage calls; Source/Target/Notes remain unchanged.
func AddEncoded(ctx context.Context, g Glossary, payload []byte, limit int64, build func(context.Context, func() error) error) (EncodedAddResult, error) {
	var result EncodedAddResult
	if int64(len(payload)) > limit {
		return result, backend.ErrBuildCapacity
	}
	pos := 0
	space := func() {
		for pos < len(payload) && (payload[pos] == ' ' || payload[pos] == '\n' || payload[pos] == '\r' || payload[pos] == '\t') {
			pos++
		}
	}
	space()
	if pos == len(payload) || payload[pos] != '[' {
		return result, fmt.Errorf("invalid glossary entries")
	}
	pos++
	for {
		space()
		if pos < len(payload) && payload[pos] == ']' {
			pos++
			space()
			if pos != len(payload) {
				return result, fmt.Errorf("trailing glossary entries")
			}
			return result, nil
		}
		var entry Entry
		var consumed int
		decode := func() error {
			b, e := backend.NewBuildBounds(limit, int64(len(payload)))
			if e != nil {
				return e
			}
			raw, n, e := backend.FirstJSONRaw(b, payload[pos:])
			if e != nil {
				return e
			}
			consumed = n
			return b.DecodeJSONLimited(raw, &entry, limit)
		}
		var err error
		if build != nil {
			err = build(ctx, decode)
		} else {
			err = decode()
		}
		if err != nil {
			return result, err
		}
		added, err := g.Add(ctx, entry)
		if err != nil {
			return result, err
		}
		result.Added += len(added.Added)
		result.Skipped += len(added.Skipped)
		entry = Entry{}
		added = AddResult{}
		pos += consumed
		space()
		if pos < len(payload) && payload[pos] == ',' {
			pos++
			continue
		}
		if pos < len(payload) && payload[pos] == ']' {
			continue
		}
		return result, fmt.Errorf("invalid glossary entries separator")
	}
}

func sourceContainsFold(text, source string) bool {
	for at := 0; at < len(text); {
		p, q := at, 0
		for p < len(text) && q < len(source) {
			r, n := utf8.DecodeRuneInString(text[p:])
			s, m := utf8.DecodeRuneInString(source[q:])
			if unicode.ToLower(r) != unicode.ToLower(s) {
				break
			}
			p += n
			q += m
		}
		if q == len(source) {
			return true
		}
		_, n := utf8.DecodeRuneInString(text[at:])
		at += n
	}
	return false
}

// LowerEqual compares the existing lowercase-key semantics without building
// a copy of potentially large immutable dictionary source text.
func LowerEqual(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	for len(a) > 0 && len(b) > 0 {
		r, n := utf8.DecodeRuneInString(a)
		s, m := utf8.DecodeRuneInString(b)
		if unicode.ToLower(r) != unicode.ToLower(s) {
			return false
		}
		a = a[n:]
		b = b[m:]
	}
	return len(a) == 0 && len(b) == 0
}
func LookupSourcesFromEntries(b *backend.BuildBounds, entries []Entry, text string, maxBytes int64) ([]string, error) {
	var out []string
	total := int64(0)
	text = strings.TrimSpace(text)
	for _, entry := range entries {
		if entry.Forbidden {
			continue
		}
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			continue
		}
		matched := strings.Contains(text, source)
		if !entry.CaseSensitive {
			matched = sourceContainsFold(text, source)
		}
		if !matched {
			continue
		}
		if int64(len(source)) > maxBytes-total {
			return nil, backend.ErrBuildCapacity
		}
		total += int64(len(source))
		var err error
		out, err = backend.AppendNode(b, out, entry.Source)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (g *FileGlossary) LookupSourcesBounded(ctx context.Context, text, src, tgt string, limit int64, build func(context.Context, func() error) error) ([]string, error) {
	head := g.entrySnapshot.Load()
	var out []string
	fn := func() error {
		b, e := backend.NewBuildBounds(limit, int64(len(text)))
		if e != nil {
			return e
		}
		type hit struct {
			source   string
			sequence int
		}
		var hits []hit
		total := int64(0)
		for node := head; node != nil; node = node.Previous {
			entry := node.Entry
			if entry.Forbidden {
				continue
			}
			source := strings.TrimSpace(entry.Source)
			if source == "" {
				continue
			}
			matched := strings.Contains(text, source)
			if !entry.CaseSensitive {
				matched = sourceContainsFold(text, source)
			}
			if !matched {
				continue
			}
			if int64(len(source)) > limit-total {
				return backend.ErrBuildCapacity
			}
			total += int64(len(source))
			hits, e = backend.AppendNode(b, hits, hit{entry.Source, node.Order})
			if e != nil {
				return e
			}
		}
		sort.Slice(hits, func(i, j int) bool {
			if len(hits[i].source) != len(hits[j].source) {
				return len(hits[i].source) > len(hits[j].source)
			}
			return hits[i].sequence < hits[j].sequence
		})
		for _, h := range hits {
			out, e = backend.AppendNode(b, out, h.source)
			if e != nil {
				return e
			}
		}
		return nil
	}
	var e error
	if build != nil {
		e = build(ctx, fn)
	} else {
		e = fn()
	}
	return out, e
}
