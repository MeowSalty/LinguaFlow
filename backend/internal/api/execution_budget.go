package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
	"time"
)

func (s *Server) prepareExecutionActor(ctx context.Context, actorID int) error {
	if actorID <= 0 {
		return fmt.Errorf("execution actor is required")
	}
	return service.PrepareExecutionActor(ctx, func() (func(), error) {
		s.budgetInventoryMu.Lock()
		defer s.budgetInventoryMu.Unlock()
		source := s.budgetSource
		source.ActorID = actorID
		if err := s.executionBudget.RebuildActor(ctx, actorID, source); err != nil {
			return nil, err
		}
		release, err := s.executionBudget.PinActor(actorID)
		if err != nil {
			_ = s.executionBudget.ForgetActor(actorID)
			return nil, err
		}
		return func() { release(); _ = s.executionBudget.ForgetActor(actorID) }, nil
	})
}

func (s *Server) prepareCancelledBudget(ctx context.Context, jobID int) (func(context.Context) error, error) {
	finish, err := s.prepareBudgetDeletion(ctx, jobID)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		factCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		writeErr := workstate.NewStore(s.entClient).CleanupCancelledPayloads(factCtx, jobID)
		return errors.Join(writeErr, finish(factCtx))
	}, nil
}

func (s *Server) prepareBudgetDeletion(ctx context.Context, jobID int) (func(context.Context) error, error) {
	actor, err := s.budgetSource.Actor(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if actor > 0 {
		ctx = service.WithExecutionActor(ctx, actor)
		if err = s.prepareExecutionActor(ctx, actor); err != nil {
			return nil, err
		}
	}
	s.budgetInventoryMu.RLock()
	before, storageBefore, err := s.budgetSource.Job(ctx, jobID)
	if err != nil {
		s.budgetInventoryMu.RUnlock()
		service.ReleaseExecutionActor(ctx)
		return nil, err
	}
	actorCtx := ctx
	return func(ctx context.Context) error {
		defer service.ReleaseExecutionActor(actorCtx)
		defer s.budgetInventoryMu.RUnlock()
		factCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		after, storageAfter, err := s.budgetSource.Job(factCtx, jobID)
		if err != nil {
			return err
		}
		delta := resourcebudget.Vector{UnconfirmedMembers: before.UnconfirmedMembers - after.UnconfirmedMembers, PayloadBytes: before.PayloadBytes - after.PayloadBytes, LogicalStorageBytes: storageBefore - storageAfter}
		return s.executionBudget.ReleaseInventory(resourcebudget.Scope{ActorID: actor, JobID: jobID}, delta)
	}, nil
}
