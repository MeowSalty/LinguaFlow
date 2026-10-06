package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

var (
	ErrTaskNotTerminal     = errors.New("task_not_terminal")
	ErrTaskCleanupDeferred = errors.New("task_cleanup_deferred")
	ErrTaskHistoryNotFound = errors.New("task history not found")
	errTaskNotExpired      = errors.New("task retention deadline has not elapsed")
)

const taskCleanupBudget = 5 * time.Second
const taskBatchCleanupBudget = 30 * time.Second

type HistoryTarget struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	ProjectID int    `json:"project_id"`
}

type HistoryDeleteResult struct {
	HistoryTarget
	Status string `json:"status"`
}

// TaskHistoryService owns historical deletion, not task execution or file GC.
// Its lifecycle coordinator must be the same one used by queues and controls.
type TaskHistoryService struct {
	client          *ent.Client
	projects        *ProjectService
	lifecycle       *tasklife.Coordinator
	broker          *event.Broker
	maintenance     func() bool
	ready           atomic.Bool
	stopping        atomic.Bool
	logger          *slog.Logger
	now             func() time.Time
	deleteBudget    time.Duration
	batchBudget     time.Duration
	wake            chan struct{}
	scanMu          sync.Mutex
	statusMu        sync.Mutex
	running         bool
	lastScan        *RetentionScanSummary
	lastError       string
	backlog         *bool
	traversal       *retentionTraversal
	scanLimit       int
	scanBudget      time.Duration
	previewBudget   time.Duration
	previewLimit    int
	anchorTraversal *retentionAnchorTraversal
}

func NewTaskHistoryService(client *ent.Client, projects *ProjectService, lifecycle *tasklife.Coordinator, broker *event.Broker) *TaskHistoryService {
	if lifecycle == nil {
		lifecycle = &tasklife.Coordinator{}
	}
	return &TaskHistoryService{client: client, projects: projects, lifecycle: lifecycle, broker: broker,
		logger: slog.Default(), now: func() time.Time { return time.Now().UTC() },
		deleteBudget: taskCleanupBudget, batchBudget: taskBatchCleanupBudget, wake: make(chan struct{}, 1),
		scanLimit: 1000, scanBudget: 30 * time.Second, previewBudget: 5 * time.Second, previewLimit: 10000}
}

func (s *TaskHistoryService) SetMaintenance(check func() bool) { s.maintenance = check }
func (s *TaskHistoryService) SetReady(ready bool)              { s.ready.Store(ready && !s.stopping.Load()) }
func (s *TaskHistoryService) Stop()                            { s.stopping.Store(true); s.ready.Store(false) }
func (s *TaskHistoryService) Ready() bool                      { return s.ready.Load() && !s.stopping.Load() }
func (s *TaskHistoryService) SetLogger(logger *slog.Logger) {
	if logger != nil {
		s.logger = logger
	}
}
func (s *TaskHistoryService) blocked() bool {
	return !s.Ready() || s.maintenance != nil && s.maintenance()
}

func historyTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func historyTargetID(target HistoryTarget) (int, error) {
	if target.Kind != OperationTranslation && target.Kind != OperationGlossarySync {
		return 0, ErrInvalidInput
	}
	id, err := strconv.Atoi(target.ID)
	if err != nil || id <= 0 || strconv.Itoa(id) != target.ID || target.ProjectID < 0 {
		return 0, ErrInvalidInput
	}
	return id, nil
}

type historyTask struct {
	ID         int
	ProjectID  int
	Status     string
	Anchor     *time.Time
	FinishedAt *time.Time
}

func readHistoryTask(ctx context.Context, client *ent.Client, kind string, id int) (historyTask, error) {
	var out historyTask
	var err error
	if kind == OperationTranslation {
		var row *ent.Job
		row, err = client.Job.Query().Where(job.IDEQ(id)).Select(job.FieldID, job.FieldProjectID, job.FieldStatus, job.FieldRetentionAnchorAt, job.FieldFinishedAt).Only(ctx)
		if err == nil {
			out = historyTask{row.ID, row.ProjectID, row.Status, row.RetentionAnchorAt, row.FinishedAt}
		}
	} else {
		var row *ent.SyncTask
		row, err = client.SyncTask.Query().Where(synctask.IDEQ(id)).Select(synctask.FieldID, synctask.FieldProjectID, synctask.FieldStatus, synctask.FieldRetentionAnchorAt, synctask.FieldFinishedAt).Only(ctx)
		if err == nil {
			out = historyTask{row.ID, row.ProjectID, row.Status, row.RetentionAnchorAt, row.FinishedAt}
		}
	}
	if ent.IsNotFound(err) {
		err = ErrTaskHistoryNotFound
	}
	return out, err
}

// CanDelete is advisory. Submit always rechecks current authorization and state.
func (s *TaskHistoryService) CanDelete(ctx context.Context, actor int, kind string, id, projectID int, status string) bool {
	if s == nil || kind == OperationStorage || !historyTerminal(status) || s.blocked() || s.lifecycle.Busy(kind, id) {
		return false
	}
	p, err := s.projects.requireProjectAccess(ctx, actor, projectID, true)
	return err == nil && p.StorageState == "active"
}

func (s *TaskHistoryService) Delete(ctx context.Context, actor int, target HistoryTarget) error {
	return s.delete(ctx, actor, target, nil)
}

type retentionDeletion struct {
	Policy TaskRetentionPolicy
	Cutoff time.Time
	ScanID string
}

func (s *TaskHistoryService) delete(ctx context.Context, actor int, target HistoryTarget, retention *retentionDeletion) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, s.deleteBudget)
	defer cancel()
	defer func() {
		// Drivers may report interruption without wrapping ctx.Err(). A normal
		// transaction error has already joined rollback; an uncertain outcome
		// must remain failed even when the request deadline also elapsed.
		var uncertain *historyUncertainError
		if resultErr != nil && ctx.Err() != nil && !errors.As(resultErr, &uncertain) {
			resultErr = ctx.Err()
		}
		resultErr = historyBudgetError(resultErr)
	}()
	id, err := historyTargetID(target)
	if err != nil {
		return err
	}
	initial, err := readHistoryTask(ctx, s.client, target.Kind, id)
	if err != nil {
		return err
	}
	if target.ProjectID != 0 && initial.ProjectID != target.ProjectID {
		return ErrTaskHistoryNotFound
	}
	var projectRow *ent.Project
	if retention == nil {
		projectRow, err = s.projects.requireProjectAccess(ctx, actor, initial.ProjectID, true)
	} else {
		projectRow, err = s.client.Project.Get(ctx, initial.ProjectID)
	}
	if err != nil {
		return err
	}
	guard, err := s.lifecycle.Lock(ctx, target.Kind, id)
	if err != nil {
		return historyBudgetError(err)
	}
	defer guard.Release()
	if !historyTerminal(initial.Status) {
		return ErrTaskNotTerminal
	}
	if guard.Active() {
		return tasklife.ErrBusy
	}
	if s.blocked() {
		return ErrStorageMaintenance
	}
	err = withHistoryTransaction(ctx, s.client, func(tx *ent.Client) error {
		if retention != nil {
			if err := lockTaskRetention(ctx, tx); err != nil {
				return err
			}
			policy, err := readTaskRetention(ctx, tx)
			if err != nil {
				return err
			}
			if !policy.Enabled || policy != retention.Policy {
				return ErrSettingsConflict
			}
		}
		p, err := lockHistoryProject(ctx, tx, projectRow)
		if err != nil {
			return err
		}
		if retention == nil {
			if _, err = NewProjectService(tx, NewUserService(tx, nil)).requireProjectAccess(ctx, actor, p.ID, true); err != nil {
				return err
			}
		}
		if s.blocked() || p.StorageState != "active" {
			return ErrStorageMaintenance
		}
		table := "jobs"
		if target.Kind == OperationGlossarySync {
			table = "sync_tasks"
		}
		if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET status=status WHERE id=$1", id); err != nil {
			return err
		}
		current, err := readHistoryTask(ctx, tx, target.Kind, id)
		if err != nil {
			return err
		}
		if current.ProjectID != p.ID {
			return ErrTaskHistoryNotFound
		}
		if !historyTerminal(current.Status) {
			return ErrTaskNotTerminal
		}
		if retention != nil && (current.Anchor == nil || current.Anchor.After(retention.Cutoff)) {
			return errTaskNotExpired
		}
		action, resourceType := "job.history_deleted", "job"
		if target.Kind == OperationTranslation {
			if _, err = tx.SSEEvent.Delete().Where(sseevent.JobIDEQ(id)).Exec(ctx); err != nil {
				return err
			}
			if _, err = tx.JobResource.Delete().Where(jobresource.HasJobWith(job.IDEQ(id))).Exec(ctx); err != nil {
				return err
			}
			err = tx.Job.DeleteOneID(id).Exec(ctx)
		} else {
			action, resourceType = "glossary.sync_task_history_deleted", "sync_task"
			err = tx.SyncTask.DeleteOneID(id).Exec(ctx)
		}
		if err != nil {
			return err
		}
		metadata := map[string]any{"task_kind": target.Kind, "task_id": id, "project_id": p.ID, "previous_status": current.Status, "source": "manual"}
		if retention != nil {
			metadata["source"] = "retention"
			metadata["scan_id"] = retention.ScanID
			metadata["policy_revision"] = retention.Policy.Revision
		}
		return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actor, ProjectID: &p.ID, Action: action, ResourceType: resourceType, ResourceID: id, Metadata: metadata})
	})
	if err != nil {
		var uncertain *historyUncertainError
		if errors.As(err, &uncertain) {
			// A read can establish whether the record is now absent, but never
			// makes an uncertain write safe to replay. Stay within this budget.
			state := "unknown"
			if ctx.Err() == nil {
				_, checkErr := readHistoryTask(ctx, s.client, target.Kind, id)
				if errors.Is(checkErr, ErrTaskHistoryNotFound) {
					state = "absent"
					if target.Kind == OperationTranslation && s.broker != nil {
						s.broker.CloseJob(id)
					}
				} else if checkErr == nil {
					state = "present"
				}
			}
			s.logger.Error("task history cleanup outcome requires verification", "task_kind", target.Kind, "task_id", id, "observed_state", state, "error", err)
		}
		return historyBudgetError(err)
	}
	if target.Kind == OperationTranslation && s.broker != nil {
		s.broker.CloseJob(id)
	}
	return nil
}

// No-op updates acquire database write locks without changing list timestamps.
func lockHistoryProject(ctx context.Context, tx *ent.Client, initial *ent.Project) (*ent.Project, error) {
	orgID := EffectiveProjectOrgID(initial)
	if orgID != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE organizations SET updated_at=updated_at WHERE id=$1", *orgID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE projects SET updated_at=updated_at WHERE id=$1", initial.ID); err != nil {
		return nil, err
	}
	p, err := tx.Project.Get(ctx, initial.ID)
	if err != nil {
		return nil, err
	}
	currentOrg := EffectiveProjectOrgID(p)
	if (orgID == nil) != (currentOrg == nil) || orgID != nil && *orgID != *currentOrg {
		return nil, ErrStorageConflict
	}
	return p, nil
}

type historyUncertainError struct{ cause error }

func (e *historyUncertainError) Error() string {
	return "task cleanup outcome uncertain: " + e.cause.Error()
}

// Never classify an uncertain commit or rollback as deferred or replay it.
func withHistoryTransaction(ctx context.Context, client *ent.Client, mutate func(*ent.Client) error) error {
	for attempt := 0; attempt < organizationMutationAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := historyTransactionAttempt(ctx, client, mutate)
		if err == nil {
			return nil
		}
		if !isOrganizationTransactionConflict(err) || attempt == organizationMutationAttempts-1 {
			return err
		}
		timer := time.NewTimer(time.Duration(10*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func historyTransactionAttempt(ctx context.Context, client *ent.Client, mutate func(*ent.Client) error) error {
	tx, err := client.BeginTx(database.WithJoinedRollback(ctx), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = mutate(tx.Client()); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			// No commit was attempted, and the reserved connection has joined
			// database/sql's automatic rollback before returning ErrTxDone.
			if !(ctx.Err() != nil && errors.Is(rollbackErr, stdsql.ErrTxDone)) {
				return &historyUncertainError{errors.Join(err, rollbackErr)}
			}
		}
		return err
	}
	// Only conflicts from the mutation phase with confirmed rollback may be
	// retried. A commit error is not portable proof that nothing committed.
	if err = tx.Commit(); err != nil {
		return &historyUncertainError{err}
	}
	return nil
}

func historyBudgetError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %v", ErrTaskCleanupDeferred, err)
	}
	return err
}

func historyDeleteStatus(err error) string {
	switch {
	case err == nil:
		return "deleted"
	case errors.Is(err, ErrTaskHistoryNotFound), ent.IsNotFound(err), errors.Is(err, ErrProjectNotFound):
		return "not_found"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrTaskNotTerminal):
		return "not_terminal"
	case errors.Is(err, tasklife.ErrBusy):
		return "busy"
	case errors.Is(err, ErrStorageMaintenance), errors.Is(err, ErrStorageConflict):
		return "blocked"
	case errors.Is(err, ErrTaskCleanupDeferred):
		return "deferred"
	default:
		return "failed"
	}
}

func (s *TaskHistoryService) BatchDelete(ctx context.Context, actor int, targets []HistoryTarget) []HistoryDeleteResult {
	ctx, cancel := context.WithTimeout(ctx, s.batchBudget)
	defer cancel()
	results := make([]HistoryDeleteResult, 0, len(targets))
	seen := make(map[HistoryTarget]bool, len(targets))
	stopped := false
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		status := "deferred"
		if !stopped && ctx.Err() == nil {
			err := s.Delete(ctx, actor, target)
			status = historyDeleteStatus(err)
			var uncertain *historyUncertainError
			stopped = errors.As(err, &uncertain)
		}
		results = append(results, HistoryDeleteResult{HistoryTarget: target, Status: status})
	}
	return results
}
