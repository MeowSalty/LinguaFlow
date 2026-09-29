package worker

import (
	"context"
	"errors"
	"sync"
)

// ResourceMutex serializes writers. References include holders and waiters, so
// cancelling a waiter cannot create a second live lock for the same resource.
type ResourceMutex struct {
	mu    sync.Mutex
	locks map[int]*resourceLock
}

type resourceLock struct {
	token    chan struct{}
	refCount int
}

func NewResourceMutex() *ResourceMutex {
	return &ResourceMutex{locks: make(map[int]*resourceLock)}
}

func (rm *ResourceMutex) Acquire(ctx context.Context, resourceID int) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resourceID <= 0 {
		return nil, errors.New("resource ID must be positive")
	}
	rm.mu.Lock()
	rl := rm.locks[resourceID]
	if rl == nil {
		rl = &resourceLock{token: make(chan struct{}, 1)}
		rl.token <- struct{}{}
		rm.locks[resourceID] = rl
	}
	rl.refCount++
	rm.mu.Unlock()

	dropReference := func() {
		rm.mu.Lock()
		defer rm.mu.Unlock()
		rl.refCount--
		if rl.refCount == 0 {
			delete(rm.locks, resourceID)
		}
	}
	select {
	case <-ctx.Done():
		dropReference()
		return nil, ctx.Err()
	case <-rl.token:
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			rl.token <- struct{}{}
			dropReference()
		})
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
