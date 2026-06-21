package service

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
)

var (
	ErrGlossaryEntryNotFound = errors.New("glossary entry not found")
	ErrGlossaryEntryExists   = errors.New("glossary entry already exists")
)

var glossaryCSVHeader = []string{"source", "target", "case_sensitive", "notes"}

type GlossaryService struct {
	client   *ent.Client
	projects *ProjectService
}

type GlossaryEntryInput struct {
	Source        string
	Target        string
	CaseSensitive bool
	Notes         string
}

type GlossaryImportSkipped struct {
	Line   int
	Source string
	Reason string
}

type GlossaryImportResult struct {
	Added   int
	Skipped []GlossaryImportSkipped
}

// GlossaryEntryUpdateResult 包含更新结果和变更信息
type GlossaryEntryUpdateResult struct {
	Entry         *ent.GlossaryEntry
	TargetChanged bool
}

func NewGlossaryService(client *ent.Client, projects *ProjectService) *GlossaryService {
	return &GlossaryService{client: client, projects: projects}
}

func (s *GlossaryService) ListEntries(ctx context.Context, actorUserID, projectID int) ([]*ent.GlossaryEntry, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}
	return s.client.GlossaryEntry.Query().
		Where(glossaryentry.ProjectIDEQ(projectID)).
		Order(ent.Asc(glossaryentry.FieldSourceKey), ent.Asc(glossaryentry.FieldID)).
		All(ctx)
}

// GetEntry 获取单个术语条目，验证项目归属。
func (s *GlossaryService) GetEntry(ctx context.Context, actorUserID, projectID, entryID int) (*ent.GlossaryEntry, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}
	entry, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrGlossaryEntryNotFound
		}
		return nil, err
	}
	return entry, nil
}

func (s *GlossaryService) CreateEntry(ctx context.Context, actorUserID, projectID int, input GlossaryEntryInput) (*ent.GlossaryEntry, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	normalized, err := normalizeGlossaryEntryInput(input)
	if err != nil {
		return nil, err
	}
	return createGlossaryEntry(ctx, s.client, projectID, normalized)
}

func (s *GlossaryService) UpdateEntry(ctx context.Context, actorUserID, projectID, entryID int, input GlossaryEntryInput) (*GlossaryEntryUpdateResult, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	normalized, err := normalizeGlossaryEntryInput(input)
	if err != nil {
		return nil, err
	}
	oldEntry, err := s.client.GlossaryEntry.Query().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrGlossaryEntryNotFound
		}
		return nil, err
	}
	oldTarget := oldEntry.Target
	updated, err := s.client.GlossaryEntry.UpdateOneID(oldEntry.ID).
		SetSource(normalized.Source).
		SetSourceKey(glossarySourceKey(normalized.Source)).
		SetTarget(normalized.Target).
		SetCaseSensitive(normalized.CaseSensitive).
		SetNotes(normalized.Notes).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrGlossaryEntryExists
		}
		return nil, err
	}
	targetChanged := oldTarget != normalized.Target
	return &GlossaryEntryUpdateResult{
		Entry:         updated,
		TargetChanged: targetChanged,
	}, nil
}

func (s *GlossaryService) DeleteEntry(ctx context.Context, actorUserID, projectID, entryID int) error {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return err
	}
	deleted, err := s.client.GlossaryEntry.Delete().
		Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrGlossaryEntryNotFound
	}
	return nil
}

func (s *GlossaryService) ImportCSV(ctx context.Context, actorUserID, projectID int, r io.Reader) (*GlossaryImportResult, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	result := &GlossaryImportResult{}
	first := true
	for lineNo := 1; ; lineNo++ {
		rec, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("glossary import line %d: %w", lineNo, readErr)
		}
		if first {
			first = false
			if isGlossaryCSVHeader(rec) {
				continue
			}
		}
		if len(rec) < 2 {
			result.Skipped = append(result.Skipped, GlossaryImportSkipped{Line: lineNo, Reason: "short_row"})
			continue
		}
		input := GlossaryEntryInput{
			Source: strings.TrimSpace(rec[0]),
			Target: strings.TrimSpace(rec[1]),
		}
		if len(rec) >= 3 {
			input.CaseSensitive = parseGlossaryBool(rec[2])
		}
		if len(rec) >= 4 {
			input.Notes = strings.TrimSpace(rec[3])
		}
		normalized, normErr := normalizeGlossaryEntryInput(input)
		if normErr != nil {
			result.Skipped = append(result.Skipped, GlossaryImportSkipped{Line: lineNo, Source: input.Source, Reason: "invalid_entry"})
			continue
		}
		if _, err := createGlossaryEntry(ctx, s.client, projectID, normalized); err != nil {
			switch {
			case errors.Is(err, ErrGlossaryEntryExists):
				result.Skipped = append(result.Skipped, GlossaryImportSkipped{Line: lineNo, Source: normalized.Source, Reason: "duplicate"})
			case errors.Is(err, ErrInvalidInput):
				result.Skipped = append(result.Skipped, GlossaryImportSkipped{Line: lineNo, Source: normalized.Source, Reason: "invalid_entry"})
			default:
				return nil, err
			}
			continue
		}
		result.Added++
	}
	return result, nil
}

func (s *GlossaryService) ExportCSV(ctx context.Context, actorUserID, projectID int, w io.Writer) error {
	entries, err := s.ListEntries(ctx, actorUserID, projectID)
	if err != nil {
		return err
	}
	writer := csv.NewWriter(w)
	if err := writer.Write(glossaryCSVHeader); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := writer.Write([]string{
			entry.Source,
			entry.Target,
			formatGlossaryBool(entry.CaseSensitive),
			entry.Notes,
		}); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func createGlossaryEntry(ctx context.Context, client *ent.Client, projectID int, input GlossaryEntryInput) (*ent.GlossaryEntry, error) {
	created, err := client.GlossaryEntry.Create().
		SetProjectID(projectID).
		SetSourceKey(glossarySourceKey(input.Source)).
		SetSource(input.Source).
		SetTarget(input.Target).
		SetCaseSensitive(input.CaseSensitive).
		SetNotes(input.Notes).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrGlossaryEntryExists
		}
		return nil, err
	}
	return created, nil
}

func normalizeGlossaryEntryInput(input GlossaryEntryInput) (GlossaryEntryInput, error) {
	source := strings.TrimSpace(input.Source)
	target := strings.TrimSpace(input.Target)
	if source == "" || target == "" {
		return GlossaryEntryInput{}, ErrInvalidInput
	}
	return GlossaryEntryInput{
		Source:        source,
		Target:        target,
		CaseSensitive: input.CaseSensitive,
		Notes:         strings.TrimSpace(input.Notes),
	}, nil
}

func glossarySourceKey(source string) string {
	return strings.ToLower(strings.TrimSpace(source))
}

func parseGlossaryBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func formatGlossaryBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func isGlossaryCSVHeader(rec []string) bool {
	if len(rec) < 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(rec[0]), glossaryCSVHeader[0]) &&
		strings.EqualFold(strings.TrimSpace(rec[1]), glossaryCSVHeader[1])
}

var _ = project.FieldID
