package pipeline

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type RuntimeEpisodeStore interface {
	LoadRuntimeState(context.Context, int) (backend.PipelineRuntimeState, error)
	BeginRuntimeProgress(context.Context, int, string, string, int, int, backend.PipelineBudgetLimits) (backend.PipelineRuntimeState, error)
	MoveRuntimeProgress(context.Context, int, string, string, string, string, int, int, backend.PipelineBudgetLimits) (backend.PipelineRuntimeState, error)
	RefreshRuntimeEpisode(context.Context, int, backend.PipelineBudgetLimits) (backend.PipelineRuntimeState, error)
}

func (r *ExecutionRuntime) BudgetLimits() backend.PipelineBudgetLimits {
	return backend.PipelineBudgetLimits{Segments: r.Window.limits.Segments, Candidates: r.Window.limits.Bytes, Item: r.Window.limits.ItemBytes, Responses: r.ResponseBytes, Handoff: r.HandoffBytes}
}
func (r *ExecutionRuntime) ConfigureEpisodeStore(ctx context.Context, jobID int, store RuntimeEpisodeStore) error {
	state, err := store.LoadRuntimeState(ctx, jobID)
	if err != nil {
		return err
	}
	if err := r.Admission.RestoreRuntimeState(state); err != nil {
		return err
	}
	r.JobID = jobID
	r.EpisodeStore = store
	return nil
}
func (r *ExecutionRuntime) EnsureProgress(ctx context.Context, id, response string, resource, round int) error {
	r.episodeMu.Lock()
	defer r.episodeMu.Unlock()
	var state backend.PipelineRuntimeState
	var err error
	if err := r.Admission.PinResponses(r.Window.PinnedRawIDs(resource, round)); err != nil {
		return err
	}
	previous := r.Admission.RuntimeState()
	persisted := r.Admission.PersistedRuntimeState()
	replacement := persisted.ProgressID != "" && persisted.ProgressID != id && r.Window.ReplacesProgress(persisted.ProgressID, id)
	if replacement {
		previous.ProgressID = persisted.ProgressID
	}
	moveProgress := previous.ProgressID != "" && previous.ProgressID != id
	moveBusiness := previous.BusinessOwner != "" && previous.BusinessOwner != response
	if moveProgress && !replacement && !r.Window.TransferAllowed(previous.ProgressID, resource, round) {
		return backend.ErrProgressRequired
	}
	business := ""
	if moveBusiness && r.Window.TransferAllowed(previous.BusinessOwner, resource, round) {
		business = previous.BusinessOwner
	}
	if moveProgress || business != "" {
		if r.EpisodeStore != nil {
			state, err = r.EpisodeStore.MoveRuntimeProgress(ctx, r.JobID, previous.ProgressID, business, id, response, resource, round, r.BudgetLimits())
		} else {
			state, err = r.Admission.MemoryMoveProgress(previous.ProgressID, business, id, response, resource, round)
		}
		if err != nil {
			return err
		}
		return r.Admission.MoveProgress(previous.ProgressID, state)
	}
	if r.EpisodeStore != nil {
		state, err = r.EpisodeStore.BeginRuntimeProgress(ctx, r.JobID, id, response, resource, round, r.BudgetLimits())
	} else {
		state, err = r.Admission.MemoryProgress(id, response, resource, round)
	}
	if err != nil {
		return err
	}
	return r.Admission.AttachProgress(state)
}
func (r *ExecutionRuntime) RefreshEpisode(ctx context.Context) error {
	if r.EpisodeStore == nil {
		return r.Admission.RefreshMemoryEpisode()
	}
	r.episodeMu.Lock()
	defer r.episodeMu.Unlock()
	state, err := r.EpisodeStore.RefreshRuntimeEpisode(ctx, r.JobID, r.BudgetLimits())
	if err != nil {
		return err
	}
	return r.Admission.RestoreRuntimeState(state)
}

// ApplyCancelledPayloads follows the joined, durable cancellation transaction.
func (r *ExecutionRuntime) ApplyCancelledPayloads(ctx context.Context) error {
	for id := range r.Admission.BudgetEntries() {
		r.Window.Release(id)
	}
	for _, id := range r.Admission.ResponseOwnerIDs() {
		r.releaseMainOwner(id)
	}
	if r.EpisodeStore != nil {
		state, err := r.EpisodeStore.LoadRuntimeState(ctx, r.JobID)
		if err != nil {
			return err
		}
		return r.Admission.RestoreRuntimeState(state)
	}
	return nil
}

func (r *ExecutionRuntime) TransferBusiness(ctx context.Context, previous, next string, resource, round int) (bool, error) {
	r.episodeMu.Lock()
	defer r.episodeMu.Unlock()
	if !r.Window.TransferAllowed(previous, resource, round) {
		return false, nil
	}
	granted, err := r.Admission.CanTransferBusiness(previous, next)
	if err != nil || !granted {
		return granted, err
	}
	if r.EpisodeStore != nil {
		store, ok := r.EpisodeStore.(interface {
			ReleaseRuntimeBusiness(context.Context, int, string) error
		})
		if !ok {
			return false, backend.ErrPipelineCapacity
		}
		if err = store.ReleaseRuntimeBusiness(ctx, r.JobID, previous); err != nil {
			return false, err
		}
	}
	if err = r.Admission.ApplyBusinessTransfer(previous, next); err != nil {
		return false, err
	}
	return true, nil
}
