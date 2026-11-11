package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type executionScopeKey struct{}
type executionActorLifetime struct {
	scope   resourcebudget.Scope
	mu      sync.Mutex
	release func()
	closed  bool
}

var executionCallSequence atomic.Uint64

// WithExecutionActor keeps the authenticated caller through service and runner
// boundaries. Project ownership never substitutes for a synchronous caller.
func WithExecutionActor(ctx context.Context, actorID int) context.Context {
	if owner, ok := ctx.Value(executionScopeKey{}).(*executionActorLifetime); ok && owner.scope.ActorID == actorID {
		return ctx
	}
	return context.WithValue(ctx, executionScopeKey{}, &executionActorLifetime{scope: resourcebudget.Scope{ActorID: actorID, CallID: fmt.Sprintf("call:%d", executionCallSequence.Add(1))}})
}

func WithExecutionJobActor(ctx context.Context, actorID, jobID int) context.Context {
	return context.WithValue(ctx, executionScopeKey{}, &executionActorLifetime{scope: resourcebudget.Scope{ActorID: actorID, JobID: jobID}})
}

func ExecutionScope(ctx context.Context) resourcebudget.Scope {
	if owner, ok := ctx.Value(executionScopeKey{}).(*executionActorLifetime); ok {
		return owner.scope
	}
	return resourcebudget.Scope{}
}

// PrepareExecutionActor keeps a lazy actor cache alive through the interval
// before its first root Claim. Repeated preparation of this scope is idempotent.
func PrepareExecutionActor(ctx context.Context, acquire func() (func(), error)) error {
	owner, ok := ctx.Value(executionScopeKey{}).(*executionActorLifetime)
	if !ok {
		return fmt.Errorf("execution scope is required")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return fmt.Errorf("execution scope is closed")
	}
	if owner.release != nil {
		return nil
	}
	release, err := acquire()
	if err != nil {
		return err
	}
	owner.release = release
	return nil
}

func ReleaseExecutionActor(ctx context.Context) {
	owner, ok := ctx.Value(executionScopeKey{}).(*executionActorLifetime)
	if !ok {
		return
	}
	owner.mu.Lock()
	release := owner.release
	owner.release = nil
	owner.closed = true
	owner.mu.Unlock()
	if release != nil {
		release()
	}
}
