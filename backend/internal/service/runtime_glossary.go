package service

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type DatabaseGlossary struct {
	client      *ent.Client
	projectID   int
	mu          sync.RWMutex
	entries     []glossary.Entry
	lookupCache map[string][]glossary.Entry
	revision    uint64
}

func NewDatabaseGlossary(ctx context.Context, client *ent.Client, projectRow *ent.Project) (*DatabaseGlossary, error) {
	if projectRow == nil {
		return nil, ErrProjectNotFound
	}
	rows, err := client.GlossaryEntry.Query().
		Where(glossaryentry.ProjectIDEQ(projectRow.ID)).
		Order(ent.Asc(glossaryentry.FieldSourceKey), ent.Asc(glossaryentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]glossary.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, glossaryEntryFromRow(row))
	}
	return &DatabaseGlossary{
		client:      client,
		projectID:   projectRow.ID,
		entries:     entries,
		lookupCache: make(map[string][]glossary.Entry),
	}, nil
}

func (g *DatabaseGlossary) Lookup(_ context.Context, text, _, _ string) ([]glossary.Entry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	g.mu.RLock()
	if cached, ok := g.lookupCache[text]; ok {
		matches := append([]glossary.Entry(nil), cached...)
		g.mu.RUnlock()
		return matches, nil
	}
	revision := g.revision
	matches := make([]glossary.Entry, 0)
	lowerText := strings.ToLower(text)
	for _, entry := range g.entries {
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			continue
		}
		matched := strings.Contains(text, source)
		if !entry.CaseSensitive {
			matched = strings.Contains(lowerText, strings.ToLower(source))
		}
		if matched {
			matches = append(matches, entry)
		}
	}
	g.mu.RUnlock()

	g.mu.Lock()
	if g.revision == revision {
		g.lookupCache[text] = append([]glossary.Entry(nil), matches...)
	}
	g.mu.Unlock()
	return matches, nil
}

func (g *DatabaseGlossary) Add(ctx context.Context, entries ...glossary.Entry) (glossary.AddResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var result glossary.AddResult
	for _, entry := range entries {
		input := GlossaryEntryInput{
			Source:        entry.Source,
			Target:        entry.Target,
			CaseSensitive: entry.CaseSensitive,
			Forbidden:     entry.Forbidden,
			Mandatory:     &entry.Mandatory,
			Notes:         entry.Notes,
		}
		normalized, err := normalizeGlossaryEntryInput(input)
		if err != nil {
			result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Reason: glossary.SkipReasonEmpty})
			continue
		}
		if existing, ok := findCachedGlossaryEntry(g.entries, normalized); ok {
			if existing.Target != normalized.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Existing: existing, Reason: glossary.SkipReasonExists})
			}
			continue
		}
		created, err := createGlossaryEntry(ctx, g.client, g.projectID, normalized)
		if err != nil {
			if errors.Is(err, ErrGlossaryEntryExists) {
				continue
			}
			return result, err
		}
		added := glossaryEntryFromRow(created)
		g.entries = append(g.entries, added)
		g.revision++
		clear(g.lookupCache)
		result.Added = append(result.Added, added)
	}
	return result, nil
}

func findCachedGlossaryEntry(entries []glossary.Entry, input GlossaryEntryInput) (glossary.Entry, bool) {
	sourceKey := glossarySourceKey(input.Source)
	for _, entry := range entries {
		if entry.Forbidden != input.Forbidden || glossarySourceKey(entry.Source) != sourceKey {
			continue
		}
		if input.Forbidden && entry.Target != input.Target {
			continue
		}
		return entry, true
	}
	return glossary.Entry{}, false
}

func glossaryEntryFromRow(row *ent.GlossaryEntry) glossary.Entry {
	return glossary.Entry{
		Source:        row.Source,
		Target:        row.Target,
		CaseSensitive: row.CaseSensitive,
		Forbidden:     row.Forbidden,
		Mandatory:     row.Mandatory,
		Notes:         row.Notes,
	}
}
