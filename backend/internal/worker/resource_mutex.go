package worker

import (
	"context"
	"sync"
)

// ResourceMutex 提供 Resource 级互斥锁，确保同一 Resource 的任务串行执行。
// 不同 Resource 之间无锁，并行执行。
type ResourceMutex struct {
	mu    sync.Mutex
	locks map[int]*resourceLock
}

type resourceLock struct {
	mu       sync.Mutex
	refCount int
}

// NewResourceMutex 创建一个新的 ResourceMutex 实例。
func NewResourceMutex() *ResourceMutex {
	return &ResourceMutex{
		locks: make(map[int]*resourceLock),
	}
}

// Acquire 获取指定 Resource 的互斥锁。
// 阻塞直到锁可用或 ctx 取消。返回 release 函数用于释放锁。
func (rm *ResourceMutex) Acquire(ctx context.Context, resourceID int) (release func(), err error) {
	// 获取或创建 resourceLock
	rm.mu.Lock()
	rl, exists := rm.locks[resourceID]
	if !exists {
		rl = &resourceLock{}
		rm.locks[resourceID] = rl
	}
	rl.refCount++
	rm.mu.Unlock()

	// 尝试获取锁，支持 ctx 取消
	acquired := make(chan struct{})
	go func() {
		rl.mu.Lock()
		close(acquired)
	}()

	select {
	case <-ctx.Done():
		// ctx 取消，减少引用计数
		rm.mu.Lock()
		rl.refCount--
		if rl.refCount == 0 {
			delete(rm.locks, resourceID)
		}
		rm.mu.Unlock()
		// 等待 goroutine 获取锁后立即释放
		<-acquired
		rl.mu.Unlock()
		return nil, ctx.Err()
	case <-acquired:
		// 锁已获取
		return func() {
			rl.mu.Unlock()
			rm.mu.Lock()
			rl.refCount--
			if rl.refCount == 0 {
				delete(rm.locks, resourceID)
			}
			rm.mu.Unlock()
		}, nil
	}
}
