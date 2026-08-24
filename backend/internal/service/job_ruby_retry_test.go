package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

func TestNormalizeRubyRetryAttempts(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 1},
		{-1, 1},
		{1, 1},
		{5, 5},
	}
	for _, c := range cases {
		if got := NormalizeRubyRetryAttempts(c.in); got != c.want {
			t.Errorf("NormalizeRubyRetryAttempts(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestExecutionPlanRubyRetrySnapshotJSON(t *testing.T) {
	t.Run("max_attempts emitted", func(t *testing.T) {
		// 与 validateAndSnapshotWith 物化一致：仅 enabled 且 backend 可达时写入，max_attempts 规范化。
		rr := schema.ExecutionPlanRubyRetryConfig{Enabled: true, BackendID: 1, MaxAttempts: 3}
		snap := ExecutionPlanRubyRetrySnapshot{
			Enabled:     true,
			Backend:     BackendSnapshot{ID: 1},
			MaxAttempts: NormalizeRubyRetryAttempts(rr.MaxAttempts),
		}
		b, err := json.Marshal(snap)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(b), `"max_attempts":3`) {
			t.Errorf("snapshot json = %s, want \"max_attempts\":3", b)
		}
	})

	t.Run("max_attempts omitted when zero", func(t *testing.T) {
		b, err := json.Marshal(ExecutionPlanRubyRetrySnapshot{Enabled: true})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(b), "max_attempts") {
			t.Errorf("snapshot json = %s, want max_attempts omitted (omitempty)", b)
		}
	})

	t.Run("normalization wired to materialization values", func(t *testing.T) {
		rrs := []schema.ExecutionPlanRubyRetryConfig{
			{MaxAttempts: 0},
			{MaxAttempts: -2},
			{MaxAttempts: 1},
			{MaxAttempts: 7},
		}
		wants := []int{1, 1, 1, 7}
		for i, rr := range rrs {
			got := NormalizeRubyRetryAttempts(rr.MaxAttempts)
			if got != wants[i] {
				t.Errorf("case %d: NormalizeRubyRetryAttempts(%d) = %d, want %d", i, rr.MaxAttempts, got, wants[i])
			}
		}
	})
}

// TestRubyRetrySnapshot_BackendIDZeroFallsBackToTranslateBackend ruby_retry 后端回退：
// enabled 且 backend_id=0 时（文档承诺“使用翻译主后端”），从快照内第一条 translate 轮
// 的 Backend 快照回填 RubyRetry；无 translate 轮可借用时维持留 nil。
func TestRubyRetrySnapshot_BackendIDZeroFallsBackToTranslateBackend(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	user, err := client.User.Create().
		SetUsername("ruby-retry-user").
		SetPasswordHash("hash").
		SetEmail("ruby-retry@test.com").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	backends := NewBackendService(client, users, nil)
	backendRow, err := client.Backend.Create().
		SetName("main-translate-backend").
		SetBackendType(entbackend.BackendType("openai")).
		SetScope("user").
		SetOwnerUserID(user.ID).
		SetOptions(map[string]any{}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	jobs := NewJobService(client, nil, nil, backends, NewTranslationPromptTemplateService(client), NewBootstrapPromptTemplateService(client), NewExecutionProfileService(client, users), nil, nil)

	newPlan := func(rr schema.ExecutionPlanRubyRetryConfig, rounds []schema.ExecutionRoundConfig) *ent.ExecutionPlanTemplate {
		return &ent.ExecutionPlanTemplate{
			ID:        1,
			Name:      "ruby-retry-plan",
			ProfileID: templates.BuiltinExecutionProfileID,
			RubyRetry: rr,
			Rounds:    rounds,
		}
	}
	check := func(int) error { return nil }

	t.Run("有 translate 轮时回填其 Backend", func(t *testing.T) {
		snap, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(
			schema.ExecutionPlanRubyRetryConfig{Enabled: true, BackendID: 0},
			[]schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)},
		), "", check)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if snap.RubyRetry == nil {
			t.Fatal("backend_id=0 且存在 translate 轮时应回填 RubyRetry，实际为 nil")
		}
		if !snap.RubyRetry.Enabled {
			t.Fatal("RubyRetry.Enabled 应为 true")
		}
		if snap.RubyRetry.Backend.ID != backendRow.ID || snap.RubyRetry.Backend.Name != backendRow.Name {
			t.Fatalf("回填的 Backend=%+v want translate 主后端 id=%d", snap.RubyRetry.Backend, backendRow.ID)
		}
		if snap.RubyRetry.MaxAttempts != 1 {
			t.Fatalf("MaxAttempts=%d want 1（省略时规范化）", snap.RubyRetry.MaxAttempts)
		}
	})

	t.Run("无 translate 轮时维持留 nil", func(t *testing.T) {
		snap, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(
			schema.ExecutionPlanRubyRetryConfig{Enabled: true, BackendID: 0},
			[]schema.ExecutionRoundConfig{{
				Mode:      "extract",
				BackendID: backendRow.ID,
				Extract: &schema.ExtractRoundConfig{
					BootstrapTemplateID: templates.BuiltinBootstrapPromptTemplateID,
					BatchSize:           10,
					Concurrency:         1,
				},
			}},
		), "", check)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if snap.RubyRetry != nil {
			t.Fatalf("无 translate 轮可借用时 RubyRetry 应为 nil，实际 %+v", snap.RubyRetry)
		}
	})

	t.Run("未启用时始终留 nil", func(t *testing.T) {
		snap, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(
			schema.ExecutionPlanRubyRetryConfig{},
			[]schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)},
		), "", check)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if snap.RubyRetry != nil {
			t.Fatalf("未启用时 RubyRetry 应为 nil，实际 %+v", snap.RubyRetry)
		}
	})

	t.Run("显式 backend_id>0 不走回退", func(t *testing.T) {
		rrBackend, err := client.Backend.Create().
			SetName("dedicated-ruby-backend").
			SetBackendType(entbackend.BackendType("openai")).
			SetScope("user").
			SetOwnerUserID(user.ID).
			SetOptions(map[string]any{}).
			Save(ctx)
		if err != nil {
			t.Fatalf("create ruby backend: %v", err)
		}
		snap, err := jobs.validateAndSnapshotWith(ctx, user.ID, newPlan(
			schema.ExecutionPlanRubyRetryConfig{Enabled: true, BackendID: rrBackend.ID, MaxAttempts: 3},
			[]schema.ExecutionRoundConfig{validTranslateRound(backendRow.ID)},
		), "", check)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if snap.RubyRetry == nil || snap.RubyRetry.Backend.ID != rrBackend.ID {
			t.Fatalf("显式后端应原样物化，实际 %+v", snap.RubyRetry)
		}
		if snap.RubyRetry.MaxAttempts != 3 {
			t.Fatalf("MaxAttempts=%d want 3", snap.RubyRetry.MaxAttempts)
		}
	})
}
