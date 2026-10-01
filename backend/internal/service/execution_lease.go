package service

import (
	"context"
	"sync"
)

type executionLeaseKey struct{}

// executionLeaseScope bridges resolution to either a committed Job reference or
// the end of a synchronous execution. The domain spec contains no lifecycle state.
type executionLeaseScope struct {
	releases []func()
	once     sync.Once
}

func withExecutionLeases(ctx context.Context) (context.Context, *executionLeaseScope) {
	leases := &executionLeaseScope{}
	return context.WithValue(ctx, executionLeaseKey{}, leases), leases
}
func (s *executionLeaseScope) release() {
	s.once.Do(func() {
		for _, release := range s.releases {
			release()
		}
	})
}
