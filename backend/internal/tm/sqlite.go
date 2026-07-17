package tm

import (
	"context"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
)

type Scope struct {
	ProjectID int
}

type SQLite struct {
	client *ent.Client
	scope  Scope
}

func NewSQLite(client *ent.Client, scope Scope) (*SQLite, error) {
	if client == nil {
		return nil, fmt.Errorf("tm: nil client")
	}
	if _, err := scope.key(); err != nil {
		return nil, err
	}
	return &SQLite{client: client, scope: scope}, nil
}

func ScopeFromProject(project *ent.Project) (Scope, error) {
	if project == nil {
		return Scope{}, fmt.Errorf("tm: nil project")
	}
	return Scope{ProjectID: project.ID}, nil
}

func (s *SQLite) Search(ctx context.Context, src, srcLang, tgtLang string) ([]Match, error) {
	query, err := s.baseQuery(strings.TrimSpace(src), srcLang, tgtLang)
	if err != nil {
		return nil, err
	}
	rows, err := query.
		Order(ent.Desc(tmentry.FieldUsageCount), ent.Asc(tmentry.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]int, 0, len(rows))
	result := make([]Match, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		result = append(result, Match{Source: row.SourceText, Target: row.TargetText, Score: 1})
	}
	if err := s.client.TMEntry.Update().Where(tmentry.IDIn(ids...)).AddUsageCount(1).Exec(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SQLite) Add(ctx context.Context, src, tgt, srcLang, tgtLang string) error {
	src = strings.TrimSpace(src)
	tgt = strings.TrimSpace(tgt)
	srcLang = strings.TrimSpace(srcLang)
	tgtLang = strings.TrimSpace(tgtLang)
	if src == "" || tgt == "" || srcLang == "" || tgtLang == "" {
		return nil
	}
	query, err := s.baseQuery(src, srcLang, tgtLang)
	if err != nil {
		return err
	}
	existing, err := query.Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if ent.IsNotFound(err) {
		return s.createEntry(ctx, src, tgt, srcLang, tgtLang)
	}
	if existing.TargetText == tgt && existing.SourceText == src {
		return nil
	}
	return s.client.TMEntry.UpdateOneID(existing.ID).
		SetSourceText(src).
		SetTargetText(tgt).
		Exec(ctx)
}

func (s *SQLite) createEntry(ctx context.Context, src, tgt, srcLang, tgtLang string) error {
	key, err := s.scope.key()
	if err != nil {
		return err
	}
	create := s.client.TMEntry.Create().
		SetScopeKey(key).
		SetSourceHash(hash.Full(src)).
		SetSourceText(src).
		SetTargetText(tgt).
		SetSourceLang(srcLang).
		SetTargetLang(tgtLang).
		SetProjectID(s.scope.ProjectID)
	if _, err := create.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil
		}
		return err
	}
	return nil
}

func (s *SQLite) baseQuery(src, srcLang, tgtLang string) (*ent.TMEntryQuery, error) {
	key, err := s.scope.key()
	if err != nil {
		return nil, err
	}
	return s.client.TMEntry.Query().Where(
		tmentry.ScopeKeyEQ(key),
		tmentry.SourceHashEQ(hash.Full(src)),
		tmentry.SourceLangEQ(strings.TrimSpace(srcLang)),
		tmentry.TargetLangEQ(strings.TrimSpace(tgtLang)),
	), nil
}

func (s Scope) key() (string, error) {
	if s.ProjectID == 0 {
		return "", fmt.Errorf("tm: empty scope")
	}
	return fmt.Sprintf("project:%d", s.ProjectID), nil
}
