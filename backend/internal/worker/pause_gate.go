package worker

import (
	"errors"
	"sync"
)

// worker 侧调度原语：准入预算（字节配额 + 资源数上限）。
// 站位信号量（pipeline.Station）与暂停闸门（pipeline.PauseGate）
// 定义在 pipeline 包——worker 注入、pipeline 消费，跨层共享同一实例。

// admission 原子准入预算：在途工作配额（字节）+ 在途资源数上限。
// RSS 保险丝（sysmem.Gate）由 JobRunner 在准入循环中直接调用 Allow()
// pull 型推进双水位状态机），不在本结构内镜像其状态。
type admission struct {
	mu           sync.Mutex
	maxWeight    int64 // 0 表示不限制
	maxResources int   // 0 表示不限制
	inflight     int64
	resources    int
}

// newAdmission 创建准入预算。
func newAdmission(maxWeightBytes int64, maxResources int) *admission {
	return &admission{maxWeight: maxWeightBytes, maxResources: maxResources}
}

var (
	errWeightBudget = errors.New("inflight work-weight budget exceeded")
	errResourceCap  = errors.New("inflight resource cap exceeded")
)

// admit 资源入线准入：weight 为该资源 work_weight（字节）。
// 预算空置（inflight==0）时放行超预算单资源独跑，消除饥饿：在途资源
// 总会释放，释放后等待者必能准入；超限资源在途期间，任何其他资源
// （权重 ≥1）都超出预算，天然串行。
// 不满足时返回错误，由资源 goroutine 周期性重试（间隔由调用方控制）。
func (a *admission) admit(weight int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := weightAllow(weight)
	if a.maxWeight > 0 && a.inflight > 0 && a.inflight+w > a.maxWeight {
		return errWeightBudget
	}
	if a.maxResources > 0 && a.resources >= a.maxResources {
		return errResourceCap
	}
	a.inflight += w
	a.resources++
	return nil
}

// weightAllow 计算准入权重：动态选择资源首次入线时 work_weight 可能为 0
// （尚未回填），按 1（最小单元）占位，避免零权重资源绕过预算。
func weightAllow(weight int64) int64 {
	if weight <= 0 {
		return 1
	}
	return weight
}

// release 资源离线：归还配额与资源计数。
func (a *admission) release(weight int64) {
	a.mu.Lock()
	a.inflight -= weightAllow(weight)
	if a.inflight < 0 {
		a.inflight = 0
	}
	a.resources--
	if a.resources < 0 {
		a.resources = 0
	}
	a.mu.Unlock()
}

// errAdmissionRetryable 判断准入错误是否可稍后重试。
func errAdmissionRetryable(err error) bool {
	return errors.Is(err, errWeightBudget) || errors.Is(err, errResourceCap)
}
