package workstate

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

func rawRequest(scope Scope, seg *ent.Segment) (Request, ResponseHandoff, Candidate) {
	digest, _ := ExecutionSnapshotDigest(map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 1})
	m := PromotionMember{SegmentID: seg.ID, WorkID: WorkIdentity(scope, seg.ID), CandidateID: "stable-raw-member", BaselineVersion: seg.ContentVersion, BaselineTarget: seg.TargetText, BaselineStatus: string(seg.Status), SourceGeneration: scope.SourceGeneration, SourceRevisionID: scope.SourceRevisionID}
	r := Request{ID: "raw-request", Scope: scope, SegmentIDs: []int{seg.ID}, Stage: "main", BackendID: 1, BudgetModel: "stage_separated", InputDigest: "input", MainAttempt: true, MaxMainAttempts: 3, FreezePayload: []byte("frozen-input"), SnapshotDigest: digest, PromotionMembers: []PromotionMember{m}}
	h := ResponseHandoff{RequestID: r.ID, Scope: scope, FreezePayload: r.FreezePayload, ResponsePayload: []byte("complete response"), SnapshotDigest: r.SnapshotDigest, Members: r.PromotionMembers, Result: RequestResult{UsageKnown: true, InputTokens: 12, OutputTokens: 8}}
	c := Candidate{ID: m.CandidateID, WorkID: m.WorkID, ParentRequestID: r.ID, Version: 1, Scope: scope, SegmentID: seg.ID, DTOVersion: 2, Mode: "translate", SnapshotDigest: r.SnapshotDigest, BaselineVersion: m.BaselineVersion, BaselineTarget: m.BaselineTarget, BaselineStatus: m.BaselineStatus, State: "ready_to_commit", Payload: []byte(`{"dto_version":2,"target":"translated"}`)}
	return r, h, c
}

func currentExecution(t *testing.T, ctx context.Context, client *ent.Client, scope Scope) {
	t.Helper()
	client.Job.UpdateOneID(scope.JobID).SetExecutionConfig(map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 1}).ExecX(ctx)
}

func TestRawHandoffAndPromotionLostAcknowledgements(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "raw.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var lose atomic.Bool
	client := ent.NewClient(ent.Driver(uncertainDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), lose: &lose}))
	t.Cleanup(func() { client.Close(); db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, seg := seedFixture(t, ctx, client)
	currentExecution(t, ctx, client, scope)
	r, h, c := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	lose.Store(true)
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
	w := client.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx)
	if row.State != "completed" || row.ResponseBytes != int64(len(h.ResponsePayload)) || w.State != "response_pending" || w.MainAttempts != 1 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("raw handoff lost payload or repeated accounting")
	}
	if counts, err := ReadStageCounts(ctx, client, scope.JobID); err != nil || counts.ConfirmedWork != 0 || counts.SavingRequests != 0 {
		t.Fatalf("raw confirmed early: %+v %v", counts, err)
	}
	cur := cursorFromRow(w)
	p := MemberPromotion{Scope: scope, RequestID: r.ID, Cursor: 0, Candidate: &c, Next: cur}
	lose.Store(true)
	if err := store.PromoteMember(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMember(ctx, p); err != nil {
		t.Fatal(err)
	}
	row = client.WorkRequest.GetX(ctx, row.ID)
	if row.PromotionCursor != 1 || row.PromotionState != "promoted" || len(row.ResponsePayload) != 0 || len(row.FreezePayload) != 0 || client.WorkCandidate.Query().CountX(ctx) != 1 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("promotion replay changed durable result")
	}
}

func TestReliableRawSurvivesRetryEpochAndPause(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	r, h, c := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	w := client.WorkItem.Query().Where(workitem.SegmentIDEQ(seg.ID)).OnlyX(ctx)
	if ok, err := HasPendingResponse(ctx, client, w); err != nil || !ok {
		t.Fatalf("retry lost raw: %v %v", ok, err)
	}
	client.Job.UpdateOneID(scope.JobID).SetRetryEpoch(1).SetPauseRequested(true).SetStatus("pausing").ExecX(ctx)
	client.WorkItem.UpdateOneID(w.ID).SetRetryEpoch(1).SetMainAttempts(0).SetNetworkAttempts(0).SetMainNetworkAttempts(0).ExecX(ctx)
	scope.RetryEpoch = 1
	c.Scope = scope
	headers, _, err := store.LoadResponseHeaders(ctx, scope.JobID, 0, 16)
	if err != nil || len(headers) != 1 || headers[0].Scope.RetryEpoch != 0 {
		t.Fatalf("cross epoch header omitted: %+v %v", headers, err)
	}
	if err := store.PromoteMember(ctx, MemberPromotion{Scope: scope, RequestID: r.ID, Cursor: 0, Candidate: &c, Next: Cursor{State: "candidate"}}); err != nil {
		t.Fatal(err)
	}
	if row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx); row.RetryEpoch != 0 {
		t.Fatal("raw identity epoch was rewritten")
	}
	if client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("retry raw recounted usage")
	}
}

func TestPromotionFailureKeepsRawUntilExplicitFailure(t *testing.T) {
	ctx, client, store, scope, seg := fixture(t)
	currentExecution(t, ctx, client, scope)
	r, h, c := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	c.BaselineVersion++
	cur, err := store.LoadCursor(ctx, scope, seg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMember(ctx, MemberPromotion{Scope: scope, RequestID: r.ID, Cursor: 0, Candidate: &c, Next: cur}); err == nil {
		t.Fatal("changed baseline accepted")
	}
	row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
	if row.PromotionCursor != 0 || len(row.ResponsePayload) == 0 || client.WorkCandidate.Query().CountX(ctx) != 0 {
		t.Fatal("failed promotion was not atomic")
	}
	cur.State = "rejected"
	if err := store.PromoteMember(ctx, MemberPromotion{Scope: scope, RequestID: r.ID, Cursor: 0, Next: cur, Failure: "capacity"}); err != nil {
		t.Fatal(err)
	}
	if row := client.WorkRequest.GetX(ctx, row.ID); row.PromotionStatus[0] != 2 || len(row.ResponsePayload) != 0 {
		t.Fatal("explicit failure did not retire raw")
	}
}
