package worker

import (
	"context"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// prepareCapturedQualityMember runs under the build permit, using the document
// member whose version was checked against the request baseline.
func prepareCapturedQualityMember(b *backend.BuildBounds, mode string, baseline pipeline.Segment, result *pipeline.TranslatedSegment) error {
	issues := result.Issues
	switch mode {
	case pipeline.RoundModeSemanticQA:
		if issues == nil {
			return workstate.ErrManifest
		}
		reconciled, err := qa.ReconcileIssuesBounded(b, issues, baseline.Issues)
		if err != nil {
			return err
		}
		issues, err = mergeSemanticQAIssuesBounded(b, baseline.Issues, reconciled)
		if err != nil {
			return err
		}
	case pipeline.RoundModeAdjudicate:
	default:
		return workstate.ErrManifest
	}
	var err error
	result.IssuesPayload, err = b.EncodeJSON(issues, b.OutputLimit())
	return err
}

// applyCapturedQualityMember runs in the same transaction as the response
// cursor and confirmation. ContentVersion covers the target and every change
// to quality_issues, including decisions by users and other rounds.
func applyCapturedQualityMember(ctx context.Context, tx *ent.Client, scope workstate.Scope, mode string, member workstate.PromotionMember, result pipeline.TranslatedSegment) (bool, error) {
	if mode != pipeline.RoundModeSemanticQA && mode != pipeline.RoundModeAdjudicate ||
		member.SegmentID <= 0 || member.BaselineVersion <= 0 || len(result.IssuesPayload) == 0 {
		return false, workstate.ErrManifest
	}
	if member.BaselineStatus != string(segment.StatusTranslated) && member.BaselineStatus != string(segment.StatusEdited) {
		return false, workstate.ErrManifest
	}
	changed, err := tx.Segment.Update().Where(
		segment.IDEQ(member.SegmentID),
		segment.ResourceIDEQ(scope.ResourceID),
		segment.ContentVersionEQ(member.BaselineVersion),
		segment.StatusEQ(segment.Status(member.BaselineStatus)),
	).SetQualityIssues(nil).Modify(func(u *sql.UpdateBuilder) {
		u.Set(segment.FieldQualityIssues, sql.ExprFunc(func(b *sql.Builder) {
			b.WriteString("CAST(").Arg(result.IssuesPayload)
			if b.Dialect() == dialect.Postgres {
				b.WriteString(" AS JSONB)")
			} else {
				b.WriteString(" AS TEXT)")
			}
		}))
	}).Save(ctx)
	if err != nil || changed == 0 {
		return false, err
	}
	if err := service.AdvanceTranslationGeneration(ctx, tx, scope.ResourceID, scope.SourceGeneration); err != nil {
		return false, err
	}
	return true, nil
}
