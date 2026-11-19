package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

var glossaryCacheSequence atomic.Uint64

// prepareCacheAllocation keeps the source receipt live through a transaction.
// Only the receiver lifecycle mutex spans preparation; Root/admission locks
// are released before DB work. Close cannot retire a still prepared cache.
func (l *ExecutionInputLease) prepareCacheAllocation(ctx context.Context, bytes int64) (glossaryCacheSettlement, error) {
	a, _ := backend.BuildOwnership(ctx)
	if a == nil {
		if err := l.RetainExplicitAllocation(ctx, bytes); err != nil {
			return glossaryCacheSettlement{}, err
		}
		return glossaryCacheSettlement{rollback: func() error { return l.ReleasePayload(bytes) }}, nil
	}
	l.mu.Lock()
	if l.closed || bytes < 0 || bytes > int64(math.MaxInt)-l.usage.PayloadBytes {
		l.mu.Unlock()
		return glossaryCacheSettlement{}, resourcebudget.ErrIdentity
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		l.mu.Unlock()
		return glossaryCacheSettlement{}, err
	}
	allocation, err := b.OwnAllocation(bytes, 1)
	if err != nil {
		b.CloseRetired()
		l.mu.Unlock()
		return glossaryCacheSettlement{}, err
	}
	target := fmt.Sprintf("glossary-cache:%s:%d", l.id, glossaryCacheSequence.Add(1))
	plan, err := backend.PrepareBuildAllocationTransfer(ctx, allocation, target)
	if err != nil {
		allocation.Release()
		b.CloseRetired()
		l.mu.Unlock()
		return glossaryCacheSettlement{}, err
	}
	if err = a.MoveBuildTo(target, l.state.root, l.state.scope, l.id, 0); err != nil {
		err = errors.Join(err, plan.Rollback())
		allocation.Release()
		b.CloseRetired()
		l.mu.Unlock()
		return glossaryCacheSettlement{}, err
	}
	settled, held := false, true
	finish := func() {
		allocation.Release()
		b.CloseRetired()
		if held {
			held = false
			l.mu.Unlock()
		}
	}
	return glossaryCacheSettlement{
		park: func() {
			if held {
				held = false
				l.mu.Unlock()
			}
		},
		commit: func() error {
			if settled {
				return nil
			}
			if !held {
				l.mu.Lock()
				held = true
			}
			if l.closed {
				held = false
				l.mu.Unlock()
				return resourcebudget.ErrIdentity
			}
			if err := a.MoveBuildTo(target, l.state.root, l.state.scope, l.id, bytes); err != nil {
				held = false
				l.mu.Unlock()
				return err
			}
			plan.Commit()
			l.usage.PayloadBytes += bytes
			l.state.mu.Lock()
			l.state.payload += bytes
			l.state.mu.Unlock()
			settled = true
			finish()
			return nil
		},
		rollback: func() error {
			if settled {
				return nil
			}
			if err := plan.Rollback(); err != nil {
				return err
			}
			settled = true
			finish()
			return nil
		},
	}, nil
}

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
