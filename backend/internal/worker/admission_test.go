package worker

import (
	"errors"
	"testing"
)

// TestAdmission_WeightBudget 字节配额流转：超预算拒绝 → 释放归还 → 可再次准入。
func TestAdmission_WeightBudget(t *testing.T) {
	a := newAdmission(100, 0) // maxWeight=100，资源数不限
	if err := a.admit(60); err != nil {
		t.Fatalf("admit(60) = %v, want nil", err)
	}
	// 60 + 60 > 100：超出字节预算。
	if err := a.admit(60); !errors.Is(err, errWeightBudget) {
		t.Fatalf("admit(60) again = %v, want errWeightBudget", err)
	}
	a.release(60)
	if err := a.admit(60); err != nil {
		t.Fatalf("release 后 admit(60) = %v, want nil", err)
	}
}

// TestAdmission_ResourceCap 资源数上限：字节预算充足时仍按资源数拒绝。
func TestAdmission_ResourceCap(t *testing.T) {
	a := newAdmission(0, 2) // 字节不限，资源数上限 2
	if err := a.admit(1); err != nil {
		t.Fatalf("admit#1 = %v, want nil", err)
	}
	if err := a.admit(1); err != nil {
		t.Fatalf("admit#2 = %v, want nil", err)
	}
	// 第三个资源：即便权重预算无限也应被资源数上限拒绝。
	if err := a.admit(1); !errors.Is(err, errResourceCap) {
		t.Fatalf("admit#3 = %v, want errResourceCap", err)
	}
	// 释放一个名额后可再次准入。
	a.release(1)
	if err := a.admit(1); err != nil {
		t.Fatalf("release 后 admit = %v, want nil", err)
	}
}

// TestAdmission_ZeroWeightCountsAsOne weight<=0 按 1（最小单元）占位，
// 防止零权重资源绕过预算。
func TestAdmission_ZeroWeightCountsAsOne(t *testing.T) {
	cases := []struct {
		name   string
		weight int64
	}{
		{"zero weight", 0},
		{"negative weight", -5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdmission(1, 0) // maxWeight=1：仅容纳一个最小单元
			if err := a.admit(tc.weight); err != nil {
				t.Fatalf("admit(%d) = %v, want nil（按 1 占位）", tc.weight, err)
			}
			if err := a.admit(tc.weight); !errors.Is(err, errWeightBudget) {
				t.Fatalf("第二次 admit(%d) = %v, want errWeightBudget", tc.weight, err)
			}
		})
	}
}

// TestAdmission_UnlimitedNeverRejects 无限模式（maxWeight=0 / maxResources=0）
// 永不拒绝。
func TestAdmission_UnlimitedNeverRejects(t *testing.T) {
	a := newAdmission(0, 0)
	for i := 0; i < 100; i++ {
		if err := a.admit(1 << 20); err != nil {
			t.Fatalf("admit#%d = %v, want nil（不限模式不应拒绝）", i+1, err)
		}
	}
}

// TestErrAdmissionRetryable 准入错误可重试判定：两类哨兵错误可重试，其余不可。
func TestErrAdmissionRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"weight budget", errWeightBudget, true},
		{"resource cap", errResourceCap, true},
		{"other error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errAdmissionRetryable(tc.err); got != tc.want {
				t.Fatalf("errAdmissionRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestAdmission_ReleaseClampsToZero 空预算上超额 release 不产生负计数
// （防御 release/admit 次数不对称）。
func TestAdmission_ReleaseClampsToZero(t *testing.T) {
	a := newAdmission(10, 0)
	a.release(999) // 未准入即释放：inflight/resources 应钳制在 0
	if err := a.admit(10); err != nil {
		t.Fatalf("admit(10) = %v, want nil（预算应为满额 10）", err)
	}
}

// TestAdmission_OversizeAdmittedOnEmptyBudget 饥饿回归：单资源权重超过
// 总预算时，预算空置（inflight==0）必须放行，而非无限拒绝。
func TestAdmission_OversizeAdmittedOnEmptyBudget(t *testing.T) {
	a := newAdmission(100, 0)
	if err := a.admit(500); err != nil {
		t.Fatalf("admit(500) 超预算但预算空置 = %v, want nil（独跑放行）", err)
	}
}

// TestAdmission_OversizeBlocksOthers 超限资源在途期间，其他资源（即使
// 权重很小）都被拒绝，保证独跑语义（峰值仅短暂超出一个资源）。
func TestAdmission_OversizeBlocksOthers(t *testing.T) {
	a := newAdmission(100, 0)
	if err := a.admit(500); err != nil {
		t.Fatalf("admit(500) = %v, want nil", err)
	}
	if err := a.admit(1); !errors.Is(err, errWeightBudget) {
		t.Fatalf("超限资源在途时 admit(1) = %v, want errWeightBudget", err)
	}
}

// TestAdmission_OversizeReleaseRestoresBudget 超限资源释放后，等待资源
// 恢复正常准入（inflight 归零，不再被残留超限计数阻塞）。
func TestAdmission_OversizeReleaseRestoresBudget(t *testing.T) {
	a := newAdmission(100, 0)
	if err := a.admit(500); err != nil {
		t.Fatalf("admit(500) = %v, want nil", err)
	}
	a.release(500)
	if err := a.admit(60); err != nil {
		t.Fatalf("释放后 admit(60) = %v, want nil", err)
	}
	// 正常预算语义恢复：60+60 > 100，第二个 60 应被拒绝。
	if err := a.admit(60); !errors.Is(err, errWeightBudget) {
		t.Fatalf("admit(60) = %v, want errWeightBudget（60+60 > 100）", err)
	}
}
