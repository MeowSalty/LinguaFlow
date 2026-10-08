package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestReviewAndQAAdvanceTranslationGeneration(t *testing.T) {
	ctx := context.Background()
	_, client, actor, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "review-generation.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "source", "target", segment.StatusTranslated, []qa.QualityIssue{{Code: qa.CheckNumberMismatch, Severity: qa.SeverityWarning}})
	review := NewReviewService(client, NewProjectService(client, nil))
	check := func(want int64) {
		t.Helper()
		if got := client.Resource.GetX(ctx, res.ID).TranslationGeneration; got != want {
			t.Fatalf("generation=%d want=%d", got, want)
		}
	}
	if _, err := review.ApproveSegment(ctx, actor, projectID, res.ID, seg.ID, SegmentDecisionInput{}); err != nil {
		t.Fatal(err)
	}
	check(1)
	if _, err := review.ApproveSegment(ctx, actor, projectID, res.ID, seg.ID, SegmentDecisionInput{}); !errors.Is(err, ErrInvalidReviewState) {
		t.Fatal(err)
	}
	check(1)
	if _, err := review.SetIssueDisposition(ctx, actor, projectID, res.ID, seg.ID, qa.CheckNumberMismatch, "", "dismissed", "reviewed"); err != nil {
		t.Fatal(err)
	}
	check(2)
	client.Segment.UpdateOneID(seg.ID).SetStatus(segment.StatusTranslated).ExecX(ctx)
	if _, err := review.RejectSegment(ctx, actor, projectID, res.ID, seg.ID, SegmentDecisionInput{}); err != nil {
		t.Fatal(err)
	}
	check(3)
	if _, err := review.RetranslateRejected(ctx, actor, projectID, res.ID); err != nil {
		t.Fatal(err)
	}
	check(4)
	client.Segment.UpdateOneID(seg.ID).SetStatus(segment.StatusTranslated).ExecX(ctx)
	if _, err := review.BatchReview(ctx, actor, projectID, res.ID, BatchReviewInput{SegmentIDs: []int{seg.ID}, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	check(5)
	client.Segment.UpdateOneID(seg.ID).SetStatus(segment.StatusTranslated).ExecX(ctx)
	if _, err := review.ApproveAllResource(ctx, actor, projectID, res.ID); err != nil {
		t.Fatal(err)
	}
	check(6)

	loaded := client.Segment.GetX(ctx, seg.ID)
	recheck := newRecheckService(client)
	fresh := map[int][]qa.QualityIssue{loaded.SegmentIndex: {{Code: qa.CheckNumberMismatch, Severity: qa.SeverityWarning}}}
	if err := recheck.recheckWriteBatch(ctx, res, []*ent.Segment{loaded}, map[int]*string{loaded.ID: loaded.TargetText}, fresh, &qaRecheckCounters{}); err != nil {
		t.Fatal(err)
	}
	check(7)
	client.Resource.UpdateOneID(res.ID).SetSourceGeneration(1).ExecX(ctx)
	if err := recheck.recheckWriteBatch(ctx, res, []*ent.Segment{loaded}, map[int]*string{loaded.ID: loaded.TargetText}, nil, &qaRecheckCounters{}); !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("stale QA=%v", err)
	}
	check(7)
}
