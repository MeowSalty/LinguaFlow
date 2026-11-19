package workstate

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"testing"
)

func TestAllocationPromotionMembersBorrowerLifetime(t *testing.T) {
	for _, boundary := range []string{"held", "invalid_member_count", "invalid_json"} {
		t.Run(boundary, func(t *testing.T) {
			base, client, store, scope, seg := fixture(t)
			currentExecution(t, base, client, scope)
			r, _, _ := rawRequest(scope, seg)
			if err := store.ReserveRequest(base, r); err != nil {
				t.Fatal(err)
			}
			row := client.WorkRequestInputMember.Query().OnlyX(base)
			revision := 7
			client.WorkRequestInputMember.UpdateOneID(row.ID).SetSourceRevisionID(revision).ExecX(base)
			if boundary == "invalid_member_count" {
				client.WorkRequest.UpdateOneID(client.WorkRequest.Query().OnlyX(base).ID).SetPromotionCount(0).ExecX(base)
			}
			if boundary == "invalid_json" {
				client.WorkRequestInputMember.UpdateOneID(row.ID).SetPayload([]byte("[")).ExecX(base)
			}
			live := int64(0)
			ctx := backend.WithBuildAllocation(base, 0, func(n int64) error { live += n; return nil }, func(n int64) { live -= n })
			member, err := store.LoadInputMember(ctx, requestMemberKey(r, 0), 1<<20)
			if boundary != "held" {
				if !errors.Is(err, ErrManifest) || member.Release != nil || len(member.Payload) != 0 || live != 0 {
					t.Fatalf("failed member read retained backing: err=%v live=%d", err, live)
				}
				return
			}
			if err != nil || member.Release == nil || live <= 0 || len(member.Payload) == 0 || member.SourceRevisionID == nil || *member.SourceRevisionID != revision {
				t.Fatalf("returned member lost backing/identity: err=%v live=%d", err, live)
			}
			release := member.Release
			member = InputMember{}
			release()
			if live != 0 {
				t.Fatalf("last borrower left live=%d", live)
			}
			release()
			if live != 0 {
				t.Fatalf("repeated release changed live=%d", live)
			}
		})
	}
}
