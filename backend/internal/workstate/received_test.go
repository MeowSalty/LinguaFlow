package workstate

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestReceivedRequestRetainsUsageAndBecomesUnknownOnRecovery(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	req := Request{ID: "saving", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", MainAttempt: true}
	if err := s.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRequest(ctx, req.ID, RequestResult{State: "received", DurationMS: 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRequest(ctx, req.ID, RequestResult{State: "received", UsageKnown: true, InputTokens: 11, OutputTokens: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRequest(ctx, req.ID, RequestResult{State: "sent"}); err != nil {
		t.Fatal(err)
	}
	row := c.WorkRequest.Query().OnlyX(ctx)
	if row.State != "received" || !row.UsageKnown || row.InputTokens != 11 || row.DurationMs != 30 {
		t.Fatalf("received request=%+v", row)
	}
	if err := Transaction(ctx, c, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, scope.JobID); err != nil {
			return err
		}
		return MarkUnknown(ctx, tx, scope.JobID)
	}); err != nil {
		t.Fatal(err)
	}
	row = c.WorkRequest.Query().OnlyX(ctx)
	if row.State != "unknown" || row.InputTokens != 11 || c.WorkItem.Query().OnlyX(ctx).MainAttempts != 1 {
		t.Fatal("restart lost charged request or kept phantom saving state")
	}
	if err := s.RecordRequest(ctx, req.ID, RequestResult{State: "completed", UsageKnown: true, InputTokens: 11, OutputTokens: 7}); err != nil {
		t.Fatal(err)
	}
	usage := c.UsageRecord.Query().AllX(ctx)
	if len(usage) != 1 || usage[0].APICalls != 1 || usage[0].InputTokens != 11 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestReceivedAlignmentIsEvidenceForSavedProgress(t *testing.T) {
	ctx, c, s, scope, seg := fixture(t)
	candidate := ready(scope, seg)
	candidate.State = "pending_alignment"
	if err := s.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "aligned", CandidateID: candidate.ID, Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "alignment", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "request", AlignmentAttempt: true}
	if err := s.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRequest(ctx, req.ID, RequestResult{State: "received"}); err != nil {
		t.Fatal(err)
	}
	cursor, err := s.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	cursor.PromptPhase = "alignment_complete"
	cursor.NetworkAttempts = 0
	cursor.AlignmentNetworkAttempts = 0
	if err := s.SaveCursor(ctx, scope, seg.ID, cursor); err != nil {
		t.Fatal(err)
	}
	if row := c.WorkItem.Query().OnlyX(ctx); row.AlignmentAttempts != 1 || row.AlignmentNetworkAttempts != 0 {
		t.Fatalf("saved progress=%+v", row)
	}
}
