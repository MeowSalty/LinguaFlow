package service

import (
	"context"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type DatabaseGlossary struct {
	client *ent.Client
	scope  glossaryScope
}

func NewDatabaseGlossary(client *ent.Client, projectRow *ent.Project) (*DatabaseGlossary, error) {
	scope, err := glossaryScopeFromProject(projectRow)
	if err != nil {
		return nil, err
	}
	return &DatabaseGlossary{client: client, scope: scope}, nil
}

func (g *DatabaseGlossary) Lookup(ctx context.Context, text, _, _ string) ([]glossary.Entry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	rows, err := g.client.GlossaryEntry.Query().
		Where(glossaryentry.ScopeKeyEQ(g.scope.key)).
		Order(ent.Asc(glossaryentry.FieldSourceKey), ent.Asc(glossaryentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]glossary.Entry, 0)
	lowerText := strings.ToLower(text)
	for _, row := range rows {
		source := strings.TrimSpace(row.Source)
		if source == "" {
			continue
		}
		matched := strings.Contains(text, source)
		if !row.CaseSensitive {
			matched = strings.Contains(lowerText, strings.ToLower(source))
		}
		if matched {
			matches = append(matches, glossary.Entry{
				Source:        row.Source,
				Target:        row.Target,
				CaseSensitive: row.CaseSensitive,
				Notes:         row.Notes,
			})
		}
	}
	return matches, nil
}

func (g *DatabaseGlossary) Add(ctx context.Context, entries ...glossary.Entry) (glossary.AddResult, error) {
	var result glossary.AddResult
	for _, entry := range entries {
		input := GlossaryEntryInput{
			Source:        entry.Source,
			Target:        entry.Target,
			CaseSensitive: entry.CaseSensitive,
			Notes:         entry.Notes,
		}
		normalized, err := normalizeGlossaryEntryInput(input)
		if err != nil {
			result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Reason: glossary.SkipReasonEmpty})
			continue
		}
		existing, err := g.client.GlossaryEntry.Query().
			Where(glossaryentry.ScopeKeyEQ(g.scope.key), glossaryentry.SourceKeyEQ(glossarySourceKey(normalized.Source))).
			Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return result, err
		}
		if existing != nil {
			existingEntry := glossary.Entry{Source: existing.Source, Target: existing.Target, CaseSensitive: existing.CaseSensitive, Notes: existing.Notes}
			if existing.Target != normalized.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: entry, Existing: existingEntry, Reason: glossary.SkipReasonExists})
			}
			continue
		}
		created, err := createGlossaryEntry(ctx, g.client, g.scope, normalized)
		if err != nil {
			if strings.Contains(err.Error(), ErrGlossaryEntryExists.Error()) {
				continue
			}
			return result, err
		}
		result.Added = append(result.Added, glossary.Entry{Source: created.Source, Target: created.Target, CaseSensitive: created.CaseSensitive, Notes: created.Notes})
	}
	return result, nil
}
