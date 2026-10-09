package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

// ReserveRequest debits each member once before admission. Repeating the same
// identity is idempotent; a new batch identity never refreshes member budgets.
func (s *Store) ReserveRequest(ctx context.Context, r Request) error {
	if r.ID == "" || len(r.SegmentIDs) == 0 || r.Stage == "" || r.InputDigest == "" || r.BudgetModel == "" {
		return fmt.Errorf("invalid request reservation")
	}
	ids := append([]int(nil), r.SegmentIDs...)
	sort.Ints(ids)
	for i, id := range ids {
		if id < 1 || (i > 0 && id == ids[i-1]) {
			return ErrManifest
		}
	}
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, r.Scope, true); err != nil {
			return err
		}
		old, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).Only(ctx)
		if err == nil {
			if old.JobID != r.Scope.JobID || old.JobRoundID != r.Scope.RoundID || old.ResourceID != r.Scope.ResourceID || old.RetryEpoch != r.Scope.RetryEpoch || old.InputDigest != r.InputDigest || old.Stage != r.Stage || old.BackendID != r.BackendID || old.CandidateID != r.CandidateID || old.BudgetModel != r.BudgetModel || !reflect.DeepEqual(old.SegmentIds, ids) {
				return fmt.Errorf("request identity already used for different input")
			}
			if old.State == "cancelled_before_dispatch" {
				return ErrStopped
			}
			return nil
		}
		if !ent.IsNotFound(err) {
			return err
		}
		logicalAttempt := 0
		debits := make([]requestDebit, 0, len(ids))
		for _, id := range ids {
			w, err := member(ctx, tx, r.Scope, id)
			if err != nil {
				return err
			}
			if w.State == "resolved" {
				return ErrStale
			}
			if r.CandidateID != "" && w.CandidateID != r.CandidateID {
				return ErrCandidateVersion
			}
			if r.MainAttempt && r.MaxMainAttempts > 0 && w.MainAttempts >= r.MaxMainAttempts {
				return ErrBudget
			}
			if r.AlignmentAttempt && r.MaxAlignmentAttempts > 0 && w.AlignmentAttempts >= r.MaxAlignmentAttempts {
				return ErrBudget
			}
			alignment := r.Stage == "ruby_alignment" || r.Stage == "alignment"
			if alignment {
				logicalAttempt = w.AlignmentAttempts
				if r.AlignmentAttempt {
					logicalAttempt++
				}
			} else {
				logicalAttempt = w.MainAttempts
				if r.MainAttempt {
					logicalAttempt++
				}
			}
			network := w.MainNetworkAttempts
			fresh := r.MainAttempt
			if alignment {
				network = w.AlignmentNetworkAttempts
				fresh = r.AlignmentAttempt
			}
			if fresh {
				network = 0
			}
			if r.MaxNetworkAttempts > 0 && network >= r.MaxNetworkAttempts {
				return ErrBudget
			}
			network++
			before := attemptsFromRow(w)
			after := before
			after.Network = network
			u := tx.WorkItem.UpdateOneID(w.ID).SetNetworkAttempts(network)
			if alignment {
				u.SetAlignmentNetworkAttempts(network)
				after.AlignmentNetwork = network
			} else {
				u.SetMainNetworkAttempts(network)
				after.MainNetwork = network
			}
			if r.MainAttempt {
				u.AddMainAttempts(1)
				after.Main++
			}
			if r.AlignmentAttempt {
				u.AddAlignmentAttempts(1)
				after.Alignment++
			}
			if err := u.Exec(ctx); err != nil {
				return err
			}
			debits = append(debits, requestDebit{SegmentID: id, Pool: w.PoolIndex, Before: before, After: after})
		}
		encoded, err := json.Marshal(debits)
		if err != nil {
			return err
		}
		return tx.WorkRequest.Create().SetIdentity(r.ID).SetCandidateID(r.CandidateID).SetLogicalAttempt(logicalAttempt).SetDebits(encoded).SetJobID(r.Scope.JobID).SetResourceID(r.Scope.ResourceID).SetJobRoundID(r.Scope.RoundID).SetRetryEpoch(r.Scope.RetryEpoch).SetSegmentIds(ids).SetStage(r.Stage).SetBackendID(r.BackendID).SetBudgetModel(r.BudgetModel).SetInputDigest(r.InputDigest).Exec(ctx)
	})
}

// RecordRequest accepts accounting for an already reserved request after
// cancellation; it never creates a candidate or changes accepted content.
func (s *Store) RecordRequest(ctx context.Context, id string, result RequestResult) error {
	if result.State != "sent" && result.State != "received" && result.State != "unknown" && result.State != "completed" && result.State != "failed" {
		return fmt.Errorf("invalid request result state")
	}
	return Transaction(ctx, s.client, func(tx *ent.Client) error {
		row, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id)).Only(ctx)
		if err != nil {
			return err
		}
		if err := LockJob(ctx, tx, row.JobID); err != nil {
			return err
		}
		row, err = tx.WorkRequest.Get(ctx, row.ID)
		if err != nil {
			return err
		}
		if row.State == "cancelled_before_dispatch" {
			return ErrStopped
		}
		u := tx.WorkRequest.UpdateOneID(row.ID)
		if row.State != "completed" && row.State != "failed" && !(row.State == "received" && result.State == "sent") {
			u.SetState(result.State).SetLastError(result.LastError)
		}
		if row.UsageRecordID == nil && (result.State == "sent" || result.State == "received" || result.State == "completed" || result.State == "failed") {
			j, err := tx.Job.Query().Where(job.IDEQ(row.JobID)).WithCreatedBy().WithProject().Only(ctx)
			if err != nil {
				return err
			}
			usage := tx.UsageRecord.Create().SetVisibilityScope("project").SetProjectID(j.ProjectID).SetSource("job").SetAPICalls(1).SetSegmentCount(len(row.SegmentIds)).SetNote(fmt.Sprintf("job:%d request:%s", row.JobID, row.Identity))
			if j.Edges.CreatedBy != nil {
				usage.SetUserID(j.Edges.CreatedBy.ID)
			}
			if p := j.Edges.Project; p != nil && p.OwnerUserID == nil && p.OwnerOrgID != nil {
				usage.SetOrganizationID(*p.OwnerOrgID)
			}
			if result.UsageKnown {
				usage.SetInputTokens(usageInt(result.InputTokens)).SetOutputTokens(usageInt(result.OutputTokens))
			}
			created, err := usage.Save(ctx)
			if err != nil {
				return err
			}
			u.SetUsageRecordID(created.ID)
		} else if row.UsageRecordID != nil && result.UsageKnown && !row.UsageKnown {
			if err := tx.UsageRecord.UpdateOneID(*row.UsageRecordID).SetInputTokens(usageInt(result.InputTokens)).SetOutputTokens(usageInt(result.OutputTokens)).Exec(ctx); err != nil {
				return err
			}
		}
		if result.UsageKnown && !row.UsageKnown {
			u.SetUsageKnown(true).SetInputTokens(result.InputTokens).SetOutputTokens(result.OutputTokens)
		}
		if result.DurationMS > row.DurationMs {
			u.SetDurationMs(result.DurationMS)
		}
		return u.Exec(ctx)
	})
}

func usageInt(n int64) int {
	if n < 0 {
		return 0
	}
	if n > 2147483647 {
		return 2147483647
	}
	return int(n)
}

// MarkUnknown is called only while the lifecycle claim is inactive, before
// resumed dispatch. An uncertain request keeps its already debited attempts.
func MarkUnknown(ctx context.Context, tx *ent.Client, jobID int) error {
	_, err := tx.WorkRequest.Update().Where(workrequest.JobIDEQ(jobID), workrequest.StateIn("reserved", "sent", "received")).SetState("unknown").Save(ctx)
	return err
}
