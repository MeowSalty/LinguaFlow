package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func (g *DatabaseGlossary) publishEntrySnapshotLocked() {
	var head *glossary.EntrySnapshot
	for i, e := range g.entries {
		head = &glossary.EntrySnapshot{Entry: e, Order: i, Previous: head}
	}
	g.entrySnapshot.Store(head)
}
func (g *DatabaseGlossary) addCachedEntryLocked(e glossary.Entry) {
	g.entries = append(g.entries, e)
	g.entrySnapshot.Store(&glossary.EntrySnapshot{Entry: e, Order: len(g.entries) - 1, Previous: g.entrySnapshot.Load()})
}
func (g *DatabaseGlossary) LookupEntriesBounded(ctx context.Context, b *backend.BuildBounds, text, _, _ string) ([]glossary.Entry, error) {
	return glossary.LookupEntrySnapshotContextBounded(ctx, b, g.entrySnapshot.Load(), text, false)
}
func (g *DatabaseGlossary) VisitMatchesBounded(ctx context.Context, text, _, _ string, b *backend.BuildBounds, run func(context.Context, func() error) error, yield func(glossary.Entry) error) error {
	return glossary.VisitEntrySnapshotMatches(ctx, b, g.entrySnapshot.Load(), text, false, run, yield)
}
