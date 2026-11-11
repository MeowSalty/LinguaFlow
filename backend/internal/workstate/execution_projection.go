package workstate

import (
	"context"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

const ExecutionDefinitionReadLimit int64 = 64 << 20

// SupportedExecution projects only a boolean. CASE checks physical bytes before
// examining JSON headers, and JSON comparisons never return a caller sized value.
func SupportedExecution(limit int64) predicate.Job {
	if limit <= 0 || limit > ExecutionDefinitionReadLimit {
		limit = ExecutionDefinitionReadLimit
	}
	return func(s *sql.Selector) {
		c := s.C(job.FieldExecutionConfig)
		n := "COALESCE(length(CAST(" + c + " AS BLOB)),0)"
		format := fmt.Sprintf("json_type(%s,'$.schema_version') IN ('integer','real') AND json_extract(%s,'$.schema_version')=%d AND json_type(%s,'$.defaults_version') IN ('integer','real') AND json_extract(%s,'$.defaults_version')=%d AND json_type(%s,'$.response_format_version') IN ('integer','real') AND json_extract(%s,'$.response_format_version')=%d", c, c, execution.SnapshotSchemaVersion, c, c, execution.SnapshotDefaultsVersion, c, c, execution.ResponseFormatVersion)
		if s.Dialect() == dialect.Postgres {
			n = "COALESCE(octet_length(CAST(" + c + " AS TEXT)),0)"
			format = fmt.Sprintf("(%s->'schema_version')='%d'::jsonb AND (%s->'defaults_version')='%d'::jsonb AND (%s->'response_format_version')='%d'::jsonb", c, execution.SnapshotSchemaVersion, c, execution.SnapshotDefaultsVersion, c, execution.ResponseFormatVersion)
		}
		valid := ""
		if s.Dialect() != dialect.Postgres {
			valid = " AND json_valid(" + c + ")"
		}
		s.Where(sql.ExprP(fmt.Sprintf("CASE WHEN %s<=%d%s THEN COALESCE((%s),FALSE) ELSE FALSE END", n, limit, valid, format)))
	}
}

func CheckExecution(ctx context.Context, client *ent.Client, id int) error {
	ok, err := client.Job.Query().Where(job.IDEQ(id), SupportedExecution(ExecutionDefinitionReadLimit)).Exist(ctx)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	exists, err := client.Job.Query().Where(job.IDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return &ent.NotFoundError{}
	}
	return execution.ErrUnsupportedExecutionFormat
}

// LoadExecutionConfig is called under the caller's finite definition lease.
// Its SQL predicate guards physical bytes before ORM decoding; the codec then
// checks nodes/depth before materializing a typed snapshot.
func LoadExecutionConfig(ctx context.Context, client *ent.Client, id int, limit int64, construct ...func(func() error) error) (map[string]any, error) {
	if limit <= 0 || limit > ExecutionDefinitionReadLimit {
		limit = ExecutionDefinitionReadLimit
	}
	if err := CheckExecution(ctx, client, id); err != nil {
		return nil, err
	}
	var rows []struct {
		Raw []byte `json:"raw"`
	}
	err := client.Job.Query().Where(job.IDEQ(id), SupportedExecution(limit)).Modify(func(s *sql.Selector) {
		c := s.C(job.FieldExecutionConfig)
		if s.Dialect() == dialect.Postgres {
			c = "CAST(" + c + " AS TEXT)"
		}
		s.Select(sql.As(c, "raw"))
	}).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBounds(0, max(int64(4096), 4*int64(len(rows[0].Raw))))
	if err != nil {
		return nil, err
	}
	var config map[string]any
	decode := func() error { return b.DecodeJSONLimited(rows[0].Raw, &config, limit) }
	if len(construct) > 0 && construct[0] != nil {
		err = construct[0](decode)
	} else {
		err = decode()
	}
	if err != nil {
		return nil, err
	}
	return config, nil
}

type executionDigestKey struct{}

func WithExecutionSnapshotDigest(ctx context.Context, digest string) context.Context {
	return context.WithValue(ctx, executionDigestKey{}, digest)
}
func executionDigest(ctx context.Context) string {
	v, _ := ctx.Value(executionDigestKey{}).(string)
	return v
}
