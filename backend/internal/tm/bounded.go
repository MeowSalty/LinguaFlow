package tm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

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

type matchStats struct {
	Count int64 `sql:"matches"`
	Bytes int64 `sql:"payload_bytes"`
}
type matchHeader struct {
	ID          int   `sql:"id"`
	SourceBytes int64 `sql:"source_bytes"`
	TargetBytes int64 `sql:"target_bytes"`
}

// SQLite's existing Search matches the exact scoped source hash, returns score
// one and orders usage DESC/id ASC. A transaction preserves that snapshot and
// increments every matched usage counter only if the complete visit succeeds.
// SQL count/byte headers reject oversized input before any Source/Target read.
func (s *SQLite) VisitMatchesBounded(ctx context.Context, source, srcLang, tgtLang string, b *backend.BuildBounds, run func(context.Context, func() error) error, yield func(Match) error) error {
	if run == nil {
		run = func(ctx context.Context, fn func() error) error { return backend.RunBuild(ctx, fn) }
	}
	var key, digest string
	e := run(ctx, func() error {
		if e := b.ReserveNodes(80, 1024); e != nil {
			return e
		}
		if e := b.ReserveText(160, 160); e != nil {
			return e
		}
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
		return nil
	})
	if e != nil {
		return e
	}
	// Tx/client fixed records, query objects and scanner records receive their
	// tickets before constructors or driver reads. No database call holds Run.
	tx, e := s.client.Tx(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	base := func() *ent.TMEntryQuery {
		return tx.TMEntry.Query().Where(tmentry.ScopeKeyEQ(key), tmentry.SourceHashEQ(digest), tmentry.SourceLangEQ(strings.TrimSpace(srcLang)), tmentry.TargetLangEQ(strings.TrimSpace(tgtLang)))
	}
	reserveQuery := func() error {
		return run(ctx, func() error {
			if e := b.ReserveNodes(32, 1024); e != nil {
				return e
			}
			return b.ReserveText(512, 512)
		})
	}
	if e = reserveQuery(); e != nil {
		return e
	}
	var stats []matchStats
	e = base().Select().Modify(func(q *sql.Selector) {
		q.SelectExpr(sql.Expr("COUNT(*) AS matches"), sql.Expr("COALESCE(SUM(length(CAST(source_text AS BLOB))+length(CAST(target_text AS BLOB))),0) AS payload_bytes"))
	}).Scan(ctx, &stats)
	if e != nil {
		return e
	}
	if len(stats) != 1 || stats[0].Count < 0 || stats[0].Bytes < 0 {
		return errors.New("invalid TM match byte header")
	}
	if stats[0].Bytes > b.OutputLimit() {
		return backend.ErrBuildCapacity
	}
	count := stats[0].Count
	if count == 0 {
		return tx.Commit()
	}
	e = run(ctx, func() error {
		if e := b.ReserveNodes(count, 1024); e != nil {
			return e
		}
		return b.ReserveNodes(1, 24)
	})
	if e != nil {
		return e
	}
	if e = reserveQuery(); e != nil {
		return e
	}
	var headers []matchHeader
	e = base().Order(ent.Desc(tmentry.FieldUsageCount), ent.Asc(tmentry.FieldID)).Select().Modify(func(q *sql.Selector) {
		q.SelectExpr(sql.Expr("id AS id"), sql.Expr("length(CAST(source_text AS BLOB)) AS source_bytes"), sql.Expr("length(CAST(target_text AS BLOB)) AS target_bytes"))
	}).Scan(ctx, &headers)
	if e != nil {
		return e
	}
	if int64(len(headers)) != count {
		return errors.New("TM match header count changed")
	}
	for _, header := range headers {
		if header.SourceBytes < 0 || header.TargetBytes < 0 || header.SourceBytes > b.OutputLimit()-header.TargetBytes {
			return backend.ErrBuildCapacity
		}
		if claim, ok := ctx.Value(inputReadCreditKey{}).(func(int64) error); ok {
			if e = claim(header.SourceBytes + header.TargetBytes); e != nil {
				return e
			}
		}
		e = run(ctx, func() error {
			return b.ReserveText(header.SourceBytes+header.TargetBytes, header.SourceBytes+header.TargetBytes)
		})
		if e != nil {
			return e
		}
		if e = reserveQuery(); e != nil {
			return e
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
		if e = reserveQuery(); e != nil {
			return e
		}
		if e = tx.TMEntry.UpdateOneID(header.ID).AddUsageCount(1).Exec(ctx); e != nil {
			return e
		}
	}
	headers = nil
	return tx.Commit()
}
