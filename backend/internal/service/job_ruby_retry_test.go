package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
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
