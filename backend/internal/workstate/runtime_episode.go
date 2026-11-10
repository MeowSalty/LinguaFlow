package workstate

import (
	"context"
	"encoding/json"
	"math"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

const RuntimeStateLimit = 2048

func decodeRuntimeState(payload []byte) (backend.PipelineRuntimeState, error) {
	s := backend.PipelineRuntimeState{Version: 1}
	if len(payload) == 0 {
		return s, nil
	}
	if len(payload) > RuntimeStateLimit {
		return s, ErrManifest
	}
	if err := json.Unmarshal(payload, &s); err != nil || s.Version != 1 || s.Generation < 0 || s.N0 < 0 || s.B0 < 0 || s.J0 < 0 {
		return s, ErrManifest
	}
	return s, nil
}
func saveRuntimeState(ctx context.Context, tx *ent.Client, jobID int, s backend.PipelineRuntimeState) error {
	b, err := backend.NewBuildBounds(RuntimeStateLimit, 0)
	if err != nil {
		return err
	}
	data, err := b.EncodeJSON(s, RuntimeStateLimit)
	if err != nil {
		return err
	}
	return tx.Job.UpdateOneID(jobID).SetPipelineRuntimeState(data).Exec(ctx)
}
func (s *Store) LoadRuntimeState(ctx context.Context, jobID int) (backend.PipelineRuntimeState, error) {
	j, err := s.client.Job.Get(ctx, jobID)
	if err != nil {
		return backend.PipelineRuntimeState{}, err
	}
	if err := guardExecution(ctx, s.client, jobID); err != nil {
		return backend.PipelineRuntimeState{}, err
	}
	return decodeRuntimeState(j.PipelineRuntimeState)
}

func addInventory(total *int64, value int64) error {
	if value < 0 || value > math.MaxInt64-*total {
		return backend.ErrPipelineCapacity
	}
	*total += value
	return nil
}
func runtimeInventory(ctx context.Context, tx *ent.Client, jobID int, itemLimit int64) (n, bytes, handoff int64, err error) {
	for after := 0; ; {
		rows, e := tx.WorkCandidate.Query().Where(workcandidate.IDGT(after), workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(jobID))).Select(workcandidate.FieldID, workcandidate.FieldPayloadBytes, workcandidate.FieldGrowthReserved).Order(ent.Asc(workcandidate.FieldID)).Limit(16).WithWorkItem(func(q *ent.WorkItemQuery) { q.Select(workitem.FieldID, workitem.FieldAlignmentAttempts) }).All(ctx)
		if e != nil {
			return 0, 0, 0, e
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			b := row.PayloadBytes
			if row.GrowthReserved || row.Edges.WorkItem != nil && row.Edges.WorkItem.AlignmentAttempts > 0 {
				b = max(b, itemLimit)
			}
			if e = addInventory(&bytes, b); e != nil {
				return 0, 0, 0, e
			}
			after = row.ID
		}
	}
	for after := 0; ; {
		rows, e := tx.WorkRequest.Query().Where(workrequest.IDGT(after), workrequest.JobIDEQ(jobID), workrequest.StateEQ("completed"), workrequest.PromotionStateIn("response_pending", "glossary_processed")).Select(workrequest.FieldID, workrequest.FieldPromotionCount, workrequest.FieldPromotionCursor, workrequest.FieldFreezeBytes, workrequest.FieldResponseBytes, workrequest.FieldCorrectedTargetBytes).Order(ent.Asc(workrequest.FieldID)).Limit(16).All(ctx)
		if e != nil {
			return 0, 0, 0, e
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			for _, b := range []int64{row.FreezeBytes, row.ResponseBytes, row.CorrectedTargetBytes, backend.RequestMetadataBytes} {
				if e = addInventory(&handoff, b); e != nil {
					return 0, 0, 0, e
				}
			}
			after = row.ID
		}
	}
	for after := 0; ; {
		rows, e := tx.WorkItem.Query().Where(workitem.IDGT(after), workitem.JobIDEQ(jobID), workitem.Or(workitem.MemberOwnedEQ(true), workitem.StateIn("candidate", "response_pending"))).Select(workitem.FieldID).Order(ent.Asc(workitem.FieldID)).Limit(16).All(ctx)
		if e != nil {
			return 0, 0, 0, e
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if e = addInventory(&n, 1); e != nil {
				return 0, 0, 0, e
			}
			after = row.ID
		}
	}
	return n, bytes, handoff, nil
}

func (s *Store) BeginRuntimeProgress(ctx context.Context, jobID int, id, response string, resource, round int, limits backend.PipelineBudgetLimits) (backend.PipelineRuntimeState, error) {
	var state backend.PipelineRuntimeState
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, jobID); err != nil {
			return err
		}
		j, err := tx.Job.Get(ctx, jobID)
		if err != nil {
			return err
		}
		if j.Status != "running" && j.Status != "pausing" && j.Status != "pending" {
			return ErrStopped
		}
		state, err = decodeRuntimeState(j.PipelineRuntimeState)
		if err != nil {
			return err
		}
		if state.ProgressID != id {
			earlier, e := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID), jobround.RoundIndexLT(round), jobround.StatusNotIn("completed", "skipped")).Exist(ctx)
			if e != nil {
				return e
			}
			if earlier {
				return backend.ErrEarlierScope
			}
		}
		if state.ProgressID != "" && state.ProgressID != id {
			return backend.ErrProgressRequired
		}
		if err := captureRecoveryEpisode(ctx, tx, jobID, id, response, limits, &state); err != nil {
			return err
		}
		state.ProgressID, state.ResponseID, state.ResourceID, state.RoundIndex = id, response, resource, round
		return saveRuntimeState(ctx, tx, jobID, state)
	})
	if err != nil {
		loaded, e := s.LoadRuntimeState(ctx, jobID)
		if e == nil && loaded.EpisodeActive && loaded.ProgressID == id && loaded.ResponseID == response {
			return loaded, nil
		}
	}
	return state, err
}

func clearRuntimeBindingTx(ctx context.Context, tx *ent.Client, jobID int, id string, responseDone bool) error {
	j, err := tx.Job.Get(ctx, jobID)
	if err != nil {
		return err
	}
	state, err := decodeRuntimeState(j.PipelineRuntimeState)
	if err != nil {
		return err
	}
	changed := false
	if state.ProgressID == id {
		state.ProgressID = ""
		state.ResponseID = ""
		changed = true
	}
	if responseDone && state.ResponseID == id {
		state.ResponseID = ""
		changed = true
	}
	if responseDone && state.BusinessOwner == id {
		state.BusinessOwner = ""
		changed = true
	}
	if changed {
		return saveRuntimeState(ctx, tx, jobID, state)
	}
	return nil
}

func (s *Store) RefreshRuntimeEpisode(ctx context.Context, jobID int, limits backend.PipelineBudgetLimits) (backend.PipelineRuntimeState, error) {
	var state backend.PipelineRuntimeState
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		j, err := tx.Job.Get(ctx, jobID)
		if err != nil {
			return err
		}
		state, err = decodeRuntimeState(j.PipelineRuntimeState)
		if err != nil {
			return err
		}
		if state.EpisodeActive && state.ProgressID == "" && state.ResponseID == "" && state.BusinessOwner == "" {
			n, b, h, err := runtimeInventory(ctx, tx, jobID, limits.Item)
			if err != nil {
				return err
			}
			if n <= int64(max(0, limits.Segments-1)) && b <= max(int64(0), limits.Candidates-limits.Item) && h <= limits.Handoff-limits.Item-limits.Responses-backend.RequestMetadataBytes {
				state.EpisodeActive = false
				state.N0, state.B0, state.J0 = 0, 0, 0
				return saveRuntimeState(ctx, tx, jobID, state)
			}
		}
		return nil
	})
	return state, err
}

// MoveRuntimeProgress changes only the attachment after a header consumer has
// proved the previous scope inactive. Fixed episode inventory never rolls.
func (s *Store) MoveRuntimeProgress(ctx context.Context, jobID int, previous, business, id, response string, resource, round int, limits backend.PipelineBudgetLimits) (backend.PipelineRuntimeState, error) {
	var state backend.PipelineRuntimeState
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, jobID); err != nil {
			return err
		}
		j, err := tx.Job.Get(ctx, jobID)
		if err != nil {
			return err
		}
		if j.Status != "running" && j.Status != "pending" && j.Status != "pausing" {
			return ErrStopped
		}
		state, err = decodeRuntimeState(j.PipelineRuntimeState)
		if err != nil {
			return err
		}
		if state.ProgressID != id {
			earlier, e := tx.JobRound.Query().Where(jobround.JobIDEQ(jobID), jobround.RoundIndexLT(round), jobround.StatusNotIn("completed", "skipped")).Exist(ctx)
			if e != nil {
				return e
			}
			if earlier {
				return backend.ErrEarlierScope
			}
		}
		if state.ProgressID != "" && state.ProgressID != previous && state.ProgressID != id {
			return backend.ErrProgressRequired
		}
		if business != "" && state.BusinessOwner != business {
			return backend.ErrProgressRequired
		}
		if err := captureRecoveryEpisode(ctx, tx, jobID, id, response, limits, &state); err != nil {
			return err
		}
		state.ProgressID, state.ResponseID, state.ResourceID, state.RoundIndex = id, response, resource, round
		if business != "" {
			state.BusinessOwner = ""
		}
		return saveRuntimeState(ctx, tx, jobID, state)
	})
	if err != nil {
		loaded, e := s.LoadRuntimeState(ctx, jobID)
		if e == nil && loaded.EpisodeActive && loaded.ProgressID == id && loaded.ResponseID == response {
			return loaded, nil
		}
	}
	return state, err
}

func transferRuntimeBindingTx(ctx context.Context, tx *ent.Client, jobID int, from, to string) error {
	j, err := tx.Job.Get(ctx, jobID)
	if err != nil {
		return err
	}
	state, err := decodeRuntimeState(j.PipelineRuntimeState)
	if err != nil {
		return err
	}
	if state.ProgressID != from {
		return nil
	}
	state.ProgressID = to
	return saveRuntimeState(ctx, tx, jobID, state)
}

// CleanupCancelledPayloads runs only after the caller joined every resource and
// response callback. Accounting history and compact receipts remain readable.
func (s *Store) CleanupCancelledPayloads(ctx context.Context, jobID int) error {
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		j, err := tx.Job.Get(ctx, jobID)
		if err != nil {
			return err
		}
		if j.Status != "cancelled" {
			return nil
		}
		if err := guardExecution(ctx, tx, jobID); err != nil {
			return err
		}
		state, err := decodeRuntimeState(j.PipelineRuntimeState)
		if err != nil {
			return err
		}
		if state.Generation == math.MaxInt64 {
			return backend.ErrPipelineCapacity
		}
		state.Generation++
		state.EpisodeActive = false
		state.N0, state.B0, state.J0 = 0, 0, 0
		state.ProgressID, state.ResponseID, state.BusinessOwner = "", "", ""
		if _, err := tx.WorkRequest.Update().Where(workrequest.JobIDEQ(jobID), workrequest.Or(workrequest.FreezeBytesGT(0), workrequest.ResponseBytesGT(0), workrequest.CorrectedTargetBytesGT(0))).SetPromotionState("cancelled").ClearFreezePayload().SetFreezeBytes(0).ClearResponsePayload().SetResponseBytes(0).ClearCorrectedTargets().ClearCorrectedTargetIndex().SetCorrectedTargetBytes(0).SetBusinessBound(0).Save(ctx); err != nil {
			return err
		}
		if _, err := tx.WorkCandidate.Update().Where(workcandidate.HasWorkItemWith(workitem.JobIDEQ(jobID)), workcandidate.StateIn("pending_alignment", "ready_to_commit")).SetState("cancelled").ClearPayload().SetGrowthReserved(false).SetPayloadBytes(0).Save(ctx); err != nil {
			return err
		}
		if _, err := tx.WorkItem.Update().Where(workitem.JobIDEQ(jobID), workitem.StateNEQ("resolved")).SetMemberOwned(false).SetState("cancelled").SetCandidateID("").SetResponseRequestID("").ClearNextAttemptAt().Save(ctx); err != nil {
			return err
		}
		return saveRuntimeState(ctx, tx, jobID, state)
	})
}

// Ordinary progress binds its identity without inventing recovery ceilings.
// The first genuine stock debt captures aggregate inventory in this Job lock.
func captureRecoveryEpisode(ctx context.Context, tx *ent.Client, jobID int, id, response string, limits backend.PipelineBudgetLimits, state *backend.PipelineRuntimeState) error {
	if state.EpisodeActive {
		return nil
	}
	n, b, j, err := runtimeInventory(ctx, tx, jobID, limits.Item)
	if err != nil {
		return err
	}
	ordinaryN, ordinaryB, ordinaryJ := n, b, j
	c, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(id), workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(jobID))).Select(workcandidate.FieldID, workcandidate.FieldWorkItemID, workcandidate.FieldPayloadBytes, workcandidate.FieldGrowthReserved).WithWorkItem(func(q *ent.WorkItemQuery) { q.Select(workitem.FieldID, workitem.FieldAlignmentAttempts) }).Only(ctx)
	if err == nil {
		owned := c.PayloadBytes
		if c.GrowthReserved || c.Edges.WorkItem != nil && c.Edges.WorkItem.AlignmentAttempts > 0 {
			owned = max(owned, limits.Item)
		}
		ordinaryN = max(int64(0), ordinaryN-1)
		ordinaryB = max(int64(0), ordinaryB-owned)
	} else if !ent.IsNotFound(err) {
		return err
	}
	if state.ProgressID == id && response != "" {
		r, e := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(response), workrequest.JobIDEQ(jobID), workrequest.PromotionStateIn("response_pending", "glossary_processed")).Select(workrequest.FieldFreezeBytes, workrequest.FieldResponseBytes, workrequest.FieldCorrectedTargetBytes).Only(ctx)
		if e == nil {
			ordinaryN = max(int64(0), ordinaryN-1)
			owned, err := checkedInventorySum(r.FreezeBytes, r.ResponseBytes, r.CorrectedTargetBytes, backend.RequestMetadataBytes)
			if err != nil {
				return err
			}
			ordinaryJ = max(int64(0), ordinaryJ-owned)
		} else if !ent.IsNotFound(e) {
			return e
		}
	}
	if ordinaryN <= int64(max(0, limits.Segments-1)) && ordinaryB <= max(int64(0), limits.Candidates-limits.Item) && ordinaryJ <= limits.Handoff-limits.Item-limits.Responses-backend.RequestMetadataBytes {
		return nil
	}
	if state.Generation == math.MaxInt64 {
		return backend.ErrPipelineCapacity
	}
	state.EpisodeActive = true
	state.Generation++
	state.N0, state.B0, state.J0 = n, b, j
	return nil
}
func checkedInventorySum(v ...int64) (int64, error) {
	var n int64
	for _, x := range v {
		if err := addInventory(&n, x); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// ReleaseRuntimeBusiness is a fixed attachment handoff after the runtime proved
// the old saved scope inactive and reserved its actual body in the same J ledger.
// Ordinary rotation never starts a recovery episode or rolls inventory ceilings.
func (s *Store) ReleaseRuntimeBusiness(ctx context.Context, jobID int, previous string) error {
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, jobID); err != nil {
			return err
		}
		old, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(previous), workrequest.JobIDEQ(jobID))).Only(ctx)
		if err != nil {
			return err
		}
		if old.State != "completed" || old.PromotionState != "glossary_processed" || old.PromotionCursor >= old.PromotionCount {
			return ErrManifest
		}
		j, err := tx.Job.Get(ctx, jobID)
		if err != nil {
			return err
		}
		state, err := decodeRuntimeState(j.PipelineRuntimeState)
		if err != nil {
			return err
		}
		if state.BusinessOwner != "" && state.BusinessOwner != previous {
			return backend.ErrProgressRequired
		}
		state.BusinessOwner = ""
		return saveRuntimeState(ctx, tx, jobID, state)
	})
	if err != nil {
		state, e := s.LoadRuntimeState(ctx, jobID)
		if e == nil && state.BusinessOwner == "" {
			return nil
		}
	}
	return err
}
