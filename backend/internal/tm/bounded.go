package tm

import (
	"context"
	"crypto/sha256"
	stdsql "database/sql"
	"encoding/hex"
	"errors"
	"strings"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/tmentry"
)

type BoundedVisitor interface {
	VisitMatchesBounded(context.Context, string, string, string, *backend.BuildBounds, func(context.Context, func() error) error, func(Match) error) error
}
type inputReadCreditKey struct{}

func WithInputReadCredit(ctx context.Context, claim func(int64) error) context.Context {
	return context.WithValue(ctx, inputReadCreditKey{}, claim)
}

func VisitMatchesBounded(ctx context.Context, m TranslationMemory, source, srcLang, tgtLang string, b *backend.BuildBounds, run func(context.Context, func() error) error, yield func(Match) error) error {
	visitor, ok := m.(BoundedVisitor)
	if !ok {
		// This guard is a capacity refusal: callers must not downgrade an
		// unsupported plugin to a best-effort warning and silently omit hints.
		return backend.ErrBuildCapacity
	}
	return visitor.VisitMatchesBounded(ctx, source, srcLang, tgtLang, b, run, yield)
}

func (Nop) VisitMatchesBounded(context.Context, string, string, string, *backend.BuildBounds, func(context.Context, func() error) error, func(Match) error) error {
	return nil
}

type matchHeader struct {
	ID          int   `sql:"id"`
	SourceBytes int64 `sql:"source_bytes"`
	TargetBytes int64 `sql:"target_bytes"`
}

// The published unique key permits one exact scoped source-hash match. Read its
// byte header and body in one snapshot, then update usage only after yield
// succeeds. PostgreSQL needs repeatable-read isolation for the two reads.
func (s *SQLite) VisitMatchesBounded(ctx context.Context, source, srcLang, tgtLang string, b *backend.BuildBounds, run func(context.Context, func() error) error, yield func(Match) error) error {
	if run == nil {
		run = func(ctx context.Context, fn func() error) error { return backend.RunBuild(ctx, fn) }
	}
	var key, digest string
	e := run(ctx, func() error {
		var e error
		key, e = s.scope.key()
		if e != nil {
			return e
		}
		h := sha256.New()
		var scratch [4096]byte
		text := strings.TrimSpace(source)
		for len(text) > 0 {
			n := copy(scratch[:], text)
			_, _ = h.Write(scratch[:n])
			text = text[n:]
		}
		digest = hex.EncodeToString(h.Sum(nil))
		return b.ReserveText(0, int64(len(key)+len(digest)))
	})
	if e != nil {
		return e
	}
	// ORM/driver internals have no known capacity. The query snapshot and
	// actual stored string bytes remain part of the input contract.
	tx, e := s.client.BeginTx(ctx, &stdsql.TxOptions{Isolation: stdsql.LevelRepeatableRead})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	base := func() *ent.TMEntryQuery {
		return tx.TMEntry.Query().Where(tmentry.ScopeKeyEQ(key), tmentry.SourceHashEQ(digest), tmentry.SourceLangEQ(strings.TrimSpace(srcLang)), tmentry.TargetLangEQ(strings.TrimSpace(tgtLang)))
	}
	var headers []matchHeader
	// The second header detects a violated unique-key invariant without an
	// unbounded header allocation or a separate COUNT query.
	e = base().Limit(2).Select().Modify(func(q *sql.Selector) {
		q.SelectExpr(sql.Expr(q.C(tmentry.FieldID)+" AS id"), sql.Expr(matchTextBytes(q, tmentry.FieldSourceText)+" AS source_bytes"), sql.Expr(matchTextBytes(q, tmentry.FieldTargetText)+" AS target_bytes"))
	}).Scan(ctx, &headers)
	if e != nil {
		return e
	}
	if len(headers) > 1 {
		return errors.New("TM match unique key violated")
	}
	if e = backend.ReserveSlice[matchHeader](b, int64(cap(headers))); e != nil {
		return e
	}
	for _, header := range headers {
		if header.SourceBytes < 0 || header.TargetBytes < 0 || header.SourceBytes > b.OutputLimit()-header.TargetBytes {
			return backend.ErrBuildCapacity
		}
		if claim, ok := ctx.Value(inputReadCreditKey{}).(func(int64) error); ok {
			if e = claim(header.SourceBytes + header.TargetBytes); e != nil {
				return e
			}
		} else {
			e = run(ctx, func() error {
				return b.ReserveText(0, header.SourceBytes+header.TargetBytes)
			})
			if e != nil {
				return e
			}
		}
		row, e := tx.TMEntry.Query().Where(tmentry.IDEQ(header.ID)).Select(tmentry.FieldID, tmentry.FieldSourceText, tmentry.FieldTargetText).First(ctx)
		if e != nil {
			return e
		}
		if int64(len(row.SourceText)) != header.SourceBytes || int64(len(row.TargetText)) != header.TargetBytes {
			return errors.New("TM match body byte header changed")
		}
		if e = yield(Match{Source: row.SourceText, Target: row.TargetText, Score: 1}); e != nil {
			return e
		}
		row = nil
		// TM entries have no storage mutation hook. This scoped update retains
		// TimeMixin's updated_at behavior and avoids UpdateOne's entity readback.
		changed, e := tx.TMEntry.Update().Where(tmentry.IDEQ(header.ID)).AddUsageCount(1).Save(ctx)
		if e != nil {
			return e
		}
		if changed != 1 {
			return errors.New("TM usage row disappeared")
		}
	}
	headers = nil
	return tx.Commit()
}

func matchTextBytes(q *sql.Selector, column string) string {
	if q.Dialect() == dialect.Postgres {
		return "OCTET_LENGTH(" + q.C(column) + ")"
	}
	return "LENGTH(CAST(" + q.C(column) + " AS BLOB))"
}
