package workstate

import (
	"context"
	"strings"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
)

func checkpointScalarBytes(sel *sql.Selector, fields ...string) string {
	parts := make([]string, len(fields))
	for i, field := range fields {
		column := "COALESCE(" + sel.C(field) + ",'')"
		if sel.Dialect() == dialect.Postgres {
			parts[i] = "octet_length(" + column + ")"
		} else {
			parts[i] = "length(CAST(" + column + " AS BLOB))"
		}
	}
	return sql.As("COALESCE(MAX("+strings.Join(parts, "+")+"),0)", "bytes")
}

func loadCheckpointRound(ctx context.Context, tx *ent.Client, id int) (*ent.JobRound, error) {
	q := tx.JobRound.Query().Where(jobround.IDEQ(id))
	var sizes []struct {
		Bytes int64 `json:"bytes"`
	}
	if err := q.Clone().Aggregate(func(s *sql.Selector) string { return checkpointScalarBytes(s, jobround.FieldStatus) }).Scan(ctx, &sizes); err != nil {
		return nil, err
	}
	if len(sizes) != 1 || sizes[0].Bytes > 128 {
		return nil, ErrManifest
	}
	return q.Select(jobround.FieldID, jobround.FieldJobID, jobround.FieldStatus, jobround.FieldManifestSealed, jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted).Only(ctx)
}

func loadCheckpointFacts(ctx context.Context, q *ent.JobRoundSegmentQuery) ([]*ent.JobRoundSegment, error) {
	var sizes []struct {
		Bytes int64 `json:"bytes"`
	}
	if err := q.Clone().Aggregate(func(s *sql.Selector) string {
		return checkpointScalarBytes(s, jobroundsegment.FieldCommitID, jobroundsegment.FieldCandidateID, jobroundsegment.FieldOutcome)
	}).Scan(ctx, &sizes); err != nil {
		return nil, err
	}
	if len(sizes) != 1 || sizes[0].Bytes > 384 {
		return nil, ErrManifest
	}
	return q.Select(jobroundsegment.FieldID, jobroundsegment.FieldJobRoundID, jobroundsegment.FieldSegmentID, jobroundsegment.FieldCommitID, jobroundsegment.FieldCandidateID, jobroundsegment.FieldOutcome).All(ctx)
}
