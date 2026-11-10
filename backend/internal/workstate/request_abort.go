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
	GrowthReserved bool            `json:"growth_reserved"`
	SegmentID      int             `json:"segment_id"`
	Pool           int             `json:"pool"`
	MemberOwned    bool            `json:"member_owned"`
	Before         attemptCounters `json:"before"`
	After          attemptCounters `json:"after"`
}

func attemptsFromRow(w *ent.WorkItem) attemptCounters {
	return attemptCounters{Main: w.MainAttempts, Alignment: w.AlignmentAttempts, Network: w.NetworkAttempts, MainNetwork: w.MainNetworkAttempts, AlignmentNetwork: w.AlignmentNetworkAttempts}
}

// AbortRequest is only for a live dispatcher that can prove admission failed
// before any network invocation. Recovery must use MarkUnknown instead.
// The request identity is retired permanently, so retries need a new identity.
// Single-owner work admission ensures no successor has debited the same cursor.
func (s *Store) AbortRequest(ctx context.Context, id string) error {
	return s.abortRequest(ctx, nil, id)
}
func (s *Store) AbortRequestForScope(ctx context.Context, scope Scope, id string) error {
	return s.abortRequest(ctx, &scope, id)
}
func (s *Store) abortRequest(ctx context.Context, scope *Scope, id string) error {
	if id == "" {
		return fmt.Errorf("invalid request identity")
	}
	jobID := 0
	ctx = operationContext(ctx)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if scope != nil {
			jobID = scope.JobID
		} else {
			header, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id)).Select(workrequest.FieldID, workrequest.FieldJobID).Only(ctx)
			if err != nil {
				return err
			}
			jobID = header.JobID
		}
		if err := LockJob(ctx, tx, jobID); err != nil {
			return err
		}
		row, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id)).Only(ctx)
		if err != nil {
			return err
		}
		if row.JobID != jobID || scope != nil && (row.ResourceID != scope.ResourceID || row.JobRoundID != scope.RoundID || row.RetryEpoch != scope.RetryEpoch) {
			return ErrManifest
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
			scope := Scope{JobID: row.JobID, RoundID: row.JobRoundID, ResourceID: row.ResourceID, RetryEpoch: row.RetryEpoch}
			items, err := queryMemberRows(ctx, tx, scope, row.SegmentIds, false)
			if err != nil {
				return err
			}
			candidates, err := loadRequestCandidates(ctx, tx, members)
			if err != nil {
				return err
			}
			type refund struct {
				row    *ent.WorkItem
				before attemptCounters
				owned  bool
				ids    []int
			}
			groups := map[string]*refund{}
			var order []string
			for _, debit := range debits {
				w := items[debit.SegmentID]
				if w == nil || w.RetryEpoch != row.RetryEpoch || w.PoolIndex != debit.Pool {
					continue
				}
				if len(members) > 0 {
					m, ok := byID[debit.SegmentID]
					c := candidates[m.CandidateID]
					if !ok || m.CandidateID != w.CandidateID || m.Pool != w.PoolIndex || m.WorkID != WorkIdentity(scope, w.SegmentID) || c == nil || c.WorkItemID != w.ID || c.Version != m.CandidateVersion {
						return ErrCandidateVersion
					}
				}
				if attemptsFromRow(w) != debit.After {
					return fmt.Errorf("%w: request cursor changed before abort", ErrCandidateVersion)
				}
				if alignmentStage(row.Stage) && w.CandidateID != "" {
					if err := tx.WorkCandidate.Update().Where(workcandidate.IdentityEQ(w.CandidateID)).SetGrowthReserved(debit.GrowthReserved).Exec(ctx); err != nil {
						return err
					}
				}
				data, err := json.Marshal([]any{w.State, w.CandidateID, w.PoolIndex, w.PromptPhase, debit.Before, debit.After, debit.MemberOwned})
				if err != nil {
					return err
				}
				key := string(data)
				g := groups[key]
				if g == nil {
					g = &refund{row: w, before: debit.Before, owned: debit.MemberOwned}
					groups[key] = g
					order = append(order, key)
				}
				g.ids = append(g.ids, w.ID)
			}
			for _, key := range order {
				g := groups[key]
				for start := 0; start < len(g.ids); start += bulkRows {
					end := min(start+bulkRows, len(g.ids))
					before := g.before
					u := tx.WorkItem.Update().Where(append(expectedMember(scope, g.row), workitem.IDIn(g.ids[start:end]...))...).SetMemberOwned(g.owned).SetMainAttempts(before.Main).SetAlignmentAttempts(before.Alignment).SetNetworkAttempts(before.Network).SetMainNetworkAttempts(before.MainNetwork).SetAlignmentNetworkAttempts(before.AlignmentNetwork)
					if err := checkedUpdate(ctx, u, end-start); err != nil {
						return err
					}
				}
			}
		}
		n, err := tx.WorkRequest.Update().Where(workrequest.IDEQ(row.ID), workrequest.StateEQ("reserved")).SetState("cancelled_before_dispatch").SetLastError("").Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStopped
		}
		return nil
	})
	return s.finishUnknown(ctx, jobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		row, err := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id)).Only(ctx)
		if err != nil {
			return false, err
		}
		if row.JobID != jobID {
			return false, ErrManifest
		}
		return row.State == "cancelled_before_dispatch", nil
	})
}
