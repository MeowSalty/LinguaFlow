package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ziputil"
)

const storageParserVersion = "2"

func (s *ResourceService) SetStorage(storage *StorageService) {
	s.storage = storage
	s.projects.storage = storage
	storage.resources = s
}
func (s *ResourceService) ensureStorage(ctx context.Context) error {
	if s.storage != nil {
		return nil
	}
	if s.fileStore == nil {
		return ErrStoragePolicy
	}
	// 面向嵌入式调用方的兼容构造函数；常规服务器装配会显式注入 storage。
	st, err := NewStorageService(s.client, s.projects, filepath.Join(s.fileStore.Root(), "..", "tmp"))
	if err != nil {
		return err
	}
	driver, err := localstore.New(filepath.Join(s.fileStore.Root(), "..", "objects"))
	if err != nil {
		return err
	}
	if _, err = st.InstallSiteSpace(ctx, "local", driver); err != nil {
		return err
	}
	s.SetStorage(st)
	return nil
}

func (s *ResourceService) uploadStoredResource(ctx context.Context, actor, projectID int, file UploadedFile) (*ResourceUploadResult, error) {
	if err := s.ensureStorage(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	path, err := NormalizeResourcePath(firstNonEmpty(file.Path, file.Filename))
	if err != nil {
		return nil, err
	}
	p, err := s.projects.requireProjectAccess(ctx, actor, projectID, true)
	if err != nil {
		return nil, err
	}
	if p.StorageSpaceID == nil {
		return nil, ErrStoragePolicy
	}
	intent := StorageIntent{Kind: "upload", IdempotencyKey: file.IdempotencyKey, Path: path, Size: file.Size}
	if file.ExpectedStorageGeneration != nil {
		intent.StorageGeneration = *file.ExpectedStorageGeneration
		intent.RequireStorageGeneration = true
	}
	task, err := s.storage.Begin(ctx, actor, projectID, intent)
	if err != nil {
		return nil, err
	}
	if task.Phase == "committed" && task.ResultResourceID != nil {
		if e := s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); e != nil {
			return nil, e
		}
		r, _, e := replayResourceResult(task)
		if e != nil {
			return nil, e
		}
		return &ResourceUploadResult{Resource: r, TotalSegments: r.TotalSegments}, nil
	}
	if err = storageTerminalError(task); err != nil {
		return nil, err
	}
	release, err := s.storage.ClaimTaskExecution(ctx, task.ID)
	if err != nil {
		return nil, &StorageOperationError{Err: err, TaskID: task.ID}
	}
	defer release()
	task, err = s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task.Phase == "committed" {
		if err = s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); err != nil {
			return nil, err
		}
		r, _, e := replayResourceResult(task)
		if e != nil {
			return nil, e
		}
		return &ResourceUploadResult{Resource: r, TotalSegments: r.TotalSegments}, nil
	}
	if task.Phase == "prepared" {
		if e := s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); e != nil {
			return nil, e
		}
		if e := s.resumeUpload(ctx, task); e != nil {
			return nil, e
		}
		done, e := s.client.StorageTask.Get(ctx, task.ID)
		if e != nil {
			return nil, e
		}
		r, _, e := replayResourceResult(done)
		if e != nil {
			return nil, e
		}
		return &ResourceUploadResult{Resource: r, TotalSegments: r.TotalSegments}, nil
	}
	staged, err := s.storage.Stage(ctx, task, file.Reader, file.Size)
	if err != nil {
		return nil, err
	}
	defer staged.Close()
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	items, err := s.parseStoredSegments(ctx, staged.File, format)
	if err != nil {
		_ = s.storage.failWrite(context.WithoutCancel(ctx), staged.Write.ID, err)
		return nil, err
	}
	return s.commitUploaded(ctx, task, staged, items, path, p)
}

func (s *ResourceService) commitUploaded(ctx context.Context, task *ent.StorageTask, staged *StagedObject, items []parsedResourceSegment, path string, p *ent.Project) (*ResourceUploadResult, error) {
	projectID := task.ProjectID
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	var result *ent.Resource
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, projectID, task.ExpectedStorageGeneration); e != nil {
			return e
		}
		if e := s.storage.logicalAdmission(ctx, tx, p, staged.Write.ActualBytes); e != nil {
			return e
		}
		b, e := s.storage.publish(ctx, tx, staged.Write, blob.PurposeSource, nil)
		if e != nil {
			return e
		}
		result, e = tx.Resource.Create().SetProjectID(projectID).SetPath(path).SetFormat(format).SetStoragePath("blob:" + b.Identity).SetTotalSegments(len(items)).SetSourceGeneration(1).Save(ctx)
		if e != nil {
			if ent.IsConstraintError(e) {
				return ErrResourceAlreadyExists
			}
			return e
		}
		rev, e := tx.SourceRevision.Create().SetResourceID(result.ID).SetProjectID(projectID).SetSourceBlobID(b.ID).SetFormat(format).SetParserVersion(storageParserVersion).SetSize(staged.Write.ActualBytes).SetSha256(staged.Write.Sha256).Save(ctx)
		if e != nil {
			return e
		}
		if e = replaceResourceSegmentsBatch(ctx, tx.Segment, result.ID, items); e != nil {
			return e
		}
		if e = tx.Resource.UpdateOneID(result.ID).SetCurrentSourceRevisionID(rev.ID).Exec(ctx); e != nil {
			return e
		}
		if e = tx.StorageTask.UpdateOneID(task.ID).SetResultResourceID(result.ID).SetResultRevisionID(rev.ID).Exec(ctx); e != nil {
			return e
		}
		result, e = tx.Resource.Get(ctx, result.ID)
		if e != nil {
			return e
		}
		if e = saveResourceResult(ctx, tx, task.ID, result, IncrementalUpdateStats{}); e != nil {
			return e
		}
		if e = completeUploadBatchItem(ctx, tx, task.ID, result); e != nil {
			return e
		}
		return s.storage.finishTask(ctx, tx, task.ID)
	})
	if err != nil {
		_ = s.storage.failWrite(context.WithoutCancel(ctx), staged.Write.ID, err)
		return nil, err
	}
	done, err := s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	result, _, err = replayResourceResult(done)
	if err != nil {
		return nil, err
	}
	return &ResourceUploadResult{Resource: result, TotalSegments: len(items)}, nil
}

func parseStoredSegments(ctx context.Context, f *os.File, format string) ([]parsedResourceSegment, error) {
	return parseStoredSegmentsWithConfig(ctx, f, format, config.DefaultStorageConfig())
}
func (s *ResourceService) parseStoredSegments(ctx context.Context, f *os.File, format string) ([]parsedResourceSegment, error) {
	return parseStoredSegmentsWithConfig(ctx, f, format, s.storage.cfg)
}
func parseStoredSegmentsWithConfig(ctx context.Context, f *os.File, format string, cfg config.StorageConfig) ([]parsedResourceSegment, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.TransferTimeout)
	defer cancel()
	ctx = ziputil.WithLimits(ctx, ziputil.Limits{MaxEntries: cfg.Limits.MaxArchiveEntries, MaxExpandedBytes: cfg.Limits.MaxExpandedBytes, MaxOutputBytes: cfg.Limits.MaxOutputBytes})
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	p, err := parser.Resolve(format)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedFormat, err)
	}
	doc, err := p.Parse(ctx, f, format)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}
	if len(doc.Segments) > cfg.Limits.MaxSegments {
		return nil, ErrStorageTooLarge
	}
	items := make([]parsedResourceSegment, 0, len(doc.Segments))
	var metadataBytes int
	for i, item := range doc.Segments {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		source := item.OriginalSource
		if source == "" {
			source = item.Source
		}
		if source == "" {
			source = " "
		}
		meta, e := json.Marshal(item.Meta)
		if e != nil {
			return nil, e
		}
		metadataBytes += len(meta)
		if int64(metadataBytes) > cfg.Limits.MaxMetadataBytes {
			return nil, ErrStorageTooLarge
		}
		items = append(items, parsedResourceSegment{Index: i, SourceText: source, TargetText: item.Target, Meta: item.Meta})
	}
	return items, nil
}

type SourceUpdatePreview struct {
	TaskID                  int                    `json:"task_id"`
	SourceGeneration        int64                  `json:"source_generation"`
	TranslationGeneration   int64                  `json:"translation_generation"`
	Stats                   IncrementalUpdateStats `json:"stats"`
	ExpiresAt               *time.Time             `json:"expires_at"`
	BaselineTrust           string                 `json:"baseline_trust"`
	LegacySnapshotAvailable bool                   `json:"legacy_snapshot_available"`
	LegacySnapshotExpiresAt *time.Time             `json:"legacy_snapshot_expires_at"`
}

func (s *ResourceService) PreviewSourceUpdate(ctx context.Context, actor, projectID, resourceID int, file UploadedFile) (*SourceUpdatePreview, error) {
	if err := s.ensureStorage(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return nil, err
	}
	task, err := s.storage.Begin(ctx, actor, projectID, StorageIntent{Kind: "source_update", IdempotencyKey: file.IdempotencyKey, ResourceID: resourceID, Size: file.Size, CaptureSourceBaseline: true})
	if err != nil {
		return nil, err
	}
	if len(task.SourcePlan) > 0 {
		if err = s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); err != nil {
			return nil, err
		}
		return s.SourcePreviewForTask(ctx, actor, projectID, task.ID)
	}
	if err = storageTerminalError(task); err != nil {
		return nil, err
	}
	res, err := s.GetResource(ctx, actor, projectID, resourceID)
	if err != nil {
		return nil, err
	}
	release, err := s.storage.ClaimTaskExecution(ctx, task.ID)
	if err != nil {
		return nil, &StorageOperationError{Err: err, TaskID: task.ID}
	}
	defer release()
	task, err = s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if len(task.SourcePlan) > 0 {
		if err = s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); err != nil {
			return nil, err
		}
		return s.SourcePreviewForTask(ctx, actor, projectID, task.ID)
	}
	if task.Phase == "parsing" {
		if err = s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); err != nil {
			return nil, err
		}
		if err = s.resumeSourcePlan(ctx, task); err != nil {
			return nil, err
		}
		return s.SourcePreviewForTask(ctx, actor, projectID, task.ID)
	}
	stage, err := s.storage.Stage(ctx, task, file.Reader, file.Size)
	if err != nil {
		return nil, err
	}
	defer stage.Close()
	items, err := s.parseStoredSegments(ctx, stage.File, res.Resource.Format)
	if err != nil {
		_ = s.storage.failWrite(ctx, stage.Write.ID, err)
		return nil, err
	}
	if err = s.prepareSourcePlan(ctx, task, items); err != nil {
		return nil, err
	}
	return s.SourcePreviewForTask(ctx, actor, projectID, task.ID)
}

func (s *ResourceService) readPreparedSource(ctx context.Context, task *ent.StorageTask, format string) ([]parsedResourceSegment, error) {
	w, err := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.storage.materialize(ctx, w.SpaceID, w.ObjectKey, w.ProviderVersion, w.ActualBytes, w.Sha256)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.parseStoredSegments(ctx, f.File, format)
}

func (s *ResourceService) updateStoredResource(ctx context.Context, actor, projectID, resourceID int, file UploadedFile) (*ent.Resource, *IncrementalUpdateStats, error) {
	if file.PreviewTaskID == 0 || file.ExpectedSourceGeneration == nil || file.ExpectedTranslationGeneration == nil {
		return nil, nil, ErrSourceRevisionConflict
	}
	return s.CommitSourceUpdate(ctx, actor, projectID, resourceID, file.PreviewTaskID, *file.ExpectedSourceGeneration, *file.ExpectedTranslationGeneration)
}

func (s *ResourceService) CommitSourceUpdate(ctx context.Context, actor, projectID, resourceID, taskID int, sourceGen, translationGen int64) (*ent.Resource, *IncrementalUpdateStats, error) {
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return nil, nil, err
	}
	task, err := s.storage.task(ctx, actor, projectID, taskID)
	if err != nil {
		return nil, nil, err
	}
	if task.Kind != "source_update" || task.ResourceID == nil || *task.ResourceID != resourceID {
		return nil, nil, ErrInvalidInput
	}
	if task.ExpectedSourceGeneration != sourceGen || task.ExpectedTranslationGeneration != translationGen {
		return nil, nil, ErrSourceRevisionConflict
	}
	if task.Phase == "committed" {
		return replayResourceResult(task)
	}
	if err = storageTerminalError(task); err != nil {
		return nil, nil, err
	}
	if err = s.storage.taskWriteAdmission(ctx, s.client, task); err != nil {
		return nil, nil, s.storage.recordDeploymentBlock(ctx, task.ID, err)
	}
	plan, err := decodeSourcePlan(task)
	if err != nil {
		return nil, nil, err
	}
	release, err := s.storage.ClaimTaskExecution(ctx, task.ID)
	if err != nil {
		return nil, nil, &StorageOperationError{Err: err, TaskID: task.ID}
	}
	defer release()
	task, err = s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, nil, err
	}
	if task.Phase == "committed" {
		return replayResourceResult(task)
	}
	res, err := s.client.Resource.Get(ctx, resourceID)
	if err != nil {
		return nil, nil, err
	}
	w, err := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
	if err != nil {
		return nil, nil, err
	}
	if w.ID != plan.WriteID || w.Sha256 != plan.SHA256 || w.ActualBytes != plan.Size {
		return nil, nil, ErrSourceRevisionConflict
	}
	// Confirmation can occur long after preparation. Verify the immutable
	// candidate again without reparsing or changing the frozen segment plan.
	candidate, err := s.storage.materialize(ctx, w.SpaceID, w.ObjectKey, w.ProviderVersion, w.ActualBytes, w.Sha256)
	if err != nil {
		return nil, nil, err
	}
	if err = candidate.Close(); err != nil {
		return nil, nil, err
	}
	var stats *IncrementalUpdateStats
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, projectID, task.ExpectedStorageGeneration); e != nil {
			return e
		}
		n, e := tx.Resource.Update().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID), resource.SourceGenerationEQ(sourceGen), resource.TranslationGenerationEQ(translationGen)).AddSourceGeneration(1).AddTranslationGeneration(1).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrSourceRevisionConflict
		}
		if e = storageSourceIdle(ctx, tx, resourceID, projectID); e != nil {
			return e
		}
		p, e := tx.Project.Get(ctx, projectID)
		if e != nil {
			return e
		}
		if e = s.storage.logicalAdmission(ctx, tx, p, w.ActualBytes); e != nil {
			return e
		}
		changes := make([]SegmentChange, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			change := SegmentChange{ChangeType: c.Kind, NewIndex: c.Index, NewSource: c.Source, NewMeta: normalizeMeta(c.Meta)}
			if c.OldID > 0 {
				old, e := tx.Segment.Get(ctx, c.OldID)
				if e != nil {
					return e
				}
				if old.ResourceID == nil || *old.ResourceID != resourceID {
					return ErrSourceRevisionConflict
				}
				change.OldSegment = old
			}
			changes = append(changes, change)
		}
		stats = &plan.Stats
		b, e := s.storage.publish(ctx, tx, w, blob.PurposeSource, nil)
		if e != nil {
			return e
		}
		rev, e := tx.SourceRevision.Create().SetResourceID(resourceID).SetProjectID(projectID).SetSourceBlobID(b.ID).SetFormat(res.Format).SetParserVersion(storageParserVersion).SetSize(w.ActualBytes).SetSha256(w.Sha256).Save(ctx)
		if e != nil {
			return e
		}
		if res.CurrentSourceRevisionID != nil {
			if e = tx.SourceRevision.UpdateOneID(*res.CurrentSourceRevisionID).SetCurrent(false).SetRetainUntil(time.Now().UTC().Add(s.storage.sourceRetention)).Exec(ctx); e != nil {
				return e
			}
		}
		local := *s
		local.client = tx
		if e = local.applySegmentChanges(ctx, resourceID, changes); e != nil {
			return e
		}
		if e = tx.Resource.UpdateOneID(resourceID).SetCurrentSourceRevisionID(rev.ID).SetStoragePath("blob:" + b.Identity).SetTotalSegments(plan.Stats.Added + plan.Stats.Updated + plan.Stats.Unchanged).Exec(ctx); e != nil {
			return e
		}
		if e = tx.StorageTask.UpdateOneID(task.ID).SetResultResourceID(resourceID).SetResultRevisionID(rev.ID).Exec(ctx); e != nil {
			return e
		}
		result, e := tx.Resource.Get(ctx, resourceID)
		if e != nil {
			return e
		}
		if e = saveResourceResult(ctx, tx, task.ID, result, *stats); e != nil {
			return e
		}
		if len(task.LegacySnapshot) > 0 {
			if e = tx.StorageTask.UpdateOneID(task.ID).SetLegacySnapshotExpiresAt(time.Now().UTC().Add(s.storage.sourceRetention)).Exec(ctx); e != nil {
				return e
			}
		}
		return s.storage.finishTask(ctx, tx, task.ID)
	})
	if err != nil {
		return nil, nil, s.storage.recordDeploymentBlock(ctx, task.ID, err)
	}
	done, err := s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, nil, err
	}
	return replayResourceResult(done)
}

func changeStats(changes []SegmentChange) *IncrementalUpdateStats {
	r := &IncrementalUpdateStats{}
	for _, c := range changes {
		switch c.ChangeType {
		case SegmentChangeAdded:
			r.Added++
		case SegmentChangeUpdated:
			r.Updated++
		case SegmentChangeUnchanged:
			r.Unchanged++
		case SegmentChangeDeleted:
			r.Deleted++
		}
	}
	return r
}

func storageSourceIdle(ctx context.Context, tx *ent.Client, resourceID, projectID int) error {
	busy, err := tx.JobResource.Query().Where(jobresource.HasResourceWith(resource.IDEQ(resourceID)), jobresource.HasJobWith(job.StatusIn(JobStatusPending, JobStatusRunning, JobStatusPaused))).Exist(ctx)
	if err != nil {
		return err
	}
	if busy {
		return ErrSourceRevisionConflict
	}
	syncBusy, err := tx.SyncTask.Query().Where(synctask.HasProjectWith(project.IDEQ(projectID)), synctask.StatusIn("pending", "running", "paused")).Exist(ctx)
	if err != nil {
		return err
	}
	if syncBusy {
		return ErrSourceRevisionConflict
	}
	return nil
}

func (s *ResourceService) RepairSource(ctx context.Context, actor, projectID, resourceID, revisionID, targetSpace int, locationGeneration int64, file UploadedFile) (*ent.StorageTask, error) {
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	rev, err := s.client.SourceRevision.Query().Where(sourcerevision.IDEQ(revisionID), sourcerevision.ResourceIDEQ(resourceID), sourcerevision.ProjectIDEQ(projectID), sourcerevision.DeletedEQ(false)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if rev.VerificationState != sourcerevision.VerificationStateVerified || rev.Size == nil || rev.Sha256 == nil {
		return nil, ErrRepairMismatch
	}
	task, err := s.storage.Begin(ctx, actor, projectID, StorageIntent{Kind: "repair", IdempotencyKey: file.IdempotencyKey, ResourceID: resourceID, SourceRevisionID: revisionID, TargetSpaceID: targetSpace, Size: file.Size, LocationGeneration: locationGeneration})
	if err != nil {
		return nil, err
	}
	if task.Phase == "committed" {
		if e := s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); e != nil {
			return nil, e
		}
		return task, nil
	}
	if err = storageTerminalError(task); err != nil {
		return nil, err
	}
	release, err := s.storage.ClaimTaskExecution(ctx, task.ID)
	if err != nil {
		return nil, &StorageOperationError{Err: err, TaskID: task.ID}
	}
	defer release()
	task, err = s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task.Phase == "committed" {
		if err = s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); err != nil {
			return nil, err
		}
		return task, nil
	}
	if task.Phase == "prepared" {
		if e := s.storage.verifyTaskContent(ctx, task, file.Reader, file.Size); e != nil {
			return nil, e
		}
		if e := s.resumeRepair(ctx, task); e != nil {
			return nil, e
		}
		return s.storage.task(ctx, actor, projectID, task.ID)
	}
	stage, err := s.storage.Stage(ctx, task, file.Reader, file.Size)
	if err != nil {
		return nil, err
	}
	defer stage.Close()
	if stage.Write.ActualBytes != *rev.Size || stage.Write.Sha256 != *rev.Sha256 {
		_ = s.storage.failWrite(ctx, stage.Write.ID, ErrRepairMismatch)
		return nil, ErrRepairMismatch
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, projectID, task.ExpectedStorageGeneration); e != nil {
			return e
		}
		b, e := tx.Blob.Get(ctx, rev.SourceBlobID)
		if e != nil {
			return e
		}
		if b.LocationGeneration != locationGeneration {
			return ErrStorageConflict
		}
		if _, e = s.storage.publish(ctx, tx, stage.Write, blob.PurposeSource, b); e != nil {
			return e
		}
		return s.storage.finishTask(ctx, tx, task.ID)
	})
	if err != nil {
		_ = s.storage.failWrite(ctx, stage.Write.ID, err)
		return nil, err
	}
	return s.storage.task(ctx, actor, projectID, task.ID)
}

var _ = errors.Is
var _ = storagetask.StatusCompleted
