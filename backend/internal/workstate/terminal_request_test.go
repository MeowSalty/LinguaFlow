package workstate

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

func TestTerminalMainFailurePersistsMemberWithRequest(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, stage := range []string{"main", MainPromptUpgradeStage} {
			t.Run(driver+"/"+stage, func(t *testing.T) {
				ctx := context.Background()
				client, recovered := independentClients(t, driver)
				_, _, store, scope, seg := seedFixture(t, ctx, client)
				currentExecution(t, ctx, client, scope)
				r, _, _ := rawRequest(scope, seg)
				if err := store.ReserveRequest(ctx, r); err != nil {
					t.Fatal(err)
				}
				if item := recovered.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).OnlyX(ctx); item.PoolIndex != 0 || item.MainAttempts != 1 || !item.MemberOwned {
					t.Fatalf("reserve did not debit the actual member: %+v", item)
				}
				if err := store.RecordRequestForScope(ctx, scope, r.ID, RequestResult{State: "sent"}); err != nil {
					t.Fatal(err)
				}
				if stage == MainPromptUpgradeStage {
					if err := store.RecordRequestForScope(ctx, scope, r.ID, RequestResult{State: "completed"}); err != nil {
						t.Fatal(err)
					}
					cursor, err := store.LoadCursor(ctx, scope, seg.ID)
					if err != nil {
						t.Fatal(err)
					}
					cursor.PromptPhase = responsecontract.PromptUpgradeCursorPrefix + "test-parse-failure"
					if err := store.SaveCursors(ctx, scope, []CursorTransition{{SegmentID: seg.ID, Cursor: cursor}}); err != nil {
						t.Fatal(err)
					}
					r.ID, r.Stage, r.MainAttempt, r.MaxNetworkAttempts = "upgrade-request", MainPromptUpgradeStage, false, 2
					r.InputMembers[0].Key.RequestID = r.ID
					if err := store.ReserveRequest(ctx, r); err != nil {
						t.Fatal(err)
					}
					if item := recovered.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).OnlyX(ctx); item.MainAttempts != 1 || item.MainNetworkAttempts != 2 {
						t.Fatalf("prompt upgrade did not use the same logical attempt: %+v", item)
					}
					if err := store.RecordRequestForScope(ctx, scope, r.ID, RequestResult{State: "sent"}); err != nil {
						t.Fatal(err)
					}
				}
				const cause = "provider access rejected"
				if err := store.RecordRequestForScope(ctx, scope, r.ID, RequestResult{State: "failed", LastError: cause, MainFailure: &model.RoundWorkCursor{Pool: 3, State: "unresolved"}}); err != nil {
					t.Fatal(err)
				}
				// Observe from another connection before the dispatcher can save any
				// follow-up cursor event. The failed request already closes its member.
				request := recovered.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
				item := recovered.WorkItem.Query().Where(workitem.JobRoundIDEQ(scope.RoundID)).OnlyX(ctx)
				if request.State != "failed" || request.LastError != cause || request.UsageRecordID == nil || item.LastError != cause || item.State != "unresolved" || item.PoolIndex != 3 || item.MemberOwned {
					t.Fatalf("independent recovery observed a partial failure: request=%+v member=%+v", request, item)
				}
			})
		}
	}
}
