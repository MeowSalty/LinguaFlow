package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

func retentionSettingsFixture(t *testing.T) (*ent.Client, *SettingsService, int) {
	t.Helper()
	client := testClient(t)
	if _, err := NewInitializationService(client).Initialize(context.Background(), config.ModeServer, bootstrapForTest("retention-admin", false)); err != nil {
		t.Fatal(err)
	}
	return client, NewSettingsService(client), client.User.Query().OnlyX(context.Background()).ID
}

func TestTaskRetentionStrictStoredPolicy(t *testing.T) {
	valid := `{"enabled":false,"retention_days":30,"revision":1}`
	if got, err := parseTaskRetention(valid); err != nil || got != defaultTaskRetention() {
		t.Fatalf("default: %+v %v", got, err)
	}
	for _, value := range []string{
		`null`, `{}`, `{"enabled":false,"retention_days":30}`, `{"enabled":null,"retention_days":30,"revision":1}`,
		`{"enabled":"false","retention_days":30,"revision":1}`, `{"enabled":false,"retention_days":"30","revision":1}`,
		`{"enabled":false,"retention_days":null,"revision":1}`, `{"enabled":false,"retention_days":0,"revision":1}`,
		`{"enabled":false,"retention_days":3651,"revision":1}`, `{"enabled":false,"retention_days":30,"revision":0}`,
		`{"enabled":false,"retention_days":30,"revision":9007199254740992}`, `{"enabled":false,"retention_days":30,"revision":1,"extra":true}`,
		valid + ` {}`, `{"enabled":false,"retention_days":1.5,"revision":1}`,
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseTaskRetention(value); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}

func TestTaskRetentionDomainsAndInitializationAreIndependent(t *testing.T) {
	ctx := context.Background()
	c, settings, _ := retentionSettingsFixture(t)
	policy := c.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).OnlyX(ctx)
	c.SystemSetting.UpdateOne(policy).SetValue("broken").ExecX(ctx)
	if value, err := settings.RegistrationEnabled(ctx); err != nil || value {
		t.Fatalf("registration depends on retention: %v %v", value, err)
	}
	if _, err := NewInitializationService(c).Validate(ctx, config.ModeServer); err != nil {
		t.Fatalf("retention corruption invalidated identity: %v", err)
	}
	if _, err := settings.TaskRetention(ctx); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("retention corruption: %v", err)
	}
	if _, err := settings.Get(ctx); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("admin read hid corruption: %v", err)
	}
	c.SystemSetting.UpdateOne(policy).SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).ExecX(ctx)
	c.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).SetValue("broken").ExecX(ctx)
	if value, err := settings.TaskRetention(ctx); err != nil || value != defaultTaskRetention() {
		t.Fatalf("retention depends on registration: %+v %v", value, err)
	}
	c.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).SetValue("false").ExecX(ctx)
	c.SystemSetting.Create().SetKey("unknown-policy").SetValue("true").SaveX(ctx)
	if _, err := settings.Get(ctx); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("unknown setting accepted: %v", err)
	}
	if _, err := settings.TaskRetention(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTaskRetentionPatchRevisionNoopAndWakeup(t *testing.T) {
	ctx := context.Background()
	c, settings, actor := retentionSettingsFixture(t)
	before := c.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).OnlyX(ctx)
	var wakes atomic.Int32
	settings.OnTaskRetentionChanged(func() {
		// Reading through the pool here would deadlock if invoked before commit.
		policy, err := settings.TaskRetention(ctx)
		if err != nil || policy.Revision < 2 {
			t.Errorf("callback ran before commit: %+v %v", policy, err)
		}
		wakes.Add(1)
	})
	if _, err := settings.Patch(ctx, actor, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: false, RetentionDays: 30, ExpectedRevision: 1}}); err != nil {
		t.Fatal(err)
	}
	unchanged := c.SystemSetting.GetX(ctx, before.ID)
	if !before.UpdatedAt.Equal(unchanged.UpdatedAt) || wakes.Load() != 0 || c.ActivityLog.Query().CountX(ctx) != 1 {
		t.Fatal("noop mutated policy, timestamp, audit, or wakeup")
	}
	registration := true
	result, err := settings.Patch(ctx, actor, SettingsPatch{RegistrationEnabled: &registration, TaskRetention: &TaskRetentionPatch{Enabled: true, RetentionDays: 7, ExpectedRevision: 1}})
	if err != nil || !result.RegistrationEnabled || result.TaskRetention != (TaskRetentionPolicy{Enabled: true, RetentionDays: 7, Revision: 2}) || wakes.Load() != 1 {
		t.Fatalf("patch: %+v %v wakes=%d", result, err, wakes.Load())
	}
	registration = false
	if _, err := settings.Patch(ctx, actor, SettingsPatch{RegistrationEnabled: &registration}); err != nil {
		t.Fatal(err)
	}
	policy, err := settings.TaskRetention(ctx)
	if err != nil || policy.Revision != 2 || wakes.Load() != 1 {
		t.Fatal("registration patch changed task policy")
	}
	if _, err := settings.Patch(ctx, actor, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: false, RetentionDays: 7, ExpectedRevision: 1}}); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if wakes.Load() != 1 {
		t.Fatal("conflict woke scanner")
	}
	audit := c.ActivityLog.Query().Where(activitylog.ActionEQ("admin.task_retention.update")).OnlyX(ctx)
	if len(audit.Metadata) != 6 || audit.Metadata["before_revision"] != float64(1) || audit.Metadata["after_revision"] != float64(2) || audit.VisibilityScope != activitylog.VisibilityScopeUnknown {
		t.Fatalf("audit=%+v", audit)
	}
}

func TestTaskRetentionPatchFailureIsAtomic(t *testing.T) {
	for _, fail := range []string{"audit", "commit", "revision"} {
		t.Run(fail, func(t *testing.T) {
			ctx := context.Background()
			c, settings, actor := retentionSettingsFixture(t)
			var wakes atomic.Int32
			settings.OnTaskRetentionChanged(func() { wakes.Add(1) })
			failure := errors.New("injected failure")
			if fail != "revision" {
				c.ActivityLog.Use(func(next ent.Mutator) ent.Mutator {
					return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
						if fail == "audit" {
							return nil, failure
						}
						tx, err := m.(*ent.ActivityLogMutation).Tx()
						if err != nil {
							return nil, err
						}
						tx.OnCommit(func(ent.Committer) ent.Committer {
							return ent.CommitFunc(func(context.Context, *ent.Tx) error { return failure })
						})
						return next.Mutate(ctx, m)
					})
				})
			}
			registration := true
			revision := int64(1)
			if fail == "revision" {
				revision = 2
				failure = ErrSettingsConflict
			}
			_, err := settings.Patch(ctx, actor, SettingsPatch{RegistrationEnabled: &registration, TaskRetention: &TaskRetentionPatch{Enabled: true, RetentionDays: 1, ExpectedRevision: revision}})
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			got, err := settings.Get(ctx)
			if err != nil || got.RegistrationEnabled || got.TaskRetention != defaultTaskRetention() || wakes.Load() != 0 || c.ActivityLog.Query().CountX(ctx) != 1 {
				t.Fatalf("partial patch: %+v %v", got, err)
			}
		})
	}
}

func TestTaskRetentionPatchValidationAndAuthority(t *testing.T) {
	ctx := context.Background()
	c, settings, actor := retentionSettingsFixture(t)
	for _, patch := range []SettingsPatch{{}, {TaskRetention: &TaskRetentionPatch{RetentionDays: 0, ExpectedRevision: 1}}, {TaskRetention: &TaskRetentionPatch{RetentionDays: 3651, ExpectedRevision: 1}}, {TaskRetention: &TaskRetentionPatch{RetentionDays: 30, ExpectedRevision: 0}}} {
		if _, err := settings.Patch(ctx, actor, patch); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("validation: %v", err)
		}
	}
	for _, state := range []string{"inactive", "user"} {
		role := SystemRoleAdmin
		if state == "user" {
			role = SystemRoleUser
		}
		c.User.UpdateOneID(actor).SetActive(state != "inactive").SetRole(role).ExecX(ctx)
		if _, err := settings.Patch(ctx, actor, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: true, RetentionDays: 1, ExpectedRevision: 1}}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s allowed: %v", state, err)
		}
	}
	c.User.UpdateOneID(actor).SetActive(true).SetRole(SystemRoleAdmin).ExecX(ctx)
	c.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingTaskRetention)).SetValue(fmt.Sprintf(`{"enabled":false,"retention_days":30,"revision":%d}`, MaxTaskRetentionRevision)).ExecX(ctx)
	if _, err := settings.Patch(ctx, actor, SettingsPatch{TaskRetention: &TaskRetentionPatch{Enabled: true, RetentionDays: 30, ExpectedRevision: MaxTaskRetentionRevision}}); !errors.Is(err, ErrSettingsConflict) {
		t.Fatalf("overflow: %v", err)
	}
}

func TestTaskRetentionAuditAllowlistAndSystemActor(t *testing.T) {
	ctx := context.Background()
	c, _, owner := retentionSettingsFixture(t)
	p := c.Project.Create().SetName("retained project").SetOwnerUserID(owner).SaveX(ctx)
	metadata := map[string]any{"task_kind": "translation", "task_id": 42, "project_id": p.ID, "previous_status": "failed", "source": "retention", "scan_id": "scan-1", "policy_revision": int64(2), "request_body": "secret"}
	for _, action := range []string{"job.history_deleted", "glossary.sync_task_history_deleted"} {
		if err := recordAuditEvent(ctx, c, AuditEvent{ProjectID: &p.ID, Action: action, ResourceType: "job", ResourceID: 42, Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
		row := c.ActivityLog.Query().Where(activitylog.ActionEQ(action)).OnlyX(ctx)
		if row.VisibilityScope != activitylog.VisibilityScopeProject || row.QueryActor().ExistX(ctx) || len(row.Metadata) != 7 {
			t.Fatalf("automatic audit=%+v", row)
		}
		if _, exists := row.Metadata["request_body"]; exists {
			t.Fatal("secret retained")
		}
		encoded, _ := json.Marshal(SanitizeActivityMetadata(action, metadata))
		var normalized map[string]any
		if err := json.Unmarshal(encoded, &normalized); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(SanitizeActivityMetadata(action, row.Metadata), normalized) {
			t.Fatal("write and response filters differ")
		}
	}
}
