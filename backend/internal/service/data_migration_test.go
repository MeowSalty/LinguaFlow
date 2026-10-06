package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

func TestTaskRetentionDataMigrationIdempotentAndPreservesPolicy(t *testing.T) {
	ctx := context.Background()
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "existing"}[existing], func(t *testing.T) {
			c, settings, _ := retentionSettingsFixture(t)
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(0).ExecX(ctx)
			c.SystemSetting.Delete().Where(systemsetting.KeyEQ(SettingTaskRetention)).ExecX(ctx)
			want := defaultTaskRetention()
			if existing {
				c.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue(`{"enabled":true,"retention_days":90,"revision":8}`).SaveX(ctx)
				want = TaskRetentionPolicy{Enabled: true, RetentionDays: 90, Revision: 8}
			}
			if err := ValidateDataVersion(ctx, c); !errors.Is(err, ErrDataMigrationRequired) {
				t.Fatalf("upgrade not required: %v", err)
			}
			if err := MigrateData(ctx, c); err != nil {
				t.Fatal(err)
			}
			row := c.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).OnlyX(ctx)
			if err := MigrateData(ctx, c); err != nil {
				t.Fatal(err)
			}
			if got, err := settings.TaskRetention(ctx); err != nil || got != want {
				t.Fatalf("policy %+v %v", got, err)
			}
			if after := c.SystemSetting.GetX(ctx, row.ID); !after.UpdatedAt.Equal(row.UpdatedAt) {
				t.Fatal("repeated migration rewrote policy")
			}
			if marker := c.InstanceInitialization.GetX(ctx, 1); marker.DataVersion != CurrentDataVersion {
				t.Fatal("version did not advance")
			}
			if c.User.Query().CountX(ctx) != 1 || c.ActivityLog.Query().CountX(ctx) != 1 {
				t.Fatal("migration changed identity or added synthetic history")
			}
		})
	}
}

func TestTaskRetentionMigrationNeverRepairsCurrentCorruption(t *testing.T) {
	ctx := context.Background()
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "corrupt", true: "missing"}[missing], func(t *testing.T) {
			c, settings, _ := retentionSettingsFixture(t)
			if missing {
				c.SystemSetting.Delete().Where(systemsetting.KeyEQ(SettingTaskRetention)).ExecX(ctx)
			} else {
				c.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingTaskRetention)).SetValue("broken").ExecX(ctx)
			}
			if err := MigrateData(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDataVersion(ctx, c); err != nil {
				t.Fatal(err)
			}
			if _, err := NewInitializationService(c).Validate(ctx, config.ModeServer); err != nil {
				t.Fatalf("identity invalidated: %v", err)
			}
			if _, err := settings.TaskRetention(ctx); !errors.Is(err, ErrSettingsUnavailable) {
				t.Fatalf("corruption silently repaired: %v", err)
			}
		})
	}
}

func TestTaskRetentionDataMigrationRollbackAndEmptyInstance(t *testing.T) {
	ctx := context.Background()
	empty := testClient(t)
	if err := MigrateData(ctx, empty); err != nil {
		t.Fatal(err)
	}
	if has, err := NewInitializationService(empty).IsEmpty(ctx); err != nil || !has {
		t.Fatal("migration initialized an empty instance")
	}
	for _, fail := range []string{"policy", "marker", "invalid-policy"} {
		t.Run(fail, func(t *testing.T) {
			c, _, _ := retentionSettingsFixture(t)
			c.InstanceInitialization.UpdateOneID(1).SetDataVersion(0).ExecX(ctx)
			c.SystemSetting.Delete().Where(systemsetting.KeyEQ(SettingTaskRetention)).ExecX(ctx)
			failure := errors.New("injected migration failure")
			if fail == "invalid-policy" {
				c.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue("broken").SaveX(ctx)
				failure = ErrSettingsUnavailable
			} else {
				c.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
						if fail == "policy" && mutation.Type() == "SystemSetting" || fail == "marker" && mutation.Type() == "InstanceInitialization" {
							return nil, failure
						}
						return next.Mutate(ctx, mutation)
					})
				})
			}
			if err := MigrateData(ctx, c); !errors.Is(err, failure) {
				t.Fatalf("failure=%v", err)
			}
			if c.InstanceInitialization.GetX(ctx, 1).DataVersion != 0 {
				t.Fatal("failed migration advanced version")
			}
			if fail != "invalid-policy" && c.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).ExistX(ctx) {
				t.Fatal("failed migration left policy")
			}
		})
	}
}

func TestTaskRetentionNewInitializationDataVersion(t *testing.T) {
	for _, mode := range []string{config.ModeServer, config.ModeLocal} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			c := testClient(t)
			input := bootstrapForTest("admin", false)
			if mode == config.ModeLocal {
				input.Admin = nil
			}
			if _, err := NewInitializationService(c).Initialize(ctx, mode, input); err != nil {
				t.Fatal(err)
			}
			if err := ValidateDataVersion(ctx, c); err != nil {
				t.Fatal(err)
			}
			if policy, err := NewSettingsService(c).TaskRetention(ctx); err != nil || policy != defaultTaskRetention() {
				t.Fatalf("new policy: %+v %v", policy, err)
			}
		})
	}
}
