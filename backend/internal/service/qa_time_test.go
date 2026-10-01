package service

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segmentrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestEqualIssuesComparesDecisionInstants(t *testing.T) {
	utc := time.Date(2026, 9, 29, 0, 0, 0, 123456789, time.UTC)
	east := utc.In(time.FixedZone("east", 8*60*60))
	original := qa.QualityIssue{Code: qa.IssueCodeCalque, Message: "decision", Severity: qa.SeverityWarning, Disposition: qa.DispositionDismissed, DecidedAt: &utc}
	same := original
	same.DecidedAt = &east
	if !equalIssues([]qa.QualityIssue{original}, []qa.QualityIssue{same}) {
		t.Fatal("equivalent decision timestamps diverged")
	}
	if original.DecidedAt != &utc || same.DecidedAt != &east {
		t.Fatal("comparison mutated inputs")
	}
	for _, tc := range []struct {
		name   string
		change func(*qa.QualityIssue)
	}{
		{"later instant", func(i *qa.QualityIssue) { later := utc.Add(time.Nanosecond); i.DecidedAt = &later }},
		{"missing instant", func(i *qa.QualityIssue) { i.DecidedAt = nil }},
		{"changed note", func(i *qa.QualityIssue) { i.Note = "edited" }},
		{"changed disposition", func(i *qa.QualityIssue) { i.Disposition = qa.DispositionPending }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := same
			tc.change(&changed)
			if equalIssues([]qa.QualityIssue{original}, []qa.QualityIssue{changed}) {
				t.Fatal("genuine decision edit was ignored")
			}
		})
	}
}

func TestUndoSearchReplacePreservesDecisionInstant(t *testing.T) {
	svc, client, ctx, actor, project, resource := searchReplaceSetup(t, "colour")
	row, err := client.Segment.Query().Where(segment.ResourceIDEQ(resource.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 9, 29, 10, 0, 0, 123456789, time.FixedZone("west", -7*60*60))
	issue := qa.QualityIssue{Code: qa.IssueCodeCalque, Message: "decision", Severity: qa.SeverityWarning, Disposition: qa.DispositionDismissed, DecidedAt: &instant}
	if err := client.Segment.UpdateOne(row).SetQualityIssues([]qa.QualityIssue{issue}).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	apply, err := svc.ApplySearchReplace(ctx, actor.ID, project.ID, resource.ID, SearchReplaceOptions{Find: "colour", ReplaceWith: "color"})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := client.SegmentRevision.Query().Where(segmentrevision.OperationIDEQ(apply.OperationID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(revision.BeforeIssues) != 1 || revision.BeforeIssues[0].DecidedAt.Location() != time.UTC || !revision.BeforeIssues[0].DecidedAt.Equal(instant) {
		t.Fatalf("before snapshot changed decision: %+v", revision.BeforeIssues)
	}
	undo, err := svc.UndoSearchReplace(ctx, actor.ID, project.ID, resource.ID, apply.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if undo.UndoneCount != 1 || len(undo.Items[0].QualityIssues) != 1 {
		t.Fatalf("undo=%+v", undo)
	}
	decision := undo.Items[0].QualityIssues[0].DecidedAt
	if decision == nil || !decision.Equal(instant) || decision.Location() != time.UTC {
		t.Fatalf("restored decision=%v", decision)
	}
}

func TestApplyPreviewPreservesDecisionInstant(t *testing.T) {
	svc, client, actorID, projectID := newApplyFixture(t)
	resource := createTestResource(t, client, projectID, "decision-preview.txt")
	row := seedRevisionSegment(t, client, resource.ID, 0, "source", "old", segment.StatusTranslated, nil)
	instant := time.Date(2026, 9, 29, 10, 0, 0, 123456789, time.FixedZone("east", 9*60*60))
	issue := qa.QualityIssue{Code: qa.IssueCodeCalque, Message: "decision", Severity: qa.SeverityWarning, Disposition: qa.DispositionDismissed, DecidedAt: &instant}
	claims := revisionApplyClaims(row, resource.ID, projectID, actorID, nil, previewtoken.QAConfigClaims{Enabled: false})
	claims.FinalIssues = []qa.QualityIssue{issue}
	token := encodeApplyClaims(t, claims)
	applied, err := svc.ApplyPreview(context.Background(), actorID, projectID, resource.ID, row.ID, token, "新译文")
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.QualityIssues) != 1 {
		t.Fatalf("issues=%+v", applied.QualityIssues)
	}
	decision := applied.QualityIssues[0].DecidedAt
	if decision == nil || !decision.Equal(instant) || decision.Location() != time.UTC {
		t.Fatalf("applied decision=%v", decision)
	}
}
