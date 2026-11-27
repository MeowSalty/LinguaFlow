package worker

import (
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	pa "github.com/MeowSalty/LinguaFlow/backend/internal/persistenceacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestCapturedQualityWritesOnlyMatchingContentVersion(t *testing.T) {
	ctx := t.Context()
	fixture := pa.Seed(t, 1, false)
	accepted := qa.QualityIssue{Code: "calque", Message: "old scan", Note: "prior decision", Disposition: qa.DispositionDismissed}
	rule := qa.QualityIssue{Code: "source_residual", Message: "rule", Disposition: qa.DispositionPending}
	row := fixture.Client.Segment.UpdateOneID(fixture.IDs[0]).SetStatus(segment.StatusEdited).SetTargetText("existing translation").SetQualityIssues([]qa.QualityIssue{accepted, rule}).SaveX(ctx)
	member := workstate.PromotionMember{SegmentID: row.ID, BaselineVersion: row.ContentVersion, BaselineStatus: string(row.Status)}
	apply := func(mode string, baseline workstate.PromotionMember, result pipeline.TranslatedSegment) bool {
		t.Helper()
		var applied bool
		err := workstate.Transaction(ctx, fixture.Client, func(tx *ent.Client) error {
			var err error
			applied, err = applyCapturedQualityMember(ctx, tx, fixture.Scope, mode, baseline, result)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return applied
	}
	b, err := backend.NewBuildBounds(backend.DefaultMaxResponseBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	res := pipeline.TranslatedSegment{Issues: []qa.QualityIssue{{Code: "calque", Message: "fresh scan"}}}
	if err := prepareCapturedQualityMember(b, pipeline.RoundModeSemanticQA, pipeline.Segment{Issues: row.QualityIssues}, &res); err != nil {
		t.Fatal(err)
	}
	if !apply(pipeline.RoundModeSemanticQA, member, res) || apply(pipeline.RoundModeSemanticQA, member, res) {
		t.Fatal("semantic QA result must apply exactly once")
	}
	scanned := fixture.Client.Segment.GetX(ctx, row.ID)
	if scanned.ContentVersion != row.ContentVersion+1 || scanned.Status != row.Status || !reflect.DeepEqual(scanned.TargetText, row.TargetText) || len(scanned.QualityIssues) != 2 {
		t.Fatalf("unexpected semantic QA update: %+v", scanned)
	}
	if scanned.QualityIssues[0] != rule || scanned.QualityIssues[1].Message != "fresh scan" || scanned.QualityIssues[1].Note != accepted.Note || !scanned.QualityIssues[1].Dismissed() {
		t.Fatalf("semantic QA did not preserve rule and reconciled decision: %+v", scanned.QualityIssues)
	}
	member.BaselineVersion = scanned.ContentVersion
	dismissed := rule
	dismissed.Disposition, dismissed.Note = qa.DispositionDismissed, "old verdict"
	decision := pipeline.TranslatedSegment{Issues: []qa.QualityIssue{dismissed, scanned.QualityIssues[1]}}
	if err := prepareCapturedQualityMember(b, pipeline.RoundModeAdjudicate, pipeline.Segment{}, &decision); err != nil {
		t.Fatal(err)
	}
	newIssue := rule
	newIssue.Message = "issue raised after the request"
	updated := fixture.Client.Segment.UpdateOneID(row.ID).SetQualityIssues([]qa.QualityIssue{newIssue}).SaveX(ctx)
	if apply(pipeline.RoundModeAdjudicate, member, decision) {
		t.Fatal("old verdict applied to issue from a later version")
	}
	latest := fixture.Client.Segment.GetX(ctx, row.ID)
	if latest.ContentVersion != updated.ContentVersion || !reflect.DeepEqual(latest.QualityIssues, []qa.QualityIssue{newIssue}) {
		t.Fatalf("stale verdict changed newer issue: %+v", latest)
	}
	resource := fixture.Client.Resource.GetX(ctx, fixture.Scope.ResourceID)
	if resource.TranslationGeneration != 1 {
		t.Fatalf("replay or stale result advanced translation generation: %d", resource.TranslationGeneration)
	}
}
