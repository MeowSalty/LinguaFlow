package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

const requestMembersVersion = 1

type requestMembers struct {
	Version int             `json:"version"`
	Members []RequestMember `json:"members"`
}

// WorkIdentity survives process recovery and Retry, but never aliases another
// round's candidate for the same segment.
func WorkIdentity(scope Scope, segmentID int) string {
	return fmt.Sprintf("job:%d/round:%d/segment:%d", scope.JobID, scope.RoundID, segmentID)
}

func normalizedMembers(r Request, ids []int) ([]RequestMember, error) {
	if len(r.Members) == 0 {
		return nil, nil
	}
	if !alignmentStage(r.Stage) || len(r.Members) != len(ids) {
		return nil, ErrManifest
	}
	members := append([]RequestMember(nil), r.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].SegmentID < members[j].SegmentID })
	for i, m := range members {
		if m.SegmentID != ids[i] || m.WorkID != WorkIdentity(r.Scope, m.SegmentID) || m.CandidateID == "" || m.CandidateVersion < 1 || m.Pool < 0 || m.LogicalAttempt < 0 || m.NetworkAttempt < 0 {
			return nil, ErrManifest
		}
		if i > 0 && m.Pool != members[0].Pool {
			return nil, ErrManifest
		}
		if r.CandidateID != "" && (len(members) != 1 || r.CandidateID != m.CandidateID) {
			return nil, ErrManifest
		}
	}
	return members, nil
}

func decodeRequestMembers(data json.RawMessage) ([]RequestMember, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var envelope requestMembers
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != requestMembersVersion || len(envelope.Members) == 0 {
		return nil, fmt.Errorf("%w: unsupported request members", ErrManifest)
	}
	return envelope.Members, nil
}

func alignmentStage(stage string) bool { return stage == "ruby_alignment" || stage == "alignment" }

func loadRequestCandidates(ctx context.Context, tx *ent.Client, members []RequestMember) (map[string]*ent.WorkCandidate, error) {
	result := make(map[string]*ent.WorkCandidate, len(members))
	for start := 0; start < len(members); start += bulkIDs {
		end := min(start+bulkIDs, len(members))
		ids := make([]string, 0, end-start)
		for _, m := range members[start:end] {
			ids = append(ids, m.CandidateID)
		}
		rows, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityIn(ids...)).Select(workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldWorkItemID, workcandidate.FieldVersion, workcandidate.FieldState).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			result[row.Identity] = row
		}
	}
	return result, nil
}

func validateRequestMemberRow(w *ent.WorkItem, m RequestMember, c *ent.WorkCandidate) error {
	if w.CandidateID != m.CandidateID || w.PoolIndex != m.Pool || c == nil {
		return ErrCandidateVersion
	}
	if c.WorkItemID != w.ID || c.Version != m.CandidateVersion || c.State != "pending_alignment" {
		return ErrCandidateVersion
	}
	logical := m.LogicalAttempt
	if m.NetworkAttempt > 0 {
		logical++
	}
	if w.AlignmentAttempts != logical || w.AlignmentNetworkAttempts != m.NetworkAttempt {
		return ErrCandidateVersion
	}
	return nil
}

// alignmentProof uses the exact invocation for new candidates and only falls
// back to the historical single-candidate columns for old DTOs/ledger rows.
// version is the saved response version, one beyond the invocation's version.
func alignmentProof(ctx context.Context, tx *ent.Client, scope Scope, w *ent.WorkItem, requestID, workID string, version int64, completed bool) (bool, error) {
	q := tx.WorkRequest.Query().Where(workrequest.JobIDEQ(scope.JobID), workrequest.ResourceIDEQ(scope.ResourceID), workrequest.JobRoundIDEQ(scope.RoundID), workrequest.RetryEpochEQ(scope.RetryEpoch), workrequest.StageIn("ruby_alignment", "alignment"))
	if completed {
		q.Where(workrequest.StateIn("received", "completed"))
	} else {
		q.Where(workrequest.StateIn("sent", "received", "completed", "failed"))
	}
	if requestID != "" {
		q.Where(workrequest.IdentityEQ(requestID))
	} else {
		q.Where(workrequest.CandidateIDEQ(w.CandidateID))
		if completed {
			q.Where(workrequest.LogicalAttemptEQ(w.AlignmentAttempts))
		}
	}
	rows, err := q.All(ctx)
	if err != nil {
		return false, err
	}
	return alignmentProofRows(scope, w, requestID, workID, version, completed, rows)
}

func loadCursorProofs(ctx context.Context, tx *ent.Client, scope Scope, transitions []CursorTransition, items map[int]*ent.WorkItem) ([]*ent.WorkRequest, error) {
	var requests, candidates []string
	seenRequests, seenCandidates := map[string]bool{}, map[string]bool{}
	for _, t := range transitions {
		w := items[t.SegmentID]
		if w.State == "resolved" || t.Cursor.PromptPhase != "alignment_complete" || t.Cursor.AlignmentAttempts != w.AlignmentAttempts || w.CandidateID == "" {
			continue
		}
		if t.Cursor.LastAlignmentRequestID != "" {
			if !seenRequests[t.Cursor.LastAlignmentRequestID] {
				seenRequests[t.Cursor.LastAlignmentRequestID] = true
				requests = append(requests, t.Cursor.LastAlignmentRequestID)
			}
		} else if !seenCandidates[w.CandidateID] {
			seenCandidates[w.CandidateID] = true
			candidates = append(candidates, w.CandidateID)
		}
	}
	var result []*ent.WorkRequest
	for start := 0; start < max(len(requests), len(candidates)); start += bulkIDs {
		var identity []predicate.WorkRequest
		if start < len(requests) {
			identity = append(identity, workrequest.IdentityIn(requests[start:min(start+bulkIDs, len(requests))]...))
		}
		if start < len(candidates) {
			identity = append(identity, workrequest.CandidateIDIn(candidates[start:min(start+bulkIDs, len(candidates))]...))
		}
		rows, err := tx.WorkRequest.Query().Where(workrequest.JobIDEQ(scope.JobID), workrequest.ResourceIDEQ(scope.ResourceID), workrequest.JobRoundIDEQ(scope.RoundID), workrequest.RetryEpochEQ(scope.RetryEpoch), workrequest.StageIn("ruby_alignment", "alignment"), workrequest.StateIn("received", "completed"), workrequest.Or(identity...)).All(ctx)
		if err != nil {
			return nil, err
		}
		result = append(result, rows...)
	}
	return result, nil
}

func alignmentProofRows(scope Scope, w *ent.WorkItem, requestID, workID string, version int64, completed bool, rows []*ent.WorkRequest) (bool, error) {
	for _, r := range rows {
		if requestID != "" && r.Identity != requestID {
			continue
		}
		if requestID == "" && r.CandidateID != w.CandidateID {
			continue
		}
		members, err := decodeRequestMembers(r.Members)
		if err != nil {
			return false, err
		}
		if len(members) == 0 {
			if r.CandidateID != w.CandidateID || (completed && r.LogicalAttempt != w.AlignmentAttempts) {
				continue
			}
			for _, id := range r.SegmentIds {
				if id == w.SegmentID {
					return true, nil
				}
			}
			continue
		}
		if requestID == "" || workID != WorkIdentity(scope, w.SegmentID) {
			continue
		}
		for _, m := range members {
			if m.SegmentID != w.SegmentID || m.WorkID != workID || m.CandidateID != w.CandidateID || m.CandidateVersion+1 != version || m.Pool != w.PoolIndex || m.LogicalAttempt+1 != w.AlignmentAttempts {
				continue
			}
			// An identical handoff replay may observe a cleared network cursor.
			if w.AlignmentNetworkAttempts == m.NetworkAttempt+1 || (w.AlignmentNetworkAttempts == 0 && w.PromptPhase == "alignment_complete") {
				return true, nil
			}
		}
	}
	return false, nil
}
