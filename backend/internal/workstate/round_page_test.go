package workstate

import (
	"errors"
	"os"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func TestPersistenceRoundMemberPageBounds(t *testing.T) {
	driver := dialect.SQLite
	if os.Getenv("LINGUAFLOW_PERSISTENCE_DRIVER") == dialect.Postgres {
		driver = dialect.Postgres
	}
	t.Run(driver, func(t *testing.T) {
		client, _ := independentClients(t, driver)
		ctx, client, _, scope, first := seedFixture(t, t.Context(), client)
		ids := []int{first.ID}
		for i := 1; i < 17; i++ {
			row := client.Segment.Create().SetResourceID(scope.ResourceID).SetSegmentIndex(i).SetSourceText("source").SaveX(ctx)
			client.WorkItem.Create().SetJobID(scope.JobID).SetResourceID(scope.ResourceID).SetJobRoundID(scope.RoundID).SetSegmentID(row.ID).SaveX(ctx)
			ids = append(ids, row.ID)
		}
		// pending (7 bytes) + forty three-byte characters + x = 128 bytes.
		client.WorkItem.Update().SetPromptPhase(strings.Repeat("界", 40) + "x").ExecX(ctx)
		client.WorkItem.Update().Where(workitem.SegmentIDEQ(ids[16])).SetPromptPhase(strings.Repeat("界", 41)).ExecX(ctx)
		store := NewStore(client.Debug())
		page, after, err := store.LoadRoundMembersPage(ctx, scope, 0, 16)
		if err != nil || len(page) != 16 || after != ids[15] {
			t.Fatalf("oversized later page changed current page: members=%d after=%d err=%v", len(page), after, err)
		}
		for i, member := range page {
			if member.SegmentID != ids[i] || len(member.Cursor.State)+len(member.Cursor.PromptPhase) != 128 {
				t.Fatalf("page order/UTF-8 byte boundary: %+v", member)
			}
		}
		if _, _, err := store.LoadRoundMembersPage(ctx, scope, after, 16); !errors.Is(err, ErrManifest) {
			t.Fatalf("oversized next page was not rejected: %v", err)
		}
		client.WorkItem.Update().Where(workitem.SegmentIDEQ(ids[16])).SetPromptPhase("initial").SetRetryEpoch(1).ExecX(ctx)
		if _, _, err := store.LoadRoundMembersPage(ctx, scope, after, 16); !errors.Is(err, ErrStopped) {
			t.Fatalf("retry epoch fence lost: %v", err)
		}
		client.WorkItem.Update().Where(workitem.SegmentIDEQ(ids[16])).SetRetryEpoch(0).ExecX(ctx)
		page, after, err = store.LoadRoundMembersPage(ctx, scope, after, 16)
		if err != nil || len(page) != 1 || after != ids[16] {
			t.Fatalf("last page=%+v after=%d err=%v", page, after, err)
		}
		page, next, err := store.LoadRoundMembersPage(ctx, scope, after, 16)
		if err != nil || len(page) != 0 || next != after {
			t.Fatalf("terminal page=%+v after=%d err=%v", page, next, err)
		}
		for _, other := range []Scope{{JobID: scope.JobID + 1, ResourceID: scope.ResourceID, RoundID: scope.RoundID}, {JobID: scope.JobID, ResourceID: scope.ResourceID + 1, RoundID: scope.RoundID}, {JobID: scope.JobID, ResourceID: scope.ResourceID, RoundID: scope.RoundID + 1}} {
			page, _, err := store.LoadRoundMembersPage(ctx, other, 0, 16)
			if err != nil || len(page) != 0 {
				t.Fatalf("scope leaked: scope=%+v page=%+v err=%v", other, page, err)
			}
		}
		for _, limit := range []int{0, 17} {
			if _, _, err := store.LoadRoundMembersPage(ctx, scope, 0, limit); !errors.Is(err, ErrManifest) {
				t.Fatalf("invalid page limit %d accepted: %v", limit, err)
			}
		}
	})
}
