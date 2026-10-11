package backend

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrLimiterClosed = errors.New("backend rate limiter is closed")
var ErrLimiterMissing = errors.New("backend rate limit policy is unavailable")

type LimiterSnapshot struct {
	State                    string  `json:"state"`
	ActiveLimiters           int64   `json:"active_limiters"`
	Waiters                  int64   `json:"waiters"`
	WaitDurationSecondsSum   float64 `json:"wait_duration_seconds_sum"`
	WaitDurationSecondsCount int64   `json:"wait_duration_seconds_count"`
	WaitCancelledTotal       int64   `json:"wait_cancelled_total"`
}

// LimiterPool stores current capacity policy, independent of execution snapshots.
// Handles remain stable across Refresh; Remove invalidates existing handles and
// removes the map reference. Token replenishment uses elapsed time, not goroutines.
type LimiterPool struct {
	mu          sync.Mutex
	limiters    map[int]*limiterHandle
	initialized bool
	closed      bool
	metrics     LimiterSnapshot
}

type limiterHandle struct {
	pool    *LimiterPool
	rpm     int
	tokens  float64
	updated time.Time
	changed chan struct{}
	closed  bool
}

func NewLimiterPool() *LimiterPool { return &LimiterPool{limiters: make(map[int]*limiterHandle)} }

// Initialize replaces the policy registry before workers start. Missing IDs are
// subsequently errors; stale persisted execution snapshots cannot recreate them.
func (p *LimiterPool) Initialize(policies map[int]int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	for id, h := range p.limiters {
		if _, ok := policies[id]; !ok {
			p.removeLocked(id, h)
		}
	}
	for id, rpm := range policies {
		p.registerLocked(id, rpm)
	}
	p.initialized = true
}

func (p *LimiterPool) Register(id, rpm int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.registerLocked(id, rpm)
	}
}

func (p *LimiterPool) registerLocked(id, rpm int) {
	if rpm < 0 {
		rpm = 0
	}
	h, ok := p.limiters[id]
	if !ok {
		h = &limiterHandle{pool: p, changed: make(chan struct{})}
		p.limiters[id] = h
	}
	if h.rpm > 0 {
		p.metrics.ActiveLimiters--
	}
	if rpm > 0 {
		p.metrics.ActiveLimiters++
	}
	h.rpm, h.tokens, h.updated = rpm, float64(rpm), time.Now()
	close(h.changed)
	h.changed = make(chan struct{})
}

func (p *LimiterPool) Lookup(id int) (RateLimiter, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrLimiterClosed
	}
	h, ok := p.limiters[id]
	if !ok {
		return nil, ErrLimiterMissing
	}
	return h, nil
}

// Get is retained for standalone callers. Server execution uses Lookup and an
// initialized registry, so this compatibility path never revives deleted IDs.
func (p *LimiterPool) Get(id, rpm int) RateLimiter {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return failedLimiter{ErrLimiterClosed}
	}
	if h, ok := p.limiters[id]; ok {
		return h
	}
	if p.initialized {
		return failedLimiter{ErrLimiterMissing}
	}
	p.registerLocked(id, rpm)
	return p.limiters[id]
}

func (p *LimiterPool) Refresh(id, rpm int) { p.Register(id, rpm) }

func (p *LimiterPool) removeLocked(id int, h *limiterHandle) {
	if h.rpm > 0 {
		p.metrics.ActiveLimiters--
	}
	h.closed = true
	close(h.changed)
	delete(p.limiters, id)
}

func (p *LimiterPool) Remove(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h, ok := p.limiters[id]; ok {
		p.removeLocked(id, h)
	}
}

func (p *LimiterPool) Shutdown() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for id, h := range p.limiters {
		p.removeLocked(id, h)
	}
}

func (p *LimiterPool) Snapshot() LimiterSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.metrics
	s.State = "ready"
	if !p.initialized {
		s.State = "uninitialized"
	}
	if p.closed {
		s.State = "stopped"
	}
	return s
}

// tryTakeLocked performs the RPM part of joint admission while p.mu is held.
// Failed admission never consumes a token. The returned channel is invalidated
// by Refresh, Remove, and Shutdown, so callers must retry against current policy.
func (p *LimiterPool) tryTakeLocked(id int, now time.Time) (bool, time.Time, <-chan struct{}, error) {
	if p.closed {
		return false, time.Time{}, nil, ErrLimiterClosed
	}
	h, ok := p.limiters[id]
	if !ok || h.closed {
		return false, time.Time{}, nil, ErrLimiterMissing
	}
	if h.rpm <= 0 {
		return true, time.Time{}, h.changed, nil
	}
	h.tokens = min(float64(h.rpm), h.tokens+max(now.Sub(h.updated).Minutes(), 0)*float64(h.rpm))
	h.updated = now
	if h.tokens >= 1 {
		h.tokens--
		return true, time.Time{}, h.changed, nil
	}
	wait := time.Duration((1 - h.tokens) / float64(h.rpm) * float64(time.Minute))
	return false, now.Add(max(wait, time.Nanosecond)), h.changed, nil
}

func (h *limiterHandle) Wait(ctx context.Context) (err error) {
	p := h.pool
	var start time.Time
	defer func() {
		if start.IsZero() {
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.metrics.Waiters--
		p.metrics.WaitDurationSecondsCount++
		p.metrics.WaitDurationSecondsSum += time.Since(start).Seconds()
		if err != nil {
			p.metrics.WaitCancelledTotal++
		}
	}()
	for {
		p.mu.Lock()
		if err = ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		if p.closed || h.closed {
			p.mu.Unlock()
			return ErrLimiterClosed
		}
		if h.rpm <= 0 {
			p.mu.Unlock()
			return nil
		}
		now := time.Now()
		h.tokens = min(float64(h.rpm), h.tokens+now.Sub(h.updated).Minutes()*float64(h.rpm))
		h.updated = now
		if h.tokens >= 1 {
			h.tokens--
			p.mu.Unlock()
			return nil
		}
		if start.IsZero() {
			start = now
			p.metrics.Waiters++
		}
		wait := time.Duration((1 - h.tokens) / float64(h.rpm) * float64(time.Minute))
		changed := h.changed
		p.mu.Unlock()
		timer := time.NewTimer(max(wait, time.Nanosecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// A shared handle is owned by the pool, not by an individual backend wrapper.
func (*limiterHandle) Close() {}

type failedLimiter struct{ err error }

func (l failedLimiter) Wait(context.Context) error { return l.err }
func (failedLimiter) Close()                       {}
