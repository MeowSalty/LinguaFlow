package workstate

import (
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"strings"
	"testing"
)

func TestExecutionProjectionFixedBooleanDialectSQL(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		s := sql.Dialect(driver).Select("id").From(sql.Table("jobs"))
		SupportedExecution(4096)(s)
		query, _ := s.Query()
		if !strings.Contains(query, "CASE WHEN") || !strings.Contains(query, "<=4096") || !strings.Contains(query, "ELSE FALSE") {
			t.Fatalf("unbounded projection %s", query)
		}
		if driver == dialect.Postgres && (!strings.Contains(query, "octet_length") || strings.Contains(query, "->>")) {
			t.Fatalf("PG header projects body text: %s", query)
		}
		if driver == dialect.SQLite && !strings.Contains(query, "json_valid") {
			t.Fatalf("invalid JSON not guarded: %s", query)
		}
	}
}

func TestPromotionBaselineProjectionFixedChunks(t *testing.T) {
	ctx, c, _, _, seg := fixture(t)
	for _, value := range []*string{nil, new(string), func() *string { s := strings.Repeat("汉字🌳", 1000); return &s }()} {
		c.Segment.UpdateOneID(seg.ID).SetNillableTargetText(value).ExecX(ctx)
		member := PromotionMember{BaselineTargetIsNull: value == nil, BaselineDigest: stringDigest(value)}
		matches, err := promotionBaselineMatchesSQL(ctx, c, seg.ID, member)
		if err != nil || !matches {
			t.Fatalf("baseline null=%v: %v %v", value == nil, matches, err)
		}
		member.BaselineDigest = strings.Repeat("0", 64)
		matches, err = promotionBaselineMatchesSQL(ctx, c, seg.ID, member)
		if err != nil || matches {
			t.Fatalf("different digest: %v %v", matches, err)
		}
	}
	if _, err := c.ExecContext(ctx, "UPDATE segments SET target_text=CAST(zeroblob(67108865) AS TEXT) WHERE id=$1", seg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := promotionBaselineMatchesSQL(ctx, c, seg.ID, PromotionMember{}); err == nil {
		t.Fatal("oversized target read")
	}
	// Scalar identity/version projections remain usable beside the oversized target.
	if _, err := c.Segment.Query().Where(segment.IDEQ(seg.ID)).Select(segment.FieldID, segment.FieldContentVersion).Only(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionStageCountsNeverLoadHistoricalStageStrings(t *testing.T) {
	ctx, c, _, scope, _ := fixture(t)
	for i := 0; i < 257; i++ {
		c.WorkRequest.Create().SetIdentity(fmt.Sprintf("stage-%d", i)).SetRetryEpoch(0).SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetStage(fmt.Sprintf("audit-stage-%d", i)).SetSegmentIds([]int{}).SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("frozen").SetState("sent").SaveX(ctx)
	}
	if _, err := c.ExecContext(ctx, "UPDATE work_requests SET stage=$1 WHERE identity='stage-0'", strings.Repeat("x", 1<<20)); err != nil {
		t.Fatal(err)
	}
	counts, err := ReadStageCounts(ctx, c, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if counts.MainRequests != 257 || counts.AlignmentRequests != 0 {
		t.Fatalf("finite observation %+v", counts)
	}
}
