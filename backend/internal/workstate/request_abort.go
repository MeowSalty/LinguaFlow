package workstate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
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
		members, err := decodeRequestMembers(row.Members)
		if err != nil {
			return err
		}
		byID := make(map[int]RequestMember, len(members))
		for _, m := range members {
			if _, duplicate := byID[m.SegmentID]; duplicate {
				return ErrManifest
			}
			byID[m.SegmentID] = m
		}
		if len(members) > 0 && len(members) != len(debits) {
			return ErrManifest
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
				if len(members) > 0 {
					m, ok := byID[debit.SegmentID]
					if !ok || m.CandidateID != w.CandidateID || m.Pool != w.PoolIndex || m.WorkID != WorkIdentity(Scope{JobID: row.JobID, RoundID: row.JobRoundID}, w.SegmentID) {
						return ErrCandidateVersion
					}
					valid, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(m.CandidateID), workcandidate.WorkItemIDEQ(w.ID), workcandidate.VersionEQ(m.CandidateVersion)).Exist(ctx)
					if err != nil {
						return err
					}
					if !valid {
						return ErrCandidateVersion
					}
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
