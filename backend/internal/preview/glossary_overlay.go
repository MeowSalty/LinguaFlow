// Package preview provides the in-memory, side-effect-free runtime for single
// segment translation previews.
package preview

import (
	"context"
	"strings"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// OverlayGlossary wraps a read-only base Glossary with an in-memory overlay.
// Lookup merges results from both layers (base wins on conflict). Add only
// mutates the overlay so extracted terms are visible to subsequent translate
// rounds without persisting them.
//
// NOTE: OverlayGlossary intentionally does NOT implement glossary.Saver, so
// engine.SaveGlossary is a no-op for preview sessions.
type OverlayGlossary struct {
	base    glossary.Glossary
	mu      sync.RWMutex
	overlay []glossary.Entry
}

func NewOverlayGlossary(base glossary.Glossary) *OverlayGlossary {
	return &OverlayGlossary{base: base}
}

func glossarySourceKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (g *OverlayGlossary) Lookup(ctx context.Context, text, srcLang, tgtLang string) ([]glossary.Entry, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	g.mu.RLock()
	matches := append([]glossary.Entry(nil), g.overlay...)
	g.mu.RUnlock()

	lowerText := strings.ToLower(text)
	var overlayMatches []glossary.Entry
	for _, entry := range matches {
		source := strings.TrimSpace(entry.Source)
		if source == "" {
			continue
		}
		matched := strings.Contains(text, source)
		if !entry.CaseSensitive {
			matched = strings.Contains(lowerText, strings.ToLower(source))
		}
		if matched {
			overlayMatches = append(overlayMatches, entry)
		}
	}

	if g.base == nil {
		return overlayMatches, nil
	}

	baseMatches, err := g.base.Lookup(ctx, text, srcLang, tgtLang)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(baseMatches))
	for _, e := range baseMatches {
		seen[glossarySourceKey(e.Source)] = true
	}

	result := make([]glossary.Entry, 0, len(baseMatches)+len(overlayMatches))
	result = append(result, baseMatches...)
	for _, e := range overlayMatches {
		if !seen[glossarySourceKey(e.Source)] {
			result = append(result, e)
		}
	}
	return result, nil
}

func (g *OverlayGlossary) Add(ctx context.Context, entries ...glossary.Entry) (glossary.AddResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var result glossary.AddResult

	for _, entry := range entries {
		if strings.TrimSpace(entry.Source) == "" || strings.TrimSpace(entry.Target) == "" {
			result.Skipped = append(result.Skipped, glossary.SkippedEntry{
				Proposed: entry,
				Reason:   glossary.SkipReasonEmpty,
			})
			continue
		}

		sourceKey := glossarySourceKey(entry.Source)
		existing, found := g.findBySourceKey(sourceKey, entry.Forbidden)
		if found {
			if existing.Target != entry.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{
					Proposed: entry,
					Existing: existing,
					Reason:   glossary.SkipReasonExists,
				})
			}
			continue
		}

		entryCopy := entry
		g.overlay = append(g.overlay, entryCopy)
		result.Added = append(result.Added, entryCopy)
	}

	return result, nil
}

func (g *OverlayGlossary) findBySourceKey(sourceKey string, forbidden bool) (glossary.Entry, bool) {
	for _, entry := range g.overlay {
		if glossarySourceKey(entry.Source) == sourceKey && entry.Forbidden == forbidden {
			return entry, true
		}
	}
	return glossary.Entry{}, false
}
