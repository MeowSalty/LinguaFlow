package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func (s *StorageService) Configure(cfg config.StorageConfig, dialect string, defaultSpace int) {
	s.cfg = cfg
	s.dialect = dialect
	s.maintenance = cfg.Maintenance
	s.maxFileBytes = cfg.Limits.MaxFileBytes
	s.maxTempBytes = cfg.Limits.MaxTempBytes
	s.slots = make(chan struct{}, cfg.Limits.MaxConcurrency)
	s.ingressSlots = make(chan struct{}, cfg.Limits.MaxConcurrency)
	s.deleteGrace = cfg.DeletionGrace
	s.sourceRetention = cfg.SourceRetention
	s.defaultSpaceID = defaultSpace
}
func (s *StorageService) Task(ctx context.Context, actor, projectID, id int) (*ent.StorageTask, error) {
	return s.task(ctx, actor, projectID, id)
}
func (s *StorageService) ListTasks(ctx context.Context, actor, projectID int) ([]*ent.StorageTask, error) {
	if _, e := s.projects.requireProjectAccess(ctx, actor, projectID, false); e != nil {
		return nil, e
	}
	return s.client.StorageTask.Query().Where(storagetask.ProjectIDEQ(projectID)).Order(ent.Desc(storagetask.FieldID)).Limit(100).All(ctx)
}
func (s *StorageService) SpaceForProject(ctx context.Context, actor, projectID int) (*ent.StorageSpace, error) {
	p, e := s.projects.requireProjectAccess(ctx, actor, projectID, false)
	if e != nil {
		return nil, e
	}
	if p.StorageSpaceID == nil {
		return nil, ErrStoragePolicy
	}
	return s.client.StorageSpace.Get(ctx, *p.StorageSpaceID)
}

func (s *StorageService) Receive(ctx context.Context, actor, projectID, id int, r io.Reader, size int64) (*ent.StorageTask, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.TransferTimeout)
	defer cancel()
	if _, e := s.projects.requireProjectAccess(ctx, actor, projectID, true); e != nil {
		return nil, e
	}
	task, e := s.task(ctx, actor, projectID, id)
	if e != nil {
		return nil, e
	}
	if task.Kind != "upload" && task.Kind != "repair" && task.Kind != "source_update" {
		return nil, ErrInvalidInput
	}
	if task.Phase == "committed" {
		if e = s.verifyTaskContent(ctx, task, r, size); e != nil {
			return nil, e
		}
		return task, nil
	}
	if e = storageTerminalError(task); e != nil {
		return nil, e
	}
	if task.Phase == "prepared" {
		if e = s.verifyTaskContent(ctx, task, r, size); e != nil {
			return nil, e
		}
		return task, nil
	}
	release, e := s.ClaimTaskExecution(ctx, id)
	if e != nil {
		return nil, &StorageOperationError{Err: e, TaskID: id}
	}
	defer release()
	task, e = s.client.StorageTask.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	if task.Phase == "committed" {
		if e = s.verifyTaskContent(ctx, task, r, size); e != nil {
			return nil, e
		}
		return task, nil
	}
	if e = storageTerminalError(task); e != nil {
		return nil, e
	}
	if task.Phase == "prepared" {
		if e = s.verifyTaskContent(ctx, task, r, size); e != nil {
			return nil, e
		}
		return task, nil
	}
	if task.Kind == "source_update" && task.Phase == "parsing" {
		if e = s.verifyTaskContent(ctx, task, r, size); e != nil {
			return nil, e
		}
		if e = s.resources.resumeSourcePlan(ctx, task); e != nil {
			return nil, e
		}
		return s.task(ctx, actor, projectID, id)
	}
	encoded, e := json.Marshal(task.Input)
	if e != nil {
		return nil, e
	}
	var intent StorageIntent
	if e = json.Unmarshal(encoded, &intent); e != nil {
		return nil, e
	}
	if size != intent.Size {
		return nil, ErrRepairMismatch
	}
	stage, e := s.Stage(ctx, task, r, size)
	if e != nil {
		return nil, e
	}
	if task.Kind == "source_update" {
		defer stage.Close()
		if s.resources == nil || task.ResourceID == nil {
			return nil, ErrInvalidInput
		}
		res, e := s.client.Resource.Get(ctx, *task.ResourceID)
		if e != nil {
			return nil, e
		}
		items, e := s.resources.parseStoredSegments(ctx, stage.File, res.Format)
		if e != nil {
			_ = s.failWrite(context.WithoutCancel(ctx), stage.Write.ID, e)
			return nil, e
		}
		if e = s.resources.prepareSourcePlan(ctx, task, items); e != nil {
			return nil, e
		}
		return s.task(ctx, actor, projectID, id)
	}
	if e = stage.Close(); e != nil {
		return nil, e
	}
	return s.task(ctx, actor, projectID, id)
}

func StorageAllowedActions(t *ent.StorageTask) []string {
	if t == nil || !interactiveStorageTask(t.Kind) {
		return []string{}
	}
	if storageTerminalError(t) != nil || t.Phase == "invalidated" {
		return []string{}
	}
	if t.Phase == "committed" || t.Status == storagetask.StatusCompleted {
		return []string{}
	}
	if t.Status == storagetask.StatusCancelled {
		return []string{}
	}
	actions := []string{}
	if t.Kind != "migration" || t.Phase != "cutover" && t.Phase != "cleanup" {
		actions = append(actions, "cancel")
	}
	if t.Status == storagetask.StatusNeedsAction || t.Status == storagetask.StatusFailed {
		actions = append(actions, "retry")
	}
	if t.Phase == "accepted" || t.Phase == "cleaned" {
		if t.Kind == "upload" || t.Kind == "repair" || t.Kind == "source_update" {
			actions = append(actions, "upload_content")
		}
	}
	if t.Kind == "source_update" && t.Phase == "prepared" && len(t.SourcePlan) > 0 {
		actions = append(actions, "commit")
	}
	return actions
}

func (s *StorageService) Retry(ctx context.Context, actor, projectID, id int) (*ent.StorageTask, error) {
	task, e := s.task(ctx, actor, projectID, id)
	if e != nil {
		return nil, e
	}
	if e = storageTerminalError(task); e != nil {
		return nil, e
	}
	if !interactiveStorageTask(task.Kind) {
		return nil, storage.ErrUnsupported
	}
	if task.Kind == "migration" {
		if e = s.projects.mutateProject(ctx, actor, projectID, func(*ent.Client, *ent.Project) error { return nil }); e != nil {
			return nil, e
		}
	} else if _, e = s.projects.requireProjectAccess(ctx, actor, projectID, true); e != nil {
		return nil, e
	}
	if task.Status != storagetask.StatusNeedsAction && task.Status != storagetask.StatusFailed && task.Status != storagetask.StatusWaitingRetry {
		return nil, ErrStorageConflict
	}
	writes, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(id), storagewrite.PhaseNotIn("committed", "cleaned", "prepared")).Exist(ctx)
	if e != nil {
		return nil, e
	}
	if writes {
		return nil, ErrStorageConflict
	}
	update := s.client.StorageTask.Update().Where(storagetask.IDEQ(id), storagetask.StatusEQ(task.Status), storagetask.PhaseEQ(task.Phase)).SetStatus(storagetask.StatusPending).SetErrorCode("").ClearNextRetryAt().ClearRetryStartedAt().SetAttempts(0)
	prepared, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(id), storagewrite.PhaseEQ("prepared")).Exist(ctx)
	if e != nil {
		return nil, e
	}
	if prepared {
		if task.Kind != "migration" {
			update.SetPhase("prepared")
		}
	} else if task.Kind == "upload" || task.Kind == "repair" || task.Kind == "source_update" {
		update.SetPhase("accepted")
	}
	n, e := update.Save(ctx)
	if e != nil {
		return nil, e
	}
	if n != 1 {
		return nil, ErrStorageConflict
	}
	return s.task(ctx, actor, projectID, id)
}

func (s *StorageService) ProcessTasks(ctx context.Context) error {
	if err := s.expireTasks(ctx); err != nil {
		return err
	}
	if s.maintenance || s.resources == nil {
		return nil
	}
	preparedExport := func(selector *sql.Selector) {
		w := sql.Table(storagewrite.Table)
		selector.Where(sql.In(selector.C(storagetask.FieldID), sql.Select(w.C(storagewrite.FieldTaskID)).From(w).Where(sql.EQ(w.C(storagewrite.FieldPhase), "prepared"))))
	}
	tasks, err := s.client.StorageTask.Query().Where(
		storagetask.ProjectIDGT(0),
		storagetask.Or(storagetask.KindEQ("migration"),
			storagetask.And(storagetask.KindEQ("source_update"), storagetask.PhaseEQ("parsing")),
			storagetask.And(storagetask.KindIn("upload", "repair"), storagetask.PhaseEQ("prepared")),
			storagetask.And(storagetask.KindEQ("export"), storagetask.Or(storagetask.ResultArtifactIDNotNil(), preparedExport))),
		storagetask.Or(storagetask.NextRetryAtIsNil(), storagetask.NextRetryAtLTE(time.Now().UTC())),
		storagetask.StatusIn(storagetask.StatusPending, storagetask.StatusRunning, storagetask.StatusWaitingRetry), storagetask.PhaseNEQ("committed"),
	).Order(ent.Asc(storagetask.FieldUpdatedAt)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if t.NextRetryAt != nil && t.NextRetryAt.After(time.Now()) {
			continue
		}
		writes, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(t.ID)).All(ctx)
		if e != nil {
			return e
		}
		active := false
		s.mu.Lock()
		for _, w := range writes {
			active = active || s.inFlight[w.ID]
		}
		s.mu.Unlock()
		if active {
			continue
		}
		if (t.Kind == "upload" || t.Kind == "repair") && t.Phase != "prepared" {
			continue
		}
		if t.Kind == "export" && t.ResultArtifactID == nil {
			prepared := false
			for _, w := range writes {
				prepared = prepared || w.Phase == "prepared"
			}
			if !prepared {
				continue
			}
		}
		if t.RetryStartedAt == nil {
			started := time.Now().UTC()
			n, e := s.client.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.StatusEQ(t.Status), storagetask.RetryStartedAtIsNil()).SetRetryStartedAt(started).Save(ctx)
			if e != nil {
				return e
			}
			if n != 1 {
				continue
			}
			t.RetryStartedAt = &started
		}
		if time.Since(*t.RetryStartedAt) >= s.cfg.RetryWindow || t.Attempts >= s.cfg.RetryMaxAttempts {
			_, e := s.client.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.StatusIn(storagetask.StatusPending, storagetask.StatusRunning, storagetask.StatusWaitingRetry), storagetask.PhaseNEQ("committed")).SetStatus(storagetask.StatusNeedsAction).SetErrorCode("storage_retry_exhausted").ClearNextRetryAt().Save(ctx)
			if e != nil {
				return e
			}
			continue
		}
		release, leaseErr := s.ClaimTaskExecution(ctx, t.ID)
		if errors.Is(leaseErr, ErrStorageInProgress) {
			continue
		}
		if leaseErr != nil {
			return leaseErr
		}
		attemptCtx, cancel := context.WithTimeout(ctx, min(s.cfg.TransferTimeout, time.Until(t.RetryStartedAt.Add(s.cfg.RetryWindow))))
		switch t.Kind {
		case "upload":
			e = s.resources.resumeUpload(attemptCtx, t)
		case "source_update":
			e = s.resources.resumeSourcePlan(attemptCtx, t)
		case "repair":
			e = s.resources.resumeRepair(attemptCtx, t)
		case "migration":
			e = s.ContinueMigration(attemptCtx, t.ID)
		case "export":
			e = s.resources.finishExport(attemptCtx, t)
		}
		cancel()
		release()
		if e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(e, ErrStorageMaintenance) && t.Kind == "migration" && t.Phase == "draining" {
				continue
			}
			state := storagetask.StatusNeedsAction
			update := s.client.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.StatusIn(storagetask.StatusPending, storagetask.StatusRunning, storagetask.StatusWaitingRetry), storagetask.PhaseNEQ("committed")).SetErrorCode(storageCode(e)).AddAttempts(1).ClearNextRetryAt()
			if (errors.Is(e, storage.ErrUnavailable) || errors.Is(e, context.DeadlineExceeded)) && t.Attempts+1 < s.cfg.RetryMaxAttempts && time.Since(*t.RetryStartedAt) < s.cfg.RetryWindow {
				state = storagetask.StatusWaitingRetry
				delay := s.cfg.RetryBaseDelay
				for i := 0; i < t.Attempts && delay < s.cfg.RetryMaxDelay; i++ {
					delay = min(delay, s.cfg.RetryMaxDelay/2) * 2
				}
				delay = min(delay, s.cfg.RetryMaxDelay)
				delay = delay/2 + time.Duration(rand.Int64N(max(int64(delay/2), 1)))
				var hint interface{ RetryAfter() time.Duration }
				if errors.As(e, &hint) {
					delay = max(delay, hint.RetryAfter())
				}
				next := time.Now().UTC().Add(delay)
				if next.Before(t.RetryStartedAt.Add(s.cfg.RetryWindow)) {
					update.SetNextRetryAt(next)
				} else {
					state = storagetask.StatusNeedsAction
				}
			}
			if _, err = update.SetStatus(state).Save(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ResourceService) resumeUpload(ctx context.Context, t *ent.StorageTask) error {
	w, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
	if e != nil {
		return e
	}
	f, e := s.storage.materialize(ctx, w.SpaceID, w.ObjectKey, w.ProviderVersion, w.ActualBytes, w.Sha256)
	if e != nil {
		return e
	}
	defer f.Close()
	path, _ := t.Input["path"].(string)
	path, e = NormalizeResourcePath(path)
	if e != nil {
		return e
	}
	format := strings.TrimPrefix(filepath.Ext(path), ".")
	items, e := s.parseStoredSegments(ctx, f.File, format)
	if e != nil {
		return e
	}
	p, e := s.client.Project.Get(ctx, t.ProjectID)
	if e != nil {
		return e
	}
	_, e = s.commitUploaded(ctx, t, &StagedObject{Task: t, Write: w, File: f.File}, items, path, p)
	return e
}

func (s *ResourceService) resumeRepair(ctx context.Context, t *ent.StorageTask) error {
	if t.SourceRevisionID == nil || t.ResourceID == nil {
		return ErrInvalidInput
	}
	rev, e := s.client.SourceRevision.Query().Where(sourcerevision.IDEQ(*t.SourceRevisionID), sourcerevision.ProjectIDEQ(t.ProjectID), sourcerevision.ResourceIDEQ(*t.ResourceID), sourcerevision.DeletedEQ(false)).Only(ctx)
	if e != nil {
		return e
	}
	if rev.VerificationState != sourcerevision.VerificationStateVerified || rev.Size == nil || rev.Sha256 == nil {
		return ErrRepairMismatch
	}
	w, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
	if e != nil {
		return e
	}
	if w.ActualBytes != *rev.Size || w.Sha256 != *rev.Sha256 {
		return ErrRepairMismatch
	}
	f, e := s.storage.materialize(ctx, w.SpaceID, w.ObjectKey, w.ProviderVersion, w.ActualBytes, w.Sha256)
	if e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, t.ProjectID, t.ExpectedStorageGeneration); e != nil {
			return e
		}
		b, e := tx.Blob.Get(ctx, rev.SourceBlobID)
		if e != nil {
			return e
		}
		if b.LocationGeneration != t.ExpectedLocationGeneration {
			return ErrStorageConflict
		}
		if _, e = s.storage.publish(ctx, tx, w, blob.PurposeSource, b); e != nil {
			return e
		}
		return s.storage.finishTask(ctx, tx, t.ID)
	})
}

type SourceVersionRecord struct {
	ID                 int     `json:"id"`
	VerificationState  string  `json:"verification_state"`
	Format             string  `json:"format"`
	ParserVersion      string  `json:"parser_version"`
	Current            bool    `json:"current"`
	Size               *int64  `json:"size"`
	SHA256             *string `json:"sha256"`
	LocationGeneration int64   `json:"location_generation"`
	Health             string  `json:"health"`
}

func (s *ResourceService) Versions(ctx context.Context, actor, projectID, resourceID int) ([]SourceVersionRecord, error) {
	if _, e := s.GetResource(ctx, actor, projectID, resourceID); e != nil {
		return nil, e
	}
	revs, e := s.client.SourceRevision.Query().Where(sourcerevision.ProjectIDEQ(projectID), sourcerevision.ResourceIDEQ(resourceID), sourcerevision.DeletedEQ(false)).Order(ent.Desc(sourcerevision.FieldID)).All(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]SourceVersionRecord, 0, len(revs))
	for _, rev := range revs {
		b, e := s.client.Blob.Get(ctx, rev.SourceBlobID)
		if e != nil {
			return nil, e
		}
		health := "unknown"
		if b.ActiveLocationID != nil {
			l, e := s.client.BlobLocation.Get(ctx, *b.ActiveLocationID)
			if e != nil {
				return nil, e
			}
			health = string(l.Integrity)
		}
		out = append(out, SourceVersionRecord{rev.ID, string(rev.VerificationState), rev.Format, rev.ParserVersion, rev.Current, rev.Size, rev.Sha256, b.LocationGeneration, health})
	}
	return out, nil
}
func (s *ResourceService) ListExports(ctx context.Context, actor, projectID, resourceID int) ([]*ent.ExportArtifact, error) {
	return s.ListExportsIncludingDeleted(ctx, actor, projectID, resourceID, false)
}
