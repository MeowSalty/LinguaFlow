package workstate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

// These are SQL chunk limits, never transaction boundaries. One oversized JSON
// value is processed alone so batching does not reject previously valid input.
const (
	bulkIDs       = 2000
	bulkRows      = 500
	bulkJSONBytes = 1 << 20
)

type CursorTransition struct {
	SegmentID int
	Cursor    Cursor
}

func saveCursorRows(ctx context.Context, tx *ent.Client, scope Scope, transitions []CursorTransition, rows map[int]*ent.WorkItem, expected ...map[int]attemptCounters) error {
	type group struct {
		row                     *ent.WorkItem
		cursor                  Cursor
		newPool, resetAlignment bool
		ids                     []int
	}
	groups := map[string]*group{}
	var order []string
	proofs, err := loadCursorProofs(ctx, tx, scope, transitions, rows)
	if err != nil {
		return err
	}
	for _, transition := range transitions {
		row := rows[transition.SegmentID]
		cursor := transition.Cursor
		if row.State == "resolved" {
			continue
		}
		newPool := cursor.PoolIndex > row.PoolIndex
		newAlignment := cursor.AlignmentAttempts > row.AlignmentAttempts
		completed := false
		if cursor.PromptPhase == "alignment_complete" && cursor.AlignmentAttempts == row.AlignmentAttempts && row.CandidateID != "" {
			completed, err = alignmentProofRows(scope, row, cursor.LastAlignmentRequestID, cursor.WorkID, cursor.CandidateVersion, true, proofs)
			if err != nil {
				return err
			}
		}
		if cursor.PoolIndex < row.PoolIndex || (!newPool && (cursor.MainAttempts < row.MainAttempts || cursor.AlignmentAttempts < row.AlignmentAttempts)) || (!newPool && !newAlignment && !completed && cursor.NetworkAttempts < row.NetworkAttempts) {
			return ErrStale
		}
		if len(expected) > 0 {
			after := attemptsFromRow(row)
			after.Main = cursor.MainAttempts
			after.Alignment = cursor.AlignmentAttempts
			after.Network = cursor.NetworkAttempts
			if newPool {
				after.MainNetwork = 0
				after.AlignmentNetwork = 0
			} else if newAlignment || completed {
				after.AlignmentNetwork = 0
			}
			expected[0][transition.SegmentID] = after
		}
		keyBytes, err := json.Marshal([]any{row.State, row.CandidateID, row.PoolIndex, row.PromptPhase, attemptsFromRow(row), cursor, newPool, newAlignment || completed})
		if err != nil {
			return err
		}
		key := string(keyBytes)
		g := groups[key]
		if g == nil {
			g = &group{row: row, cursor: cursor, newPool: newPool, resetAlignment: newAlignment || completed}
			groups[key] = g
			order = append(order, key)
		}
		g.ids = append(g.ids, row.ID)
	}
	for _, key := range order {
		g := groups[key]
		// Cursor JSON is a shared UPDATE value, not one payload per member.
		// Oversized single values still execute normally in the same transaction.
		limit := bulkRows
		if len(g.cursor.Data) > bulkJSONBytes {
			limit = 1
		}
		for start := 0; start < len(g.ids); start += limit {
			end := min(start+limit, len(g.ids))
			u := tx.WorkItem.Update().Where(append(expectedMember(scope, g.row), workitem.IDIn(g.ids[start:end]...))...)
			c := g.cursor
			u.SetPoolIndex(c.PoolIndex).SetMainAttempts(c.MainAttempts).SetAlignmentAttempts(c.AlignmentAttempts).SetNetworkAttempts(c.NetworkAttempts).SetLastError(c.LastError)
			if g.newPool {
				u.SetMainNetworkAttempts(0).SetAlignmentNetworkAttempts(0)
			} else if g.resetAlignment {
				u.SetAlignmentNetworkAttempts(0)
			}
			if c.State != "" {
				u.SetState(c.State)
			}
			if c.PromptPhase != "" {
				u.SetPromptPhase(c.PromptPhase)
			}
			if c.NextAttemptAt == nil {
				u.ClearNextAttemptAt()
			} else {
				u.SetNextAttemptAt(*c.NextAttemptAt)
			}
			if len(c.Data) > 0 {
				u.SetCursor(c.Data)
			}
			if err := checkedUpdate(ctx, u, end-start); err != nil {
				return err
			}
			if c.State == "rejected" || c.State == "stale" || c.State == "unresolved" {
				if _, err := tx.WorkCandidate.Update().Where(workcandidate.WorkItemIDIn(g.ids[start:end]...)).SetState(c.State).ClearPayload().SetPayloadBytes(0).Save(ctx); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func memberScope(scope Scope) []predicate.WorkItem {
	return []predicate.WorkItem{workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID), workitem.JobRoundIDEQ(scope.RoundID)}
}

func loadMembers(ctx context.Context, client *ent.Client, scope Scope, ids []int) (map[int]*ent.WorkItem, error) {
	return queryMemberRows(ctx, client, scope, ids, true)
}
func queryMemberRows(ctx context.Context, client *ent.Client, scope Scope, ids []int, strict bool, withCursor ...bool) (map[int]*ent.WorkItem, error) {
	fields := []string{workitem.FieldID, workitem.FieldJobID, workitem.FieldResourceID, workitem.FieldJobRoundID, workitem.FieldSegmentID, workitem.FieldRetryEpoch, workitem.FieldState, workitem.FieldCandidateID, workitem.FieldPoolIndex, workitem.FieldMainAttempts, workitem.FieldAlignmentAttempts, workitem.FieldNetworkAttempts, workitem.FieldMainNetworkAttempts, workitem.FieldAlignmentNetworkAttempts, workitem.FieldPromptPhase, workitem.FieldNextAttemptAt, workitem.FieldLastError}
	if len(withCursor) > 0 && withCursor[0] {
		fields = append(fields, workitem.FieldCursor)
	}
	rows := make(map[int]*ent.WorkItem, len(ids))
	for start := 0; start < len(ids); start += bulkIDs {
		end := min(start+bulkIDs, len(ids))
		p := append(memberScope(scope), workitem.SegmentIDIn(ids[start:end]...))
		chunk, err := client.WorkItem.Query().Where(p...).Select(fields...).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range chunk {
			if _, duplicate := rows[row.SegmentID]; duplicate {
				return nil, ErrManifest
			}
			if strict && row.RetryEpoch != scope.RetryEpoch {
				return nil, ErrStopped
			}
			rows[row.SegmentID] = row
		}
	}
	if strict {
		for _, id := range ids {
			if rows[id] == nil {
				return nil, ErrManifest
			}
		}
	}
	return rows, nil
}

func (s *Store) LoadCursors(ctx context.Context, scope Scope, ids []int) (map[int]Cursor, error) {
	rows, err := queryMemberRows(ctx, s.client, scope, ids, true, true)
	if err != nil {
		return nil, err
	}
	result := make(map[int]Cursor, len(rows))
	for id, row := range rows {
		result[id] = cursorFromRow(row)
	}
	return result, nil
}

// expectedMember protects exactly the fields used to validate a transition.
// Job/resource locks remain the authority for epoch, source and lifecycle checks.
func expectedMember(scope Scope, row *ent.WorkItem) []predicate.WorkItem {
	p := memberScope(scope)
	return append(p, workitem.RetryEpochEQ(row.RetryEpoch), workitem.StateEQ(row.State), workitem.CandidateIDEQ(row.CandidateID), workitem.PoolIndexEQ(row.PoolIndex),
		workitem.MainAttemptsEQ(row.MainAttempts), workitem.AlignmentAttemptsEQ(row.AlignmentAttempts), workitem.NetworkAttemptsEQ(row.NetworkAttempts),
		workitem.MainNetworkAttemptsEQ(row.MainNetworkAttempts), workitem.AlignmentNetworkAttemptsEQ(row.AlignmentNetworkAttempts), workitem.PromptPhaseEQ(row.PromptPhase))
}

func checkedUpdate(ctx context.Context, update *ent.WorkItemUpdate, expected int) error {
	n, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if n != expected {
		return fmt.Errorf("%w: work update matched %d, expected %d", ErrStale, n, expected)
	}
	return nil
}

func (s *Store) SaveCursors(ctx context.Context, scope Scope, transitions []CursorTransition) error {
	if len(transitions) == 0 {
		return nil
	}
	transitions = append([]CursorTransition(nil), transitions...)
	sort.Slice(transitions, func(i, j int) bool { return transitions[i].SegmentID < transitions[j].SegmentID })
	ids := make([]int, len(transitions))
	for i, transition := range transitions {
		if transition.SegmentID < 1 || (i > 0 && transitions[i-1].SegmentID == transition.SegmentID) {
			return ErrManifest
		}
		ids[i] = transition.SegmentID
	}
	ctx = operationContext(ctx)
	expected := map[int]attemptCounters{}
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, scope, false); err != nil {
			return err
		}
		rows, err := loadMembers(ctx, tx, scope, ids)
		if err != nil {
			return err
		}
		return saveCursorRows(ctx, tx, scope, transitions, rows, expected)
	})
	return s.finishUnknown(ctx, scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		rows, err := queryMemberRows(ctx, tx, scope, ids, true, true)
		if err != nil {
			return false, err
		}
		for _, t := range transitions {
			row := rows[t.SegmentID]
			if !cursorSaved(row, t.Cursor) || row.State != "resolved" && attemptsFromRow(row) != expected[t.SegmentID] {
				if row.PoolIndex < t.Cursor.PoolIndex || row.PoolIndex == t.Cursor.PoolIndex && (row.MainAttempts < t.Cursor.MainAttempts || row.AlignmentAttempts < t.Cursor.AlignmentAttempts) {
					return false, nil
				}
				return false, errUnverifiable
			}
		}
		return true, nil
	})
}

func cursorSaved(row *ent.WorkItem, c Cursor) bool {
	if row.State == "resolved" {
		return true
	}
	if row.PoolIndex != c.PoolIndex || row.MainAttempts != c.MainAttempts || row.AlignmentAttempts != c.AlignmentAttempts || row.NetworkAttempts != c.NetworkAttempts || row.LastError != c.LastError {
		return false
	}
	if c.State != "" && row.State != c.State || c.PromptPhase != "" && row.PromptPhase != c.PromptPhase || len(c.Data) > 0 && string(row.Cursor) != string(c.Data) {
		return false
	}
	return row.NextAttemptAt == nil && c.NextAttemptAt == nil || row.NextAttemptAt != nil && c.NextAttemptAt != nil && row.NextAttemptAt.Equal(*c.NextAttemptAt)
}
