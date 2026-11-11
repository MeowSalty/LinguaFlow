package workstate

import (
	"context"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

func projectedByteLength(s *sql.Selector, column string) string {
	if s.Dialect() == dialect.Postgres {
		if !storageBinaryColumn(column) {
			return fmt.Sprintf("COALESCE(OCTET_LENGTH(CAST(%s AS TEXT)),0)", s.C(column))
		}
		return fmt.Sprintf("COALESCE(OCTET_LENGTH(%s),0)", s.C(column))
	}
	return fmt.Sprintf("COALESCE(LENGTH(CAST(%s AS BLOB)),0)", s.C(column))
}

func loadRequestMetadata(ctx context.Context, client *ent.Client, id string) (int64, error) {
	var values []struct {
		Bytes int64 `json:"bytes"`
	}
	err := client.WorkRequest.Query().Where(workrequest.IdentityEQ(id)).Aggregate(func(sel *sql.Selector) string {
		var lengths []string
		for _, field := range requestMetadataFields {
			if field != workrequest.FieldGlossaryReceipt {
				lengths = append(lengths, projectedByteLength(sel, field))
			}
		}
		return joinLengths(lengths) + " AS bytes"
	}).Scan(ctx, &values)
	if err != nil {
		return 0, err
	}
	if len(values) != 1 || values[0].Bytes < 0 {
		return 0, ErrManifest
	}
	return values[0].Bytes, nil
}

func projectedLengthPredicate(sel *sql.Selector, field, comparison string, limit int64) *sql.Predicate {
	return sql.P(func(b *sql.Builder) {
		b.WriteString(projectedByteLength(sel, field) + " " + comparison + " ").Arg(limit)
	})
}

var requestMetadataFields = []string{workrequest.FieldPromotionMembers, workrequest.FieldPromotionStatus, workrequest.FieldGlossaryReceipt, workrequest.FieldMembers, workrequest.FieldDebits, workrequest.FieldSegmentIds, workrequest.FieldCorrectedTargetIndex}

// Member tables belong to the finite frozen-request slots, separately from
// the fixed receipt O. Apply their count/actual-byte bounds before SQL returns
// any JSON/BLOB cells, including corrupt historical declarations.
func requestMetadataBounds(sel *sql.Selector) *sql.Predicate {
	ids := sel.C(workrequest.FieldSegmentIds)
	count := "COALESCE(json_array_length(" + ids + "),0)"
	if sel.Dialect() == dialect.Postgres {
		count = "COALESCE(jsonb_array_length(" + ids + "),0)"
	}
	promotionCount := sel.C(workrequest.FieldPromotionCount)
	checks := []*sql.Predicate{sql.GTE(promotionCount, 0), sql.LTE(promotionCount, 65536), sql.ExprP(count + " BETWEEN 1 AND 65536"), sql.ExprP(promotionCount + " <= " + count)}
	for _, limit := range []struct{ field, expression string }{{workrequest.FieldPromotionMembers, "4+" + promotionCount + "*4096"}, {workrequest.FieldPromotionStatus, promotionCount}, {workrequest.FieldCorrectedTargetIndex, promotionCount + "*16"}, {workrequest.FieldMembers, "32+" + count + "*2048"}, {workrequest.FieldDebits, "4+" + count + "*1024"}, {workrequest.FieldSegmentIds, "4+" + count + "*21"}} {
		checks = append(checks, sql.ExprP(projectedByteLength(sel, limit.field)+" <= "+limit.expression), projectedLengthPredicate(sel, limit.field, "<=", CandidateReadBytes))
	}
	checks = append(checks, projectedLengthPredicate(sel, workrequest.FieldGlossaryReceipt, "<=", responseReceiptReadBytes))
	return sql.And(checks...)
}
