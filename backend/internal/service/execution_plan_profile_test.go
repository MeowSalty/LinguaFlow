package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

// validTranslateRound 返回一个能通过 validateExecutionRounds 的最小 translate 轮配置。
func validTranslateRound(backendID int) schema.ExecutionRoundConfig {
	return schema.ExecutionRoundConfig{
		Mode:      "translate",
		BackendID: backendID,
		Translate: &schema.TranslateRoundConfig{
			PromptTemplateID: -1,
			BatchSize:        10,
			Concurrency:      1,
			FallbackShrink:   1.0,
		},
	}
}

// TestValidatePlanProfileID 计划级 profile_id 格式校验：非零；负数必须可解析为
// 内置策略（当前仅 -1，任意其他负数是落库后无法建任务的哑弹引用）。
func TestValidatePlanProfileID(t *testing.T) {
	cases := []struct {
		name      string
		profileID int
		wantErr   string // 空 = 合法
	}{
		{"内置默认策略 -1 合法", -1, ""},
		{"非内置负数拒绝", -99, "is not a valid builtin template"},
		{"零值非法", 0, "profile_id must not be zero"},
		{"正数格式合法（存在性属主校验另见 ref 校验）", 5, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePlanProfileID(c.profileID)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) || !errors.Is(err, ErrExecutionPlanConfigInvalid) {
				t.Fatalf("err=%v want substring %q", err, c.wantErr)
			}
		})
	}
}

func TestExecutionPlanCreate_ProfileIDValidation(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user, err := client.User.Create().
		SetUsername("plan-profile-user").
		SetPasswordHash("hash").
		SetEmail("plan-profile@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	plans := NewExecutionPlanService(client, users, NewExecutionProfileService(client, users))

	ownProfile, err := client.ExecutionProfile.Create().
		SetName("own-profile").
		SetScope("user").
		SetOwnerUserID(user.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create own profile: %v", err)
	}
	otherUser, err := client.User.Create().
		SetUsername("plan-profile-other").
		SetPasswordHash("hash").
		SetEmail("plan-profile-other@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherProfile, err := client.ExecutionProfile.Create().
		SetName("other-profile").
		SetScope("user").
		SetOwnerUserID(otherUser.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create other profile: %v", err)
	}

	input := CreateExecutionPlanTemplateInput{
		Name:        "p",
		Scope:       ScopeUser,
		OwnerUserID: &user.ID,
		Rounds:      []schema.ExecutionRoundConfig{validTranslateRound(1)},
	}

	t.Run("profile_id 缺省为零值时拒绝", func(t *testing.T) {
		if _, err := plans.Create(ctx, user.ID, input); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("非内置负数拒绝且不落库", func(t *testing.T) {
		in := input
		in.ProfileID = -99
		if _, err := plans.Create(ctx, user.ID, in); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("引用不存在的正数策略时拒绝", func(t *testing.T) {
		in := input
		in.ProfileID = 9999
		if _, err := plans.Create(ctx, user.ID, in); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("引用他人策略时拒绝且不泄露存在性", func(t *testing.T) {
		in := input
		in.ProfileID = otherProfile.ID
		_, err := plans.Create(ctx, user.ID, in)
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
		if strings.Contains(err.Error(), otherProfile.Name) {
			t.Fatalf("错误信息不应泄露他人策略名称: %v", err)
		}
	})

	t.Run("内置 -1 落库", func(t *testing.T) {
		in := input
		in.ProfileID = templates.BuiltinExecutionProfileID
		plan, err := plans.Create(ctx, user.ID, in)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if plan.ProfileID != templates.BuiltinExecutionProfileID {
			t.Fatalf("plan.ProfileID=%d want %d", plan.ProfileID, templates.BuiltinExecutionProfileID)
		}
	})

	t.Run("引用本人策略时落库", func(t *testing.T) {
		in := input
		in.ProfileID = ownProfile.ID
		plan, err := plans.Create(ctx, user.ID, in)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if plan.ProfileID != ownProfile.ID {
			t.Fatalf("plan.ProfileID=%d want %d", plan.ProfileID, ownProfile.ID)
		}
	})
}

func TestExecutionPlanUpdate_ProfileID(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	user, err := client.User.Create().
		SetUsername("plan-update-user").
		SetPasswordHash("hash").
		SetEmail("plan-update@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	plans := NewExecutionPlanService(client, users, NewExecutionProfileService(client, users))

	profileRow, err := client.ExecutionProfile.Create().
		SetName("custom-profile").
		SetScope("user").
		SetOwnerUserID(user.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	plan, err := plans.Create(ctx, user.ID, CreateExecutionPlanTemplateInput{
		Name:        "p",
		Scope:       ScopeUser,
		OwnerUserID: &user.ID,
		ProfileID:   templates.BuiltinExecutionProfileID,
		Rounds:      []schema.ExecutionRoundConfig{validTranslateRound(1)},
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}

	otherUser, err := client.User.Create().
		SetUsername("plan-update-other").
		SetPasswordHash("hash").
		SetEmail("plan-update-other@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherProfile, err := client.ExecutionProfile.Create().
		SetName("other-update-profile").
		SetScope("user").
		SetOwnerUserID(otherUser.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create other profile: %v", err)
	}

	t.Run("nil 表示保留现值", func(t *testing.T) {
		updated, err := plans.Update(ctx, user.ID, plan.ID, UpdateExecutionPlanTemplateInput{
			Name: ptrString("renamed"),
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if updated.ProfileID != templates.BuiltinExecutionProfileID {
			t.Fatalf("ProfileID=%d want preserved %d", updated.ProfileID, templates.BuiltinExecutionProfileID)
		}
	})

	t.Run("显式更新 profile_id", func(t *testing.T) {
		pid := profileRow.ID
		updated, err := plans.Update(ctx, user.ID, plan.ID, UpdateExecutionPlanTemplateInput{
			ProfileID: &pid,
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if updated.ProfileID != profileRow.ID {
			t.Fatalf("ProfileID=%d want %d", updated.ProfileID, profileRow.ID)
		}
	})

	t.Run("更新为不存在的策略时拒绝", func(t *testing.T) {
		missing := 9999
		if _, err := plans.Update(ctx, user.ID, plan.ID, UpdateExecutionPlanTemplateInput{
			ProfileID: &missing,
		}); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("更新为他人策略时拒绝", func(t *testing.T) {
		pid := otherProfile.ID
		if _, err := plans.Update(ctx, user.ID, plan.ID, UpdateExecutionPlanTemplateInput{
			ProfileID: &pid,
		}); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("更新为零值拒绝且不落库", func(t *testing.T) {
		zero := 0
		if _, err := plans.Update(ctx, user.ID, plan.ID, UpdateExecutionPlanTemplateInput{
			ProfileID: &zero,
		}); !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
		row, err := plans.GetByIDRaw(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.ProfileID != profileRow.ID {
			t.Fatalf("被拒绝的更新不应落库: ProfileID=%d", row.ProfileID)
		}
	})
}

// TestValidateAndSnapshotWith_MaterializesPlanLevelStrategy 快照顶层 Strategy 物化：
// 计划引用的 ExecutionProfile（含 builtin -1 路径）在轮次循环前物化一次。
func TestValidateAndSnapshotWith_MaterializesPlanLevelStrategy(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	user, err := client.User.Create().
		SetUsername("snapshot-strategy-user").
		SetPasswordHash("hash").
		SetEmail("snapshot-strategy@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	backends := NewBackendService(client, users, nil)
	backendRow, err := client.Backend.Create().
		SetName("strategy-backend").
		SetBackendType(entbackend.BackendType("openai")).
		SetScope("user").
		SetOwnerUserID(user.ID).
		SetOptions(map[string]any{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	jobs := NewJobService(client, nil, nil, backends, NewTranslationPromptTemplateService(client), NewBootstrapPromptTemplateService(client), NewExecutionProfileService(client, users), nil, nil)

	newPlan := func(profileID int, rounds []schema.ExecutionRoundConfig) *ent.ExecutionPlanTemplate {
		return &ent.ExecutionPlanTemplate{
			ID:        1,
			Name:      "strategy-plan",
			ProfileID: profileID,
			Rounds:    rounds,
		}
	}
	check := func(int) error { return nil }

	t.Run("builtin -1 路径物化内置策略", func(t *testing.T) {
		snap, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(templates.BuiltinExecutionProfileID, []schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)}), "", check)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		builtin := templates.BuiltinExecutionProfile(templates.BuiltinExecutionProfileID)
		if snap.Strategy.ProfileName != builtin.Name {
			t.Fatalf("ProfileName=%q want builtin %q", snap.Strategy.ProfileName, builtin.Name)
		}
		if snap.Strategy.ProfileID == nil || *snap.Strategy.ProfileID != templates.BuiltinExecutionProfileID {
			t.Fatalf("ProfileID=%v want %d", snap.Strategy.ProfileID, templates.BuiltinExecutionProfileID)
		}
	})

	t.Run("数据库策略配置透传到顶层", func(t *testing.T) {
		cfg := schema.DefaultProfileConfig()
		cfg.Protect.Enabled = true
		cfg.Protect.Rules = []string{"code"}
		cfg.Ruby.Enabled = true
		cfg.Ruby.PreserveKinds = []string{"phonetic"}
		profileRow, err := client.ExecutionProfile.Create().
			SetName("db-profile").
			SetScope("user").
			SetOwnerUserID(user.ID).
			SetConfig(cfg).
			Save(ctx)
		if err != nil {
			t.Fatalf("create profile: %v", err)
		}
		snap, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(profileRow.ID, []schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)}), "", check)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !snap.Strategy.Ruby.Enabled || len(snap.Strategy.Ruby.PreserveKinds) != 1 {
			t.Fatalf("Ruby 配置未透传: %+v", snap.Strategy.Ruby)
		}
		if len(snap.Strategy.Protect.Rules) != 1 || snap.Strategy.Protect.Rules[0] != "code" {
			t.Fatalf("Protect 配置未透传: %+v", snap.Strategy.Protect)
		}
	})

	t.Run("计划引用不存在的策略时报错", func(t *testing.T) {
		_, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(9999, []schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)}), "", check)
		if !errors.Is(err, ErrExecutionProfileNotFound) || !strings.Contains(err.Error(), "snapshot strategy profile") {
			t.Fatalf("err=%v want wrapped ErrExecutionProfileNotFound", err)
		}
	})

	t.Run("计划引用他人策略时快照物化拒绝", func(t *testing.T) {
		otherUser, err := client.User.Create().
			SetUsername("snapshot-other-user").
			SetPasswordHash("hash").
			SetEmail("snapshot-other@test.com").
			Save(ctx)
		if err != nil {
			t.Fatalf("create other user: %v", err)
		}
		otherProfile, err := client.ExecutionProfile.Create().
			SetName("other-snapshot-profile").
			SetScope("user").
			SetOwnerUserID(otherUser.ID).
			Save(ctx)
		if err != nil {
			t.Fatalf("create other profile: %v", err)
		}
		// 快照物化前复核访问权：即便计划（绕过服务校验直接构造）引用了他人
		// 策略，任务创建也在物化处按不存在拒绝。
		if _, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(otherProfile.ID, []schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)}), "", check); !errors.Is(err, ErrExecutionProfileNotFound) {
			t.Fatalf("err=%v want ErrExecutionProfileNotFound", err)
		}
	})
}

// TestExecutionProfileDelete_ReferencedByPlanRejected 删除防护：被执行计划模板
// 引用的执行策略不可删除；无引用时可删除。错误不携带计划名（不跨租户泄露），
// 非属主调用者按不存在处理。
func TestExecutionProfileDelete_ReferencedByPlanRejected(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	user, err := client.User.Create().
		SetUsername("profile-delete-user").
		SetPasswordHash("hash").
		SetEmail("profile-delete@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	otherUser, err := client.User.Create().
		SetUsername("profile-delete-other").
		SetPasswordHash("hash").
		SetEmail("profile-delete-other@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	profiles := NewExecutionProfileService(client, users)

	referenced, err := client.ExecutionProfile.Create().
		SetName("referenced-profile").
		SetScope("user").
		SetOwnerUserID(user.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create referenced profile: %v", err)
	}
	unreferenced, err := client.ExecutionProfile.Create().
		SetName("free-profile").
		SetScope("user").
		SetOwnerUserID(user.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create unreferenced profile: %v", err)
	}
	if _, err := client.ExecutionPlanTemplate.Create().
		SetName("ref-plan").
		SetScope("user").
		SetOwnerUserID(user.ID).
		SetProfileID(referenced.ID).
		SetRounds([]schema.ExecutionRoundConfig{}).
		Save(ctx); err != nil {
		t.Fatalf("create plan: %v", err)
	}

	t.Run("被计划引用时拒绝删除且不泄露计划名", func(t *testing.T) {
		err := profiles.Delete(ctx, user.ID, referenced.ID)
		if !errors.Is(err, ErrExecutionProfileInUse) {
			t.Fatalf("err=%v want ErrExecutionProfileInUse", err)
		}
		if strings.Contains(err.Error(), "ref-plan") {
			t.Fatalf("错误信息不应包含计划名: %v", err)
		}
		// 拒绝后策略仍存在
		if _, err := client.ExecutionProfile.Get(ctx, referenced.ID); err != nil {
			t.Fatalf("被引用策略不应被删除: %v", err)
		}
	})

	t.Run("非属主调用者按不存在处理", func(t *testing.T) {
		if err := profiles.Delete(ctx, otherUser.ID, unreferenced.ID); !errors.Is(err, ErrExecutionProfileNotFound) {
			t.Fatalf("err=%v want ErrExecutionProfileNotFound", err)
		}
		// 策略未被他人删除
		if _, err := client.ExecutionProfile.Get(ctx, unreferenced.ID); err != nil {
			t.Fatalf("他人删除不应生效: %v", err)
		}
	})

	t.Run("属主未被引用时可删除", func(t *testing.T) {
		if err := profiles.Delete(ctx, user.ID, unreferenced.ID); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("org 策略非成员按不存在处理、成员可删除", func(t *testing.T) {
		orgRow, err := client.Organization.Create().
			SetName("profile-delete-org").
			SetSlug("profile-delete-org").
			Save(ctx)
		if err != nil {
			t.Fatalf("create org: %v", err)
		}
		orgProfile, err := client.ExecutionProfile.Create().
			SetName("org-delete-profile").
			SetScope("org").
			SetOwnerOrgID(orgRow.ID).
			Save(ctx)
		if err != nil {
			t.Fatalf("create org profile: %v", err)
		}
		// 非成员（user）删除 org 策略：按不存在处理，不泄露存在性
		if err := profiles.Delete(ctx, user.ID, orgProfile.ID); !errors.Is(err, ErrExecutionProfileNotFound) {
			t.Fatalf("err=%v want ErrExecutionProfileNotFound", err)
		}
		if _, err := client.OrgMembership.Create().
			SetUserID(otherUser.ID).
			SetOrganizationID(orgRow.ID).
			SetRole("member").
			Save(ctx); err != nil {
			t.Fatalf("create membership: %v", err)
		}
		// 组织 member 及以上成员可删除未被引用的 org 策略
		if err := profiles.Delete(ctx, otherUser.ID, orgProfile.ID); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

func ptrString(s string) *string { return &s }
