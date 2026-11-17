package service

import (
	"context"
	"errors"
	"math"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// RetainExplicitAllocation funds only a known make/copy capacity. A response
// source transfers its existing accurate receipt; an ordinary source uses this
// input owner. The caller installs the backing and clears it before lease close.
func (l *ExecutionInputLease) RetainExplicitAllocation(ctx context.Context, bytes int64) error {
	if bytes == 0 {
		return nil
	}
	if admission, _ := backend.BuildOwnership(ctx); admission == nil {
		if err := checkExecutionAllocationPlan(ctx, bytes, "input_growth"); err != nil {
			return err
		}
		return l.RetainPayload(bytes)
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return err
	}
	defer b.CloseRetired()
	allocation, err := b.OwnAllocation(bytes, 1)
	if err != nil {
		return err
	}
	defer allocation.Release()
	return l.adoptAllocation(ctx, allocation, bytes)
}

// AdoptRetainedAllocation moves a known retained portion of an existing build.
// It does not reserve a second copy of that backing.
func (l *ExecutionInputLease) AdoptRetainedAllocation(ctx context.Context, b *backend.BuildBounds, bytes int64) error {
	allocation, err := b.RetainedAllocation(bytes)
	if err != nil {
		return err
	}
	if err = l.adoptAllocation(ctx, allocation, bytes); err != nil {
		// The caller still owns the backing. Restore its receipt without a
		// refund; the original build retires it after references are cleared.
		return errors.Join(err, allocation.ReturnToBuild())
	}
	allocation.Release()
	return nil
}

func (l *ExecutionInputLease) adoptAllocation(ctx context.Context, allocation *backend.OwnedAllocation, bytes int64) error {
	l.mu.Lock()
	if l.closed || bytes < 0 || bytes > int64(math.MaxInt)-l.usage.PayloadBytes {
		l.mu.Unlock()
		return resourcebudget.ErrIdentity
	}
	transferred, err := backend.TransferBuildAllocationToLease(ctx, allocation, l.state.root, l.state.scope, l.id)
	if err != nil {
		l.mu.Unlock()
		return executionCapacityFailure(err, "input_transfer", l.id)
	}
	if !transferred {
		l.mu.Unlock()
		return l.RetainPayload(bytes)
	}
	l.usage.PayloadBytes += bytes
	l.state.mu.Lock()
	l.state.payload += bytes
	l.state.mu.Unlock()
	l.mu.Unlock()
	return nil
}
