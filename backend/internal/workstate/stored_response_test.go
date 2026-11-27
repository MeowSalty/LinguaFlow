package workstate

import (
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

func TestStoredRequestRetainsAttemptAndUsageAfterUncapturedRecovery(t *testing.T) {
	for _, previousFormat := range []bool{true, false} {
		name := "frozen_without_response"
		if previousFormat {
			name = "released_without_archive"
		}
		t.Run(name, func(t *testing.T) {
			ctx, client, store, scope, segment := fixture(t)
			currentExecution(t, ctx, client, scope)
			req, _, _ := rawRequest(scope, segment)
			req.MemberMode = "semantic_qa"
			if previousFormat {
				// v0.17.5 reserved stored calls and recorded received usage
				// without writing the member or response archive.
				req.InputMembers = nil
			}
			if err := store.ReserveRequest(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordRequest(ctx, req.ID, RequestResult{State: "received", UsageKnown: true, InputTokens: 9}); err != nil {
				t.Fatal(err)
			}
			if err := Transaction(ctx, client, func(tx *ent.Client) error {
				if err := LockJob(ctx, tx, scope.JobID); err != nil {
					return err
				}
				return MarkUnknown(ctx, tx, scope.JobID)
			}); err != nil {
				t.Fatal(err)
			}
			row := client.WorkRequest.Query().OnlyX(ctx)
			member := client.WorkItem.Query().OnlyX(ctx)
			headers, _, err := store.LoadResponseHeaders(ctx, scope.JobID, 0, 16)
			if err != nil || len(headers) != 0 || row.State != "unknown" || row.InputTokens != 9 || row.InputMemberBytes != 0 || member.MainAttempts != 1 || member.ResponseRequestID != "" {
				t.Fatalf("unexpected recovery state, headers=%+v request=%+v member=%+v err=%v", headers, row, member, err)
			}
		})
	}
}

func TestStoredProviderFailureSavesRetryCauseAndRetiresFrozenInput(t *testing.T) {
	ctx, client, store, scope, segment := fixture(t)
	currentExecution(t, ctx, client, scope)
	req, _, _ := rawRequest(scope, segment)
	req.MemberMode = "adjudicate"
	if err := store.ReserveRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	retryAt := time.Now().Add(time.Minute).UTC().Truncate(time.Microsecond)
	const cause = "provider rate limit"
	result := RequestResult{State: "failed", LastError: cause, MainFailure: &model.RoundWorkCursor{Attempt: 1, State: "pending", NextAttemptAt: retryAt}}
	if err := store.RecordRequestForScope(ctx, scope, req.ID, result); err != nil {
		t.Fatal(err)
	}
	row := client.WorkRequest.Query().OnlyX(ctx)
	member := client.WorkItem.Query().OnlyX(ctx)
	input := client.WorkRequestInputMember.Query().OnlyX(ctx)
	if row.State != "failed" || row.LastError != cause || row.PromotionState != "cancelled" || row.InputMemberBytes != 0 || input.PayloadBytes != 0 || member.LastError != cause || member.NextAttemptAt == nil || !member.NextAttemptAt.Equal(retryAt) || member.MainAttempts != 1 {
		t.Fatalf("failed request lost its retry or retained input, request=%+v member=%+v input_bytes=%d", row, member, input.PayloadBytes)
	}
}
