package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
)

var retentionStatuses = []string{"completed", "failed", "cancelled"}

type RetentionScanSummary struct {
	ScanID             string         `json:"scan_id"`
	PolicyRevision     int64          `json:"policy_revision"`
	AsOf               time.Time      `json:"as_of"`
	StartedAt          time.Time      `json:"started_at"`
	FinishedAt         time.Time      `json:"finished_at"`
	DurationMS         int64          `json:"duration_ms"`
	Candidates         int            `json:"candidates"`
	Deleted            int            `json:"deleted"`
	Skipped            map[string]int `json:"skipped"`
	ErrorCode          string         `json:"error_code"`
	Completed          bool           `json:"completed"`
	LegacyAnchorCount  *int64         `json:"legacy_anchor_count"`
	MissingAnchorCount *int64         `json:"missing_anchor_count"`
}
type RetentionStatus struct {
	TaskRetention  TaskRetentionPolicy   `json:"task_retention"`
	PolicyRevision int64                 `json:"policy_revision"`
	State          string                `json:"state"`
	ReasonCodes    []string              `json:"reason_codes"`
	LastScan       *RetentionScanSummary `json:"last_scan"`
	Backlog        *bool                 `json:"backlog"`
}
type retentionCursor struct {
	Anchor time.Time
	ID     int
}
type retentionPartition struct {
	Kind      string
	Status    string
	UpperID   int
	Cursor    retentionCursor
	Page      []historyTask
	Exhausted bool
}
type retentionTraversal struct {
	Policy     TaskRetentionPolicy
	AsOf       time.Time
	Cutoff     time.Time
	Partitions []retentionPartition
	Next       int
}

type retentionAnchorPosition struct {
	Kind         string
	After, Upper int
	Done         bool
}
type retentionAnchorTraversal struct {
	Positions []retentionAnchorPosition
	Next      int
}

// Recurring legacy maintenance has its own finite domain and cursor. Busy old
// records cannot consume every cleanup round or starve already anchored tasks.
func (s *TaskHistoryService) advanceHistoryAnchors(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	if s.anchorTraversal == nil {
		t := &retentionAnchorTraversal{}
		for _, kind := range []string{OperationTranslation, OperationGlossarySync} {
			var ids []int
			var err error
			if kind == OperationTranslation {
				ids, err = s.client.Job.Query().Order(ent.Desc(job.FieldID)).Limit(1).IDs(ctx)
			} else {
				ids, err = s.client.SyncTask.Query().Order(ent.Desc(synctask.FieldID)).Limit(1).IDs(ctx)
			}
			if err != nil {
				return err
			}
			p := retentionAnchorPosition{Kind: kind, Done: len(ids) == 0}
			if len(ids) > 0 {
				p.Upper = ids[0]
			}
			t.Positions = append(t.Positions, p)
		}
		s.anchorTraversal = t
	}
	for visited := 0; visited < 100; {
		if ctx.Err() != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			return nil
		}
		t := s.anchorTraversal
		if t.Positions[0].Done && t.Positions[1].Done {
			s.anchorTraversal = nil
			return nil
		}
		p := &t.Positions[t.Next]
		t.Next = (t.Next + 1) % len(t.Positions)
		if p.Done {
			continue
		}
		var ids []int
		var err error
		if p.Kind == OperationTranslation {
			ids, err = s.client.Job.Query().Where(job.IDGT(p.After), job.IDLTE(p.Upper), job.StatusIn(retentionStatuses...), job.RetentionAnchorAtIsNil()).Order(ent.Asc(job.FieldID)).Limit(1).IDs(ctx)
		} else {
			ids, err = s.client.SyncTask.Query().Where(synctask.IDGT(p.After), synctask.IDLTE(p.Upper), synctask.StatusIn(retentionStatuses...), synctask.RetentionAnchorAtIsNil()).Order(ent.Asc(synctask.FieldID)).Limit(1).IDs(ctx)
		}
		if err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return nil
			}
			return err
		}
		if len(ids) == 0 {
			p.Done = true
			continue
		}
		visited++
		p.After = ids[0]
		if err = s.initializeHistoryAnchor(ctx, p.Kind, p.After); err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return nil
			}
			return err
		}
	}
	return nil
}

func (s *TaskHistoryService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Prepare is called after recovery preparation, before workers are admitted.
// Busy legacy rows are retried on subsequent scans even if retention is disabled.
func (s *TaskHistoryService) Prepare(ctx context.Context) error {
	if err := s.initializeHistoryAnchors(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.SetReady(true)
	s.Wake()
	return nil
}

func (s *TaskHistoryService) initializeHistoryAnchors(ctx context.Context) error {
	for _, kind := range []string{OperationTranslation, OperationGlossarySync} {
		after := 0
		for {
			var ids []int
			var err error
			if kind == OperationTranslation {
				ids, err = s.client.Job.Query().Where(job.IDGT(after), job.StatusIn(retentionStatuses...), job.RetentionAnchorAtIsNil()).Order(ent.Asc(job.FieldID)).Limit(100).IDs(ctx)
			} else {
				ids, err = s.client.SyncTask.Query().Where(synctask.IDGT(after), synctask.StatusIn(retentionStatuses...), synctask.RetentionAnchorAtIsNil()).Order(ent.Asc(synctask.FieldID)).Limit(100).IDs(ctx)
			}
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for _, id := range ids {
				after = id
				if err = s.initializeHistoryAnchor(ctx, kind, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *TaskHistoryService) initializeHistoryAnchor(ctx context.Context, kind string, id int) error {
	guard, err := s.lifecycle.Lock(ctx, kind, id)
	if err != nil {
		return err
	}
	defer guard.Release()
	if guard.Active() {
		return nil
	}
	// Conditional UPDATE is idempotent and does not change list timestamps.
	table := "jobs"
	if kind == OperationGlossarySync {
		table = "sync_tasks"
	}
	_, err = s.client.ExecContext(ctx, "UPDATE "+table+" SET retention_anchor_at=COALESCE(finished_at,$1) WHERE id=$2 AND retention_anchor_at IS NULL AND status IN ('completed','failed','cancelled')", s.now().UTC(), id)
	return err
}

func (s *TaskHistoryService) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	s.Wake()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		if err := s.Scan(ctx); err != nil && ctx.Err() == nil {
			s.logger.Error("task retention scan stopped", "error", err)
		}
	}
}

func (s *TaskHistoryService) Status(ctx context.Context) (RetentionStatus, error) {
	policy, err := readTaskRetention(ctx, s.client)
	if err != nil {
		return RetentionStatus{}, err
	}
	out := RetentionStatus{TaskRetention: policy, PolicyRevision: policy.Revision, State: "idle", ReasonCodes: []string{}}
	s.statusMu.Lock()
	if s.lastScan != nil {
		copy := *s.lastScan
		copy.Skipped = make(map[string]int, len(s.lastScan.Skipped))
		for k, v := range s.lastScan.Skipped {
			copy.Skipped[k] = v
		}
		out.LastScan = &copy
	}
	if s.backlog != nil {
		value := *s.backlog
		out.Backlog = &value
	}
	running := s.running
	lastError := s.lastError
	s.statusMu.Unlock()
	switch {
	case !policy.Enabled:
		out.State = "disabled"
	case s.maintenance != nil && s.maintenance():
		out.State = "blocked"
		out.ReasonCodes = append(out.ReasonCodes, "storage_maintenance")
	case !s.Ready():
		out.State = "blocked"
		out.ReasonCodes = append(out.ReasonCodes, "task_recovery_pending")
	case running:
		out.State = "running"
	case lastError != "":
		out.State = "error"
		out.ReasonCodes = append(out.ReasonCodes, lastError)
	}
	return out, nil
}

func (s *TaskHistoryService) newTraversal(ctx context.Context, policy TaskRetentionPolicy) (*retentionTraversal, error) {
	now := s.now().UTC()
	t := &retentionTraversal{Policy: policy, AsOf: now, Cutoff: now.Add(-time.Duration(policy.RetentionDays) * 24 * time.Hour)}
	limits := map[string]int{}
	for _, kind := range []string{OperationTranslation, OperationGlossarySync} {
		var ids []int
		var err error
		if kind == OperationTranslation {
			ids, err = s.client.Job.Query().Order(ent.Desc(job.FieldID)).Limit(1).IDs(ctx)
		} else {
			ids, err = s.client.SyncTask.Query().Order(ent.Desc(synctask.FieldID)).Limit(1).IDs(ctx)
		}
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			limits[kind] = ids[0]
		}
	}
	// Interleave both type and status, not entire pages of a single type.
	for _, status := range retentionStatuses {
		for _, kind := range []string{OperationTranslation, OperationGlossarySync} {
			t.Partitions = append(t.Partitions, retentionPartition{Kind: kind, Status: status, UpperID: limits[kind], Exhausted: limits[kind] == 0})
		}
	}
	return t, nil
}

func (t *retentionTraversal) nextCandidate(ctx context.Context, client *ent.Client) (historyTask, string, bool, error) {
	for checked := 0; checked < len(t.Partitions); checked++ {
		index := t.Next
		t.Next = (t.Next + 1) % len(t.Partitions)
		p := &t.Partitions[index]
		if p.Exhausted {
			continue
		}
		if len(p.Page) == 0 {
			rows, err := queryRetentionCandidates(ctx, client, p.Kind, p.Status, t.Cutoff, p.UpperID, p.Cursor, 100)
			if err != nil {
				return historyTask{}, "", false, err
			}
			if len(rows) == 0 {
				p.Exhausted = true
				continue
			}
			p.Page = rows
		}
		candidate := p.Page[0]
		p.Page = p.Page[1:]
		p.Cursor = retentionCursor{Anchor: *candidate.Anchor, ID: candidate.ID}
		return candidate, p.Kind, true, nil
	}
	return historyTask{}, "", false, nil
}

func (s *TaskHistoryService) Scan(parent context.Context) error {
	if !s.scanMu.TryLock() {
		return nil
	}
	defer s.scanMu.Unlock()
	started := time.Now()
	startedAt := s.now().UTC()
	ctx, cancel := context.WithTimeout(parent, s.scanBudget)
	defer cancel()
	policy, err := readTaskRetention(ctx, s.client)
	if err != nil {
		s.recordScanFailure("settings_unavailable")
		return err
	}
	if s.traversal != nil && s.traversal.Policy != policy {
		s.traversal = nil
	}
	if s.blocked() {
		return nil
	}
	if err = s.advanceHistoryAnchors(ctx); err != nil {
		s.recordScanFailure("anchor_initialization_failed")
		return err
	}
	if !policy.Enabled {
		return nil
	}
	if s.traversal == nil {
		s.traversal, err = s.newTraversal(ctx, policy)
		if err != nil {
			s.recordScanFailure("candidate_query_failed")
			return err
		}
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	summary := RetentionScanSummary{ScanID: hex.EncodeToString(token[:]), PolicyRevision: policy.Revision, AsOf: s.traversal.AsOf, StartedAt: startedAt, Skipped: map[string]int{}}
	s.statusMu.Lock()
	s.running = true
	s.statusMu.Unlock()
	defer func() {
		backlog, remainingErr := s.remainingExpired(ctx, summary.AsOf.Add(-time.Duration(policy.RetentionDays)*24*time.Hour))
		summary.LegacyAnchorCount, summary.MissingAnchorCount = s.historyAnchorCounts(ctx)
		summary.FinishedAt = s.now().UTC()
		summary.DurationMS = time.Since(started).Milliseconds()
		var backlogValue *bool
		if remainingErr == nil {
			backlogValue = &backlog
		}
		var loggedBacklog any
		if backlogValue != nil {
			loggedBacklog = *backlogValue
		}
		s.statusMu.Lock()
		s.running = false
		s.lastScan = &summary
		s.lastError = summary.ErrorCode
		s.backlog = backlogValue
		s.statusMu.Unlock()
		var legacyCount, missingCount any
		if summary.LegacyAnchorCount != nil {
			legacyCount = *summary.LegacyAnchorCount
		}
		if summary.MissingAnchorCount != nil {
			missingCount = *summary.MissingAnchorCount
		}
		s.logger.Info("task retention scan", "scan_id", summary.ScanID, "policy_revision", summary.PolicyRevision, "duration_ms", summary.DurationMS, "candidates", summary.Candidates, "deleted", summary.Deleted, "skipped", summary.Skipped, "completed", summary.Completed, "backlog", loggedBacklog, "error_code", summary.ErrorCode, "legacy_anchor_count", legacyCount, "missing_anchor_count", missingCount)
	}()
	for summary.Candidates < s.scanLimit {
		if ctx.Err() != nil {
			return nil
		}
		candidate, kind, ok, err := s.traversal.nextCandidate(ctx, s.client)
		if err != nil {
			if ctx.Err() != nil && parent.Err() == nil {
				return nil
			}
			summary.ErrorCode = "candidate_query_failed"
			return err
		}
		if !ok {
			summary.Completed = true
			s.traversal = nil
			return nil
		}
		summary.Candidates++
		err = s.delete(ctx, 0, HistoryTarget{Kind: kind, ID: strconv.Itoa(candidate.ID), ProjectID: candidate.ProjectID}, &retentionDeletion{Policy: policy, Cutoff: s.traversal.Cutoff, ScanID: summary.ScanID})
		if err == nil {
			summary.Deleted++
			continue
		}
		if errors.Is(err, ErrSettingsConflict) {
			s.traversal = nil
			summary.Skipped["policy_changed"]++
			s.Wake()
			return nil
		}
		if errors.Is(err, errTaskNotExpired) {
			summary.Skipped["not_expired"]++
			continue
		}
		status := historyDeleteStatus(err)
		summary.Skipped[status]++
		if status == "failed" {
			summary.ErrorCode = "task_cleanup_failed"
			return err
		}
	}
	return nil
}

func (s *TaskHistoryService) historyAnchorCounts(ctx context.Context) (*int64, *int64) {
	var legacy, missing int64
	for _, table := range []string{"jobs", "sync_tasks"} {
		rows, err := s.client.QueryContext(ctx, "SELECT COALESCE(SUM(CASE WHEN finished_at IS NULL AND retention_anchor_at IS NOT NULL THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN retention_anchor_at IS NULL THEN 1 ELSE 0 END),0) FROM "+table+" WHERE status IN ('completed','failed','cancelled')")
		if err != nil {
			return nil, nil
		}
		var l, m int64
		if !rows.Next() {
			rows.Close()
			return nil, nil
		}
		err = rows.Scan(&l, &m)
		if rowErr := rows.Err(); err == nil {
			err = rowErr
		}
		closeErr := rows.Close()
		if err != nil || closeErr != nil {
			return nil, nil
		}
		legacy += l
		missing += m
	}
	return &legacy, &missing
}

func (s *TaskHistoryService) remainingExpired(ctx context.Context, cutoff time.Time) (bool, error) {
	exists, err := s.client.Job.Query().Where(job.StatusIn(retentionStatuses...), job.RetentionAnchorAtLTE(cutoff)).Exist(ctx)
	if err != nil || exists {
		return exists, err
	}
	return s.client.SyncTask.Query().Where(synctask.StatusIn(retentionStatuses...), synctask.RetentionAnchorAtLTE(cutoff)).Exist(ctx)
}

func (s *TaskHistoryService) recordScanFailure(code string) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	// A preflight failure has no evaluated domain or known policy revision.
	// Preserve the last actual scan instead of fabricating a zero-valued one.
	s.lastError = code
	s.backlog = nil
}

func queryRetentionCandidates(ctx context.Context, client *ent.Client, kind, status string, cutoff time.Time, upperID int, cursor retentionCursor, limit int) ([]historyTask, error) {
	result := []historyTask{}
	if kind == OperationTranslation {
		q := client.Job.Query().Where(job.StatusEQ(status), job.RetentionAnchorAtLTE(cutoff))
		if upperID > 0 {
			q.Where(job.IDLTE(upperID))
		}
		if cursor.ID > 0 {
			q.Where(job.Or(job.RetentionAnchorAtGT(cursor.Anchor), job.And(job.RetentionAnchorAtEQ(cursor.Anchor), job.IDGT(cursor.ID))))
		}
		rows, err := q.Select(job.FieldID, job.FieldProjectID, job.FieldStatus, job.FieldRetentionAnchorAt, job.FieldFinishedAt).Order(ent.Asc(job.FieldRetentionAnchorAt), ent.Asc(job.FieldID)).Limit(limit).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			result = append(result, historyTask{row.ID, row.ProjectID, row.Status, row.RetentionAnchorAt, row.FinishedAt})
		}
	} else {
		q := client.SyncTask.Query().Where(synctask.StatusEQ(status), synctask.RetentionAnchorAtLTE(cutoff))
		if upperID > 0 {
			q.Where(synctask.IDLTE(upperID))
		}
		if cursor.ID > 0 {
			q.Where(synctask.Or(synctask.RetentionAnchorAtGT(cursor.Anchor), synctask.And(synctask.RetentionAnchorAtEQ(cursor.Anchor), synctask.IDGT(cursor.ID))))
		}
		rows, err := q.Select(synctask.FieldID, synctask.FieldProjectID, synctask.FieldStatus, synctask.FieldRetentionAnchorAt, synctask.FieldFinishedAt).Order(ent.Asc(synctask.FieldRetentionAnchorAt), ent.Asc(synctask.FieldID)).Limit(limit).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			result = append(result, historyTask{row.ID, row.ProjectID, row.Status, row.RetentionAnchorAt, row.FinishedAt})
		}
	}
	return result, nil
}
