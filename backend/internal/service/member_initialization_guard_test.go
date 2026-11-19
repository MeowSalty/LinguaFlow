package service

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestMemberResponseInitializationRejectsOrphanTables(t *testing.T) {
	for _, table := range []string{"input", "output"} {
		t.Run(table, func(t *testing.T) {
			ctx := t.Context()
			c := testClient(t)
			if _, err := c.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
				t.Fatal(err)
			}
			if table == "input" {
				_, err := c.WorkRequestInputMember.Create().SetRequestID(999).SetOrdinal(0).SetPromptID("1").SetSegmentID(0).SetSegmentIndex(0).SetWorkID("orphan").SetCandidateID("orphan").SetBaselineVersion(0).SetBaselineDigest("").SetBaselineTargetIsNull(true).SetBaselineStatus("pending").SetSourceGeneration(0).SetPayload([]byte("input")).SetPayloadBytes(5).SetPayloadDigest("orphan").Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := c.WorkResponseMember.Create().SetRequestID(999).SetOrdinal(0).SetPayload([]byte("output")).SetPayloadBytes(6).SetPayloadDigest("orphan").Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			svc := NewInitializationService(c)
			if empty, err := svc.IsEmpty(ctx); err != nil || empty {
				t.Fatalf("orphan %s ignored: empty=%v err=%v", table, empty, err)
			}
			if _, err := svc.Initialize(ctx, config.ModeLocal, config.BootstrapInput{}); !errors.Is(err, ErrInstanceIncomplete) {
				t.Fatalf("orphan initialization accepted: %v", err)
			}
			if c.InstanceInitialization.Query().CountX(ctx) != 0 || c.User.Query().CountX(ctx) != 0 || c.SystemSetting.Query().CountX(ctx) != 0 {
				t.Fatal("incomplete inventory was claimed by initialization")
			}
		})
	}
}
