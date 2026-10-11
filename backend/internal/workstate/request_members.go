package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
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

func validateRequestMember(ctx context.Context, tx *ent.Client, w *ent.WorkItem, m RequestMember) error {
	if w.CandidateID != m.CandidateID || w.PoolIndex != m.Pool {
		return ErrCandidateVersion
	}
	c, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(m.CandidateID)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrCandidateVersion
	}
	if err != nil {
		return err
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
	for _, r := range rows {
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
