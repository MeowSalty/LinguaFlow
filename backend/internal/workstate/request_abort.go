package workstate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

type attemptCounters struct {
	Main             int `json:"main"`
	Alignment        int `json:"alignment"`
	Network          int `json:"network"`
	MainNetwork      int `json:"main_network"`
	AlignmentNetwork int `json:"alignment_network"`
}

type requestDebit struct {
	SegmentID int             `json:"segment_id"`
	Pool      int             `json:"pool"`
	Before    attemptCounters `json:"before"`
	After     attemptCounters `json:"after"`
}

func attemptsFromRow(w *ent.WorkItem) attemptCounters {
	return attemptCounters{Main: w.MainAttempts, Alignment: w.AlignmentAttempts, Network: w.NetworkAttempts, MainNetwork: w.MainNetworkAttempts, AlignmentNetwork: w.AlignmentNetworkAttempts}
}

// AbortRequest is only for a live dispatcher that can prove admission failed
// before any network invocation. Recovery must use MarkUnknown instead.
// The request identity is retired permanently, so retries need a new identity.
// Single-owner work admission ensures no successor has debited the same cursor.
func (s *Store) AbortRequest(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("invalid request identity")
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
			return nil
		}
		if row.State != "reserved" {
			return ErrStopped
		}
		var debits []requestDebit
		if err := json.Unmarshal(row.Debits, &debits); err != nil || len(debits) != len(row.SegmentIds) {
			return fmt.Errorf("%w: request reservation has no trustworthy debit record", ErrManifest)
		}
		job, err := tx.Job.Get(ctx, row.JobID)
		if err != nil {
			return err
		}
		if job.RetryEpoch == row.RetryEpoch {
			for _, debit := range debits {
				w, err := tx.WorkItem.Query().Where(workitem.JobIDEQ(row.JobID), workitem.JobRoundIDEQ(row.JobRoundID), workitem.ResourceIDEQ(row.ResourceID), workitem.SegmentIDEQ(debit.SegmentID)).Only(ctx)
				if ent.IsNotFound(err) {
					continue
				}
				if err != nil {
					return err
				}
				if w.RetryEpoch != row.RetryEpoch || w.PoolIndex != debit.Pool {
					continue
				}
				if attemptsFromRow(w) != debit.After {
					return fmt.Errorf("%w: request cursor changed before abort", ErrCandidateVersion)
				}
				before := debit.Before
				if err := tx.WorkItem.UpdateOneID(w.ID).SetMainAttempts(before.Main).SetAlignmentAttempts(before.Alignment).SetNetworkAttempts(before.Network).SetMainNetworkAttempts(before.MainNetwork).SetAlignmentNetworkAttempts(before.AlignmentNetwork).Exec(ctx); err != nil {
					return err
				}
			}
		}
		return tx.WorkRequest.UpdateOneID(row.ID).SetState("cancelled_before_dispatch").SetLastError("").Exec(ctx)
	})
}
