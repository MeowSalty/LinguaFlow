package backend_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestPreparedMembersAbortRestoresExactInventory(t *testing.T) {
	for _, existing := range []int{0, 1, 2} {
		for _, grant := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing%d/grant%t", existing, grant), func(t *testing.T) {
				a := contractAdmission(t, 8, 1, 64<<20)
				keys := []string{"old-a", "old-b"}
				for _, key := range keys[:existing] {
					if err := a.RestoreBudgetEntry(key, backend.PipelineBudgetEntry{ResourceID: 1, RoundID: 2, Count: 1, Bytes: 7, Growth: 3, Reservation: true, Durable: true, GrowthDurable: true, Progress: existing == 1}); err != nil {
						t.Fatal(err)
					}
				}
				before := a.BudgetEntries()
				if ok, err := a.PrepareMembers("request", 2, 1, 2, keys); !ok || err != nil {
					t.Fatalf("prepare: %t %v", ok, err)
				}
				for key, value := range before {
					if !reflect.DeepEqual(a.BudgetEntries()[key], value) {
						t.Fatalf("preparation modified %s", key)
					}
				}
				if grant {
					if ok, err := a.PrepareMain("request", 0, 1, 0, 0, 0, false, 2); !ok || err != nil {
						t.Fatalf("prepare main: %t %v", ok, err)
					}
					permit, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "request", ResourceID: 1, RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
					if permit == nil || err != nil {
						t.Fatalf("grant: wait=%+v err=%v", wait, err)
					}
					permit.Release()
				}
				a.ReleasePrepared("request")
				a.ReleaseBudgetEntry("request")
				if got := a.BudgetEntries(); !reflect.DeepEqual(got, before) {
					t.Fatalf("abort changed original inventory: got=%+v want=%+v", got, before)
				}
				if got := a.PipelineSnapshot(); got.Segments != existing || got.CandidateBytes != int64(existing)*7 || got.GrowthBytes != int64(existing)*3 {
					t.Fatalf("abort changed occupancy: %+v", got)
				}
			})
		}
	}
}

func TestPreparedMembersRejectsAllInvalidReplacementKeys(t *testing.T) {
	for _, keys := range [][]string{{"", "missing"}, {"missing", "missing"}, {"foreign", "missing"}} {
		a := contractAdmission(t, 8, 1, 64<<20)
		if err := a.RestoreBudgetEntry("foreign", backend.PipelineBudgetEntry{ResourceID: 9, RoundID: 2, Count: 1, Reservation: true, Durable: true}); err != nil {
			t.Fatal(err)
		}
		before := a.BudgetEntries()
		if ok, err := a.PrepareMembers("request", 2, 1, 2, keys); ok || !errors.Is(err, backend.ErrPipelineCapacity) {
			t.Fatalf("keys=%v accepted=%t err=%v", keys, ok, err)
		}
		a.ReleaseBudgetEntry("request")
		if !reflect.DeepEqual(a.BudgetEntries(), before) {
			t.Fatalf("invalid keys=%v changed inventory", keys)
		}
	}
}

func TestPreparedMembersDurableRenameDropsAbortSnapshots(t *testing.T) {
	for _, rename := range []bool{false, true} {
		a := contractAdmission(t, 8, 1, 64<<20)
		old := backend.PipelineBudgetEntry{ResourceID: 1, RoundID: 2, Count: 1, Reservation: true, Durable: true}
		if err := a.RestoreBudgetEntry("old", old); err != nil {
			t.Fatal(err)
		}
		if ok, err := a.ReserveMembersReplacing("request", 1, 1, 2, false, []string{"old"}); !ok || err != nil {
			t.Fatalf("reserve: %t %v", ok, err)
		}
		to := "request"
		if rename {
			to = "durable"
		}
		if err := a.RenameMembers("request", to); err != nil {
			t.Fatal(err)
		}
		want := old
		if got := a.BudgetEntries()[to]; !reflect.DeepEqual(got, want) {
			t.Fatalf("durable rename retained abort snapshot: got=%+v want=%+v", got, want)
		}
		if ok, err := a.ReserveMembersReplacing("retry", 1, 1, 2, false, []string{to}); !ok || err != nil {
			t.Fatalf("second replacement: %t %v", ok, err)
		}
		a.ReleaseBudgetEntry("retry")
		if got := a.BudgetEntries()[to]; !reflect.DeepEqual(got, want) {
			t.Fatalf("second abort retained snapshot history: got=%+v want=%+v", got, want)
		}
	}
}

func TestPreparedMembersRestoreRejectsUnbackedReplacement(t *testing.T) {
	a := contractAdmission(t, 8, 1, 64<<20)
	err := a.RestoreBudgetEntry("forged", backend.PipelineBudgetEntry{Count: 1, Reservation: true, Replaced: []string{"missing"}})
	if !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatalf("unbacked replacement restored: %v", err)
	}
}

func TestPreparedMembersAnonymousHintWaitsForFullOwner(t *testing.T) {
	a := contractAdmission(t, 8, 4, 64<<20)
	if ok, err := a.PrepareMembers("full", 8, 1, 2, nil); !ok || err != nil {
		t.Fatalf("prepare: %t %v", ok, err)
	}
	if ok, err := a.PrepareMain("full", 0, 1, 0, 0, 0, false, 8); !ok || err != nil {
		t.Fatalf("prepare main: %t %v", ok, err)
	}
	permit, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "full", ResourceID: 1, RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatalf("grant: %+v %v", wait, err)
	}
	before := a.BudgetEntries()
	stock := a.PipelineSnapshot()
	intent := backend.RequestAdmissionIntent{ResourceID: 2, RoundIndex: 0, Stage: backend.RequestStageMain}
	for range 3 {
		hint, err := a.ReadyHint(context.Background(), intent, nil)
		if err != nil || hint.Reason != "full_owner" {
			t.Fatalf("anonymous hint: %+v %v", hint, err)
		}
		if got := a.BudgetEntries(); !reflect.DeepEqual(got, before) {
			t.Fatal("anonymous hint reserved or changed inventory")
		}
		if got := a.PipelineSnapshot(); !reflect.DeepEqual(got, stock) {
			t.Fatal("anonymous hint consumed budget")
		}
	}
	if hint, err := a.ReadyHint(context.Background(), backend.RequestAdmissionIntent{ResourceID: 1, RoundIndex: 0, Stage: backend.RequestStageAlignment}, nil); err != nil || hint.Reason != "" {
		t.Fatalf("full owner blocked auxiliary hint: %+v %v", hint, err)
	}
	permit.Release()
	a.ReleaseBudgetEntry("full")
	if hint, err := a.ReadyHint(context.Background(), intent, nil); err != nil || hint.Reason != "" {
		t.Fatalf("released full owner blocked producer: %+v %v", hint, err)
	}
}
