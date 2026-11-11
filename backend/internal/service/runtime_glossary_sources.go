package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func (g *DatabaseGlossary) LookupSourcesBounded(ctx context.Context, text, src, tgt string, limit int64, build func(context.Context, func() error) error) ([]string, error) {
	g.mu.RLock()
	entries := g.entries
	g.mu.RUnlock() // append-only immutable entries
	var out []string
	fn := func() error {
		b, e := backend.NewBuildBounds(limit, int64(len(text)))
		if e != nil {
			return e
		}
		out, e = glossary.LookupSourcesFromEntries(b, entries, text, limit)
		return e
	}
	var e error
	if build != nil {
		e = build(ctx, fn)
	} else {
		e = fn()
	}
	return out, e
}
