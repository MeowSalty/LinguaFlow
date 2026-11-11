package workstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
)

// Retry checks a current target under the same transaction in fixed 4 KiB
// byte chunks. No target/source JSON or unbounded string is loaded, and byte
// hashing preserves the existing null versus empty acceptance distinction.
func promotionBaselineMatchesSQL(ctx context.Context, client *ent.Client, id int, m PromotionMember) (bool, error) {
	q := client.Segment.Query().Where(segment.IDEQ(id))
	var header []struct {
		Bytes int64 `json:"bytes"`
		Null  bool  `json:"null"`
	}
	err := q.Clone().Modify(func(s *sql.Selector) {
		c := s.C(segment.FieldTargetText)
		body := "CAST(" + c + " AS BLOB)"
		if s.Dialect() == dialect.Postgres {
			body = "convert_to(" + c + ",'UTF8')"
		}
		s.Select(sql.As("COALESCE(length("+body+"),0)", "bytes"), sql.As(c+" IS NULL", "null"))
	}).Scan(ctx, &header)
	if err != nil {
		return false, err
	}
	if len(header) != 1 || header[0].Bytes < 0 || header[0].Bytes > ExecutionDefinitionReadLimit {
		return false, ErrManifest
	}
	if header[0].Null != m.BaselineTargetIsNull {
		return false, nil
	}
	h := sha256.New()
	for offset := int64(0); offset < header[0].Bytes; offset += 4096 {
		var chunks []struct {
			Raw []byte `json:"raw"`
		}
		err = q.Clone().Modify(func(s *sql.Selector) {
			c := s.C(segment.FieldTargetText)
			body := "CAST(" + c + " AS BLOB)"
			expr := fmt.Sprintf("substr(%s,%d,4096)", body, offset+1)
			if s.Dialect() == dialect.Postgres {
				expr = fmt.Sprintf("substring(convert_to(%s,'UTF8') FROM %d FOR 4096)", c, offset+1)
			}
			s.Select(sql.As(expr, "raw"))
		}).Scan(ctx, &chunks)
		if err != nil {
			return false, err
		}
		if len(chunks) != 1 || len(chunks[0].Raw) > 4096 {
			return false, ErrManifest
		}
		_, _ = h.Write(chunks[0].Raw)
	}
	return hex.EncodeToString(h.Sum(nil)) == m.BaselineDigest, nil
}
