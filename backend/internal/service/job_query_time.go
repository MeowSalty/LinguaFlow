package service

import (
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// jobUpdatedAtCompare keeps the indexed column bare. PostgreSQL stores whole
// microseconds: both >= from and < before require ceiling a fractional bound,
// otherwise pgx's truncation changes which stored instants match the interval.
func jobUpdatedAtCompare(op sql.Op, instant time.Time) predicate.Job {
	return func(s *sql.Selector) {
		bound := timeutil.Normalize(instant)
		if s.Dialect() == dialect.Postgres && (op == sql.OpGTE || op == sql.OpLT) {
			bound = timeutil.CeilMicrosecond(bound)
		}
		s.Where(sql.P(func(b *sql.Builder) {
			b.Ident(s.C(job.FieldUpdatedAt)).WriteOp(op).Arg(bound)
		}))
	}
}

func jobUpdatedAtDescending(s *sql.Selector) {
	s.OrderBy(sql.Desc(s.C(job.FieldUpdatedAt)))
}
