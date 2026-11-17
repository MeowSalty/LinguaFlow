package workstate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestAllocationPromotionMembersBorrowerLifetime(t *testing.T) {
	scope := Scope{JobID: 1, ResourceID: 2, RoundID: 3}
	revision := 7
	payload, err := json.Marshal([]PromotionMember{{SegmentID: 4, WorkID: WorkIdentity(scope, 4), CandidateID: "candidate", BaselineVersion: 1, BaselineDigest: strings.Repeat("a", 64), BaselineStatus: "pending", SourceRevisionID: &revision}})
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"held", "invalid_member_count", "invalid_json"} {
		t.Run(boundary, func(t *testing.T) {
			live := int64(0)
			ctx := backend.WithBuildAllocation(context.Background(), 0, func(n int64) error { live += n; return nil }, func(n int64) { live -= n })
			row := &ent.WorkRequest{JobID: scope.JobID, ResourceID: scope.ResourceID, JobRoundID: scope.RoundID, SegmentIds: []int{4}, PromotionMembers: payload, PromotionCount: 1, PromotionStatus: []byte{0}}
			if boundary == "invalid_member_count" {
				row.PromotionCount = 2
			}
			if boundary == "invalid_json" {
				row.PromotionMembers = []byte("[")
			}
			members, release, err := decodePromotionMembers(ctx, row)
			if boundary != "held" {
				if !errors.Is(err, ErrManifest) || members != nil || release != nil || live != 0 {
					t.Fatalf("failed decode retained backing: err=%v live=%d", err, live)
				}
				return
			}
			if err != nil || release == nil || live <= 0 || len(members) != 1 || members[0].SourceRevisionID == nil || *members[0].SourceRevisionID != revision {
				t.Fatalf("returned member lost its actual backing: err=%v live=%d", err, live)
			}
			members = nil
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
