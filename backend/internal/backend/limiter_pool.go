package backend

import "sync"

// LimiterPool 管理共享的 RateLimiter 实例，按 backend ID 索引。
// 多个 goroutine 并发翻译同一 backend 时，通过共享 limiter 协调速率。
type LimiterPool struct {
	mu       sync.Mutex
	limiters map[int]RateLimiter
}

// NewLimiterPool 创建空的 LimiterPool。
func NewLimiterPool() *LimiterPool {
	return &LimiterPool{
		limiters: make(map[int]RateLimiter),
	}
}

// Get 返回 backendID 对应的共享 limiter；不存在时按 ratePerMinute 创建。
// ratePerMinute <= 0 返回 nopLimiter。
// 线程安全。
func (p *LimiterPool) Get(backendID int, ratePerMinute int) RateLimiter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l, ok := p.limiters[backendID]; ok {
		return l
	}
	l := NewRateLimiterPerMinute(ratePerMinute)
	p.limiters[backendID] = l
	return l
}

// Refresh 关闭 backendID 的旧 limiter 并按新 ratePerMinute 创建。
// 用于 backend 配置变更时热更新。
// 线程安全。
func (p *LimiterPool) Refresh(backendID int, ratePerMinute int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.limiters[backendID]; ok {
		old.Close()
	}
	p.limiters[backendID] = NewRateLimiterPerMinute(ratePerMinute)
}

// Remove 关闭并移除 backendID 的 limiter。
// 用于 backend 删除时清理资源。
// 线程安全。
func (p *LimiterPool) Remove(backendID int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.limiters[backendID]; ok {
		old.Close()
		delete(p.limiters, backendID)
	}
}
