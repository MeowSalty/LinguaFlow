package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

// ReserveRequest debits each member once before admission. Repeating the same
// identity is idempotent; a new batch identity never refreshes member budgets.
func (s *Store) ReserveRequest(ctx context.Context, r Request) error {
	if r.ID == "" || len(r.ID) > 128 || len(r.SegmentIDs) == 0 || len(r.SegmentIDs) > 65536 || r.Stage == "" || r.InputDigest == "" || len(r.InputDigest) > 128 || r.BudgetModel == "" {
		return fmt.Errorf("invalid request reservation")
	}
	ids := append([]int(nil), r.SegmentIDs...)
	sort.Ints(ids)
	for i, id := range ids {
		if id < 1 || (i > 0 && id == ids[i-1]) {
			return ErrManifest
		}
	}
	var promotionPayload []byte
	var err error
	if len(r.PromotionMembers) > 0 {
		r.PromotionMembers = append([]PromotionMember(nil), r.PromotionMembers...)
		sort.Slice(r.PromotionMembers, func(i, j int) bool { return r.PromotionMembers[i].SegmentID < r.PromotionMembers[j].SegmentID })
		if r.Stage != "main" || len(r.FreezePayload) == 0 || r.SnapshotDigest == "" || r.BusinessBound < 0 {
			return ErrManifest
		}
		if err := validatePromotionMembers(r.Scope, ids, r.PromotionMembers); err != nil {
			return err
		}
		promotionPayload, err = json.Marshal(r.PromotionMembers)
		if err != nil {
			return err
		}
	}
	members, err := normalizedMembers(r, ids)
	if err != nil {
		return err
	}
	var encodedMembers []byte
	if len(members) > 0 {
		encodedMembers, err = json.Marshal(requestMembers{Version: requestMembersVersion, Members: members})
		if err != nil {
			return err
		}
	}
	ctx = operationContext(ctx)
	err = Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, r.Scope, true); err != nil {
			return err
		}
		old, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID))).Only(ctx)
		if err == nil {
			if len(promotionPayload) > 0 {
				equal, err := promotionMembersMatch(ctx, tx, old, promotionPayload)
				if err != nil {
					return err
				}
				if old.FreezeDigest != payloadDigest(r.FreezePayload) || old.SnapshotDigest != r.SnapshotDigest || !equal || old.BusinessBound != r.BusinessBound {
					return ErrManifest
				}
			}
			oldMembers, err := decodeRequestMembers(old.Members)
			if err != nil {
				return err
			}
			if old.JobID != r.Scope.JobID || old.JobRoundID != r.Scope.RoundID || old.ResourceID != r.Scope.ResourceID || old.RetryEpoch != r.Scope.RetryEpoch || old.InputDigest != r.InputDigest || old.Stage != r.Stage || old.BackendID != r.BackendID || old.CandidateID != r.CandidateID || old.BudgetModel != r.BudgetModel || !reflect.DeepEqual(old.SegmentIds, ids) || !reflect.DeepEqual(oldMembers, members) {
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
		rows, err := loadMembers(ctx, tx, r.Scope, ids)
		if err != nil {
			return err
		}
		candidates, err := loadRequestCandidates(ctx, tx, members)
		if err != nil {
			return err
		}
		type debitGroup struct {
			row             *ent.WorkItem
			after           attemptCounters
			main, alignment bool
			ids             []int
		}
		groups := map[string]*debitGroup{}
		var groupOrder []string
		for i, id := range ids {
			w := rows[id]
			if w.ResponseRequestID != "" {
				return ErrStale
			}
			if w.State == "resolved" {
				return ErrStale
			}
			if r.CandidateID != "" && w.CandidateID != r.CandidateID {
				return ErrCandidateVersion
			}
			alignmentAttempt := r.AlignmentAttempt
			if len(members) > 0 {
				if err := validateRequestMemberRow(w, members[i], candidates[members[i].CandidateID]); err != nil {
					return err
				}
				alignmentAttempt = members[i].NetworkAttempt == 0
			}
			if r.MainAttempt && r.MaxMainAttempts > 0 && w.MainAttempts >= r.MaxMainAttempts {
				return ErrBudget
			}
			if alignmentAttempt && r.MaxAlignmentAttempts > 0 && w.AlignmentAttempts >= r.MaxAlignmentAttempts {
				return ErrBudget
			}
			alignment := alignmentStage(r.Stage)
			if alignment {
				logicalAttempt = w.AlignmentAttempts
				if alignmentAttempt {
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
				fresh = alignmentAttempt
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
			if alignment {
				after.AlignmentNetwork = network
			} else {
				after.MainNetwork = network
			}
			if r.MainAttempt {
				after.Main++
			}
			if alignmentAttempt {
				after.Alignment++
			}
			keyData, err := json.Marshal([]any{w.State, w.CandidateID, w.PoolIndex, w.PromptPhase, before, after, r.MainAttempt, alignmentAttempt})
			if err != nil {
				return err
			}
			key := string(keyData)
			g := groups[key]
			if g == nil {
				g = &debitGroup{row: w, after: after, main: r.MainAttempt, alignment: alignmentAttempt}
				groups[key] = g
				groupOrder = append(groupOrder, key)
			}
			g.ids = append(g.ids, w.ID)
			growthBefore := false
			if alignment && w.CandidateID != "" {
				candidate, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(w.CandidateID)).Select(workcandidate.FieldID, workcandidate.FieldGrowthReserved).Only(ctx)
				if err != nil {
					return err
				}
				growthBefore = candidate.GrowthReserved
				if err := tx.WorkCandidate.UpdateOneID(candidate.ID).SetGrowthReserved(true).Exec(ctx); err != nil {
					return err
				}
			}
			debits = append(debits, requestDebit{GrowthReserved: growthBefore, SegmentID: id, Pool: w.PoolIndex, Before: before, After: after, MemberOwned: w.MemberOwned})
		}
		for _, key := range groupOrder {
			g := groups[key]
			for start := 0; start < len(g.ids); start += bulkRows {
				end := min(start+bulkRows, len(g.ids))
				p := append(expectedMember(r.Scope, g.row), workitem.IDIn(g.ids[start:end]...))
				u := tx.WorkItem.Update().Where(p...).SetNetworkAttempts(g.after.Network).SetMemberOwned(true)
				if alignmentStage(r.Stage) {
					u.SetAlignmentNetworkAttempts(g.after.AlignmentNetwork)
				} else {
					u.SetMainNetworkAttempts(g.after.MainNetwork)
				}
				if g.main {
					u.AddMainAttempts(1)
				}
				if g.alignment {
					u.AddAlignmentAttempts(1)
				}
				if err := checkedUpdate(ctx, u, end-start); err != nil {
					return err
				}
			}
		}
		encoded, err := json.Marshal(debits)
		if err != nil {
			return err
		}
		create := tx.WorkRequest.Create().SetIdentity(r.ID).SetCandidateID(r.CandidateID).SetLogicalAttempt(logicalAttempt).SetDebits(encoded).SetJobID(r.Scope.JobID).SetResourceID(r.Scope.ResourceID).SetJobRoundID(r.Scope.RoundID).SetRetryEpoch(r.Scope.RetryEpoch).SetSegmentIds(ids).SetStage(r.Stage).SetBackendID(r.BackendID).SetBudgetModel(r.BudgetModel).SetInputDigest(r.InputDigest)
		if len(encodedMembers) > 0 {
			create.SetMembers(encodedMembers)
		}
		if len(promotionPayload) > 0 {
			if err := guardExecution(ctx, tx, r.Scope.JobID); err != nil {
				return err
			}
			create.SetFreezePayload(r.FreezePayload).SetFreezeBytes(int64(len(r.FreezePayload))).SetFreezeDigest(payloadDigest(r.FreezePayload)).SetSnapshotDigest(r.SnapshotDigest).SetResponseFormatVersion(1).SetPromotionMembers(promotionPayload).SetPromotionCount(len(r.PromotionMembers)).SetPromotionStatus(make([]byte, len(r.PromotionMembers))).SetBusinessBound(r.BusinessBound)
		}
		return create.Exec(ctx)
	})
	return s.finishUnknown(ctx, r.Scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) { return reservationExists(ctx, tx, r) })
}

// RecordRequest accepts accounting for an already reserved request after
// cancellation; it never creates a candidate or changes accepted content.
func (s *Store) RecordRequest(ctx context.Context, id string, result RequestResult) error {
	return s.recordRequest(ctx, nil, id, result)
}

func (s *Store) RecordRequestForScope(ctx context.Context, scope Scope, id string, result RequestResult) error {
	return s.recordRequest(ctx, &scope, id, result)
}

func (s *Store) recordRequest(ctx context.Context, scope *Scope, id string, result RequestResult) error {
	if result.State != "sent" && result.State != "received" && result.State != "unknown" && result.State != "completed" && result.State != "failed" {
		return fmt.Errorf("invalid request result state")
	}
	jobID := 0
	var expected *ent.WorkRequest
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
		row, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
		if err != nil {
			return err
		}
		if row.JobID != jobID || (scope != nil && (row.JobRoundID != scope.RoundID || row.ResourceID != scope.ResourceID || row.RetryEpoch != scope.RetryEpoch)) {
			return ErrManifest
		}
		if row.State == "cancelled_before_dispatch" {
			return ErrStopped
		}
		expected, err = recordRequestAccountingTx(ctx, tx, row, result)
		return err
	})
	return s.finishUnknown(ctx, jobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		row, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
		if err != nil {
			return false, err
		}
		if expected == nil || row.JobID != jobID {
			return false, ErrManifest
		}
		if row.State == expected.State && row.LastError == expected.LastError && row.UsageKnown == expected.UsageKnown && row.InputTokens == expected.InputTokens && row.OutputTokens == expected.OutputTokens && row.DurationMs == expected.DurationMs && sameInt(row.UsageRecordID, expected.UsageRecordID) {
			return true, nil
		}
		return false, errUnverifiable
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

// recordRequestAccountingTx joins usage and request state to a larger durable handoff.
func recordRequestAccountingTx(ctx context.Context, tx *ent.Client, row *ent.WorkRequest, result RequestResult) (*ent.WorkRequest, error) {
	copyRow := *row
	expected := &copyRow
	u := tx.WorkRequest.Update().Where(workrequest.IDEQ(row.ID), workrequest.JobIDEQ(row.JobID), workrequest.StateEQ(row.State), workrequest.UsageKnownEQ(row.UsageKnown))
	if row.State != "completed" && row.State != "failed" && !(row.State == "received" && result.State == "sent") {
		u.SetState(result.State).SetLastError(result.LastError)
		expected.State = result.State
		expected.LastError = result.LastError
	}
	if row.UsageRecordID == nil && (result.State == "sent" || result.State == "received" || result.State == "completed" || result.State == "failed") {
		j, err := tx.Job.Query().Where(job.IDEQ(row.JobID)).WithCreatedBy().WithProject().Only(ctx)
		if err != nil {
			return nil, err
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
			return nil, err
		}
		u.SetUsageRecordID(created.ID)
		expected.UsageRecordID = &created.ID
	} else if row.UsageRecordID != nil && result.UsageKnown && !row.UsageKnown {
		if n, err := tx.UsageRecord.Update().Where(usagerecord.IDEQ(*row.UsageRecordID)).SetInputTokens(usageInt(result.InputTokens)).SetOutputTokens(usageInt(result.OutputTokens)).Save(ctx); err != nil {
			return nil, err
		} else if n != 1 {
			return nil, ErrManifest
		}
	}
	if result.UsageKnown && !row.UsageKnown {
		u.SetUsageKnown(true).SetInputTokens(result.InputTokens).SetOutputTokens(result.OutputTokens)
		expected.UsageKnown = true
		expected.InputTokens = result.InputTokens
		expected.OutputTokens = result.OutputTokens
	}
	if result.DurationMS > row.DurationMs {
		u.SetDurationMs(result.DurationMS)
		expected.DurationMs = result.DurationMS
	}
	n, err := u.Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrManifest
	}
	return expected, nil
}
