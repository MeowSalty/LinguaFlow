package backend_test

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestDeferredPlanKeysBorrowCanonicalInventoryBacking(t *testing.T) {
	for _, name := range []string{"prepare", "grant_rollback", "rename"} {
		t.Run(name, func(t *testing.T) {
			a := budgetFixture(t, 16<<20)
			original := strings.Clone("member:stable-source-key")
			cached := strings.Clone(original)
			if unsafe.StringData(original) == unsafe.StringData(cached) {
				t.Fatal("test did not create distinct backing")
			}
			if err := a.RestoreBudgetEntry(original, backend.PipelineBudgetEntry{ResourceID: 1, RoundID: 1, Count: 1, Reservation: true, Durable: true}); err != nil {
				t.Fatal(err)
			}
			if name == "rename" {
				renamed := strings.Clone("member:renamed-key")
				if err := a.RenameMembers(original, renamed); err != nil {
					t.Fatal(err)
				}
				original = renamed
				cached = strings.Clone(renamed)
			}
			var ok bool
			var err error
			if name != "grant_rollback" {
				ok, err = a.PrepareMembers("new-request", 1, 1, 1, []string{cached})
			} else {
				ok, err = a.ReserveMembersReplacing("new-request", 1, 1, 1, false, []string{cached})
			}
			if !ok || err != nil {
				t.Fatal(ok, err)
			}
			entry := a.BudgetEntries()["new-request"]
			if len(entry.Replaced) != 1 || unsafe.StringData(entry.Replaced[0]) != unsafe.StringData(original) {
				t.Fatal("ledger retained transient plan key backing")
			}
			cached = ""
			a.ReleaseBudgetEntry("new-request")
			entries := a.BudgetEntries()
			if len(entries) != 1 {
				t.Fatal("rollback lost canonical member")
			}
			for key := range entries {
				if unsafe.StringData(key) != unsafe.StringData(original) {
					t.Fatal("rollback adopted transient backing")
				}
			}
			a.ReleaseBudgetEntry(original)
		})
	}
	t.Run("fresh", func(t *testing.T) {
		a := budgetFixture(t, 16<<20)
		if ok, err := a.PrepareMembers("fresh-request", 1, 1, 1, []string{strings.Clone("member:fresh")}); !ok || err != nil {
			t.Fatal(ok, err)
		}
		if len(a.BudgetEntries()["fresh-request"].Replaced) != 0 {
			t.Fatal("fresh equal key retained by ledger")
		}
		a.ReleaseBudgetEntry("fresh-request")
		if len(a.BudgetEntries()) != 0 {
			t.Fatal("fresh terminal leaked")
		}
	})
}
