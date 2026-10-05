package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
)

// StorageKeyValid is shared by HTTP and service entrypoints.
func StorageKeyValid(key string) bool {
	return len(key) > 0 && len(key) <= 200 && strings.TrimSpace(key) != "" && !strings.ContainsAny(key, "\r\n")
}

func storageTerminalError(t *ent.StorageTask) error {
	if t.Phase == "committed" {
		return nil
	}
	if t.Phase == "batch_failed" {
		return ErrStorageCancelled
	}
	if t.Status == storagetask.StatusCancelled {
		return ErrStorageCancelled
	}
	if t.ErrorCode == "repair_content_mismatch" {
		return ErrRepairMismatch
	}
	if t.ErrorCode == "storage_idempotency_conflict" {
		return ErrStorageIdempotency
	}
	if t.ErrorCode == "storage_parse_failed" {
		return ErrParseFailed
	}
	if t.Phase == "expired" || t.ErrorCode == "storage_intent_expired" {
		return ErrStorageExpired
	}
	if t.Kind == "source_update" && t.ContractVersion == 0 {
		return ErrSourceRevisionConflict
	}
	if t.Deadline != nil && !t.Deadline.After(time.Now().UTC()) && !(t.Kind == "migration" && (t.Phase == "cutover" || t.Phase == "cleanup")) {
		return ErrStorageExpired
	}
	return nil
}

// ClaimTaskExecution is a durable, bounded lease. The caller's work context must
// not outlive TransferTimeout; the metadata margin permits its final transaction.
func (s *StorageService) ClaimTaskExecution(ctx context.Context, id int) (func(), error) {
	token := generateUniqueID()
	now := time.Now().UTC()
	n, err := s.client.StorageTask.Update().Where(storagetask.IDEQ(id), storagetask.Or(storagetask.LeaseUntilIsNil(), storagetask.LeaseUntilLTE(now))).SetLeaseToken(token).SetLeaseUntil(now.Add(s.cfg.TransferTimeout + s.cfg.MetadataTimeout)).Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrStorageInProgress
	}
	return func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.MetadataTimeout)
		defer cancel()
		_, _ = s.client.StorageTask.Update().Where(storagetask.IDEQ(id), storagetask.LeaseTokenEQ(token)).SetLeaseToken("").ClearLeaseUntil().Save(c)
	}, nil
}

// verifyTaskContent consumes the complete retransmission even for committed
// tasks. A partial read never establishes a new content identity.
func (s *StorageService) verifyTaskContent(ctx context.Context, t *ent.StorageTask, r io.Reader, size int64) error {
	if size < 0 || size > s.writeLimit(t.Kind) {
		return ErrStorageTooLarge
	}
	if t.InputSha256 == "" {
		if t.Phase == "committed" || t.ContractVersion == 0 {
			return &StorageOperationError{Err: ErrStorageIdempotency, TaskID: t.ID}
		}
		return &StorageOperationError{Err: ErrStorageInProgress, TaskID: t.ID}
	}
	if t.InputSize != size {
		return ErrStorageIdempotency
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(&storageContextReader{ctx: ctx, r: r}, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != t.InputSha256 {
		return ErrStorageIdempotency
	}
	return nil
}

func (s *StorageService) expireTasks(ctx context.Context) error {
	now := time.Now().UTC()
	rows, err := s.client.StorageTask.Query().Where(storagetask.ProjectIDGT(0), storagetask.PhaseNotIn("committed", "expired", "invalidated", "batch_failed"), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled), storagetask.Or(storagetask.And(storagetask.DeadlineLTE(now), storagetask.Not(storagetask.And(storagetask.KindEQ("migration"), storagetask.PhaseIn("cutover", "cleanup")))), storagetask.And(storagetask.KindEQ("source_update"), storagetask.ContractVersionEQ(0)))).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, t := range rows {
		phase, code := "expired", "storage_intent_expired"
		if t.Kind == "source_update" && t.ContractVersion == 0 {
			phase, code = "invalidated", "source_revision_conflict"
		}
		err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.PhaseEQ(t.Phase), storagetask.StatusEQ(t.Status)).SetStatus(storagetask.StatusFailed).SetPhase(phase).SetErrorCode(code).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).ClearNextRetryAt().Save(ctx)
			if e != nil || n != 1 {
				return e
			}
			if t.Kind == "migration" {
				if _, e = tx.Project.Update().Where(project.IDEQ(t.ProjectID), project.StorageMigrationTaskIDEQ(t.ID)).SetStorageState("active").ClearStorageMigrationTaskID().AddStorageGeneration(1).Save(ctx); e != nil {
					return e
				}
			}
			_, e = tx.StorageWrite.Update().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseNotIn("committed", "cleaned", "reconciling")).SetPhase("reconcile").Save(ctx)
			return e
		})
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	cursor := s.legacySnapshotCursor
	s.mu.Unlock()
	backups, err := s.client.StorageTask.Query().Where(storagetask.IDGT(cursor), storagetask.KindEQ("source_update"), storagetask.LegacySnapshotNotNil(), storagetask.LegacySnapshotExpiresAtLTE(now)).Order(ent.Asc(storagetask.FieldID)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if len(backups) == 0 {
		s.legacySnapshotCursor = 0
	} else {
		s.legacySnapshotCursor = backups[len(backups)-1].ID
	}
	s.mu.Unlock()
	for _, backup := range backups {
		keep, _, e := legacySnapshotLifetime(ctx, s.client, backup)
		if e != nil {
			return e
		}
		if keep {
			continue
		}
		if _, e = s.client.StorageTask.Update().Where(storagetask.IDEQ(backup.ID), storagetask.PhaseEQ(backup.Phase), storagetask.StatusEQ(backup.Status), storagetask.LegacySnapshotExpiresAtEQ(*backup.LegacySnapshotExpiresAt)).ClearLegacySnapshot().ClearLegacySnapshotExpiresAt().Save(ctx); e != nil {
			return e
		}
	}
	return nil
}

// storedResourceResult is an explicit business projection, not an ORM object.
type storedResourceResult struct {
	ID                    int       `json:"id"`
	ProjectID             *int      `json:"project_id"`
	RevisionID            *int      `json:"current_source_revision_id"`
	Path                  string    `json:"path"`
	Format                string    `json:"format"`
	TotalSegments         int       `json:"total_segments"`
	SourceGeneration      int64     `json:"source_generation"`
	TranslationGeneration int64     `json:"translation_generation"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func freezeResource(r *ent.Resource) storedResourceResult {
	return storedResourceResult{r.ID, r.ProjectID, r.CurrentSourceRevisionID, r.Path, r.Format, r.TotalSegments, r.SourceGeneration, r.TranslationGeneration, r.CreatedAt, r.UpdatedAt}
}
func (r storedResourceResult) resource() *ent.Resource {
	return &ent.Resource{ID: r.ID, ProjectID: r.ProjectID, CurrentSourceRevisionID: r.RevisionID, Path: r.Path, Format: r.Format, TotalSegments: r.TotalSegments, SourceGeneration: r.SourceGeneration, TranslationGeneration: r.TranslationGeneration, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

type storedSourceResult struct {
	Version  int                    `json:"version"`
	Resource storedResourceResult   `json:"resource"`
	Stats    IncrementalUpdateStats `json:"stats"`
}

func saveResourceResult(ctx context.Context, tx *ent.Client, taskID int, r *ent.Resource, stats IncrementalUpdateStats) error {
	data, err := json.Marshal(storedSourceResult{1, freezeResource(r), stats})
	if err != nil {
		return err
	}
	return tx.StorageTask.UpdateOneID(taskID).SetResultSnapshot(data).Exec(ctx)
}
func replayResourceResult(t *ent.StorageTask) (*ent.Resource, *IncrementalUpdateStats, error) {
	var result storedSourceResult
	if len(t.ResultSnapshot) == 0 || json.Unmarshal(t.ResultSnapshot, &result) != nil || result.Version != 1 || result.Resource.ID == 0 {
		return nil, nil, ErrSourceRevisionConflict
	}
	return result.Resource.resource(), &result.Stats, nil
}
