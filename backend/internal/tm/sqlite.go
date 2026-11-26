package tm

import (
	"context"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
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
		Select(tmentry.FieldID, tmentry.FieldSourceText, tmentry.FieldTargetText).
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
	key, err := s.scope.key()
	if err != nil {
		return err
	}
	// Both supported dialects accept numbered parameters. TM has no mutation
	// or storage hook; explicit defaults retain its published row contract.
	// Concurrent different translations follow database write order: the last
	// successful content-changing write wins. A duplicate leaves all fields,
	// including updated_at, untouched; content changes retain usage and identity.
	now := timeutil.NowUTC()
	_, err = s.client.ExecContext(ctx, `INSERT INTO "tm_entries"
		("created_at", "updated_at", "scope_key", "source_hash", "source_text", "target_text", "source_lang", "target_lang", "usage_count", "project_id")
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 0, $9)
		ON CONFLICT ("scope_key", "source_hash", "source_lang", "target_lang") DO UPDATE SET
			"source_text" = excluded."source_text",
			"target_text" = excluded."target_text",
			"updated_at" = excluded."updated_at"
		WHERE "tm_entries"."source_text" <> excluded."source_text"
			OR "tm_entries"."target_text" <> excluded."target_text"`,
		now, now, key, hash.Full(src), src, tgt, srcLang, tgtLang, s.scope.ProjectID)
	if err != nil {
		return fmt.Errorf("tm: upsert entry: %w", err)
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
	if s.ProjectID <= 0 {
		return "", fmt.Errorf("tm: empty scope")
	}
	return fmt.Sprintf("project:%d", s.ProjectID), nil
}
