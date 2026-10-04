package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strconv"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageuploadbatch"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageuploadbatchitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
)

type StoredUploadResource struct {
	ID                      int       `json:"id"`
	Path                    string    `json:"path"`
	Name                    string    `json:"name"`
	Directory               string    `json:"directory"`
	Format                  string    `json:"format"`
	TotalSegments           int       `json:"total_segments"`
	TranslatedSegments      int       `json:"translated_segments"`
	ApprovedSegments        int       `json:"approved_segments"`
	CurrentSourceRevisionID *int      `json:"current_source_revision_id"`
	SourceGeneration        int64     `json:"source_generation"`
	TranslationGeneration   int64     `json:"translation_generation"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}
type StoredUploadBatchItem struct {
	Path             string                `json:"path"`
	Action           string                `json:"action"`
	Resource         *StoredUploadResource `json:"resource,omitempty"`
	ExistingResource *StoredUploadResource `json:"existing_resource,omitempty"`
	Error            string                `json:"error,omitempty"`
	ErrorCode        string                `json:"error_code,omitempty"`
}
type StoredUploadBatchResult struct {
	OperationID string                  `json:"operation_id"`
	Items       []StoredUploadBatchItem `json:"items"`
}
type uploadManifestItem struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type uploadBatchManifest struct {
	Version           int                  `json:"version"`
	StorageGeneration int64                `json:"storage_generation"`
	Items             []uploadManifestItem `json:"items"`
}

func freezeUploadResource(ctx context.Context, client *ent.Client, r *ent.Resource) (*StoredUploadResource, error) {
	dir := path.Dir(r.Path)
	if dir == "." {
		dir = ""
	}
	result := &StoredUploadResource{ID: r.ID, Path: r.Path, Name: path.Base(r.Path), Directory: dir, Format: r.Format, TotalSegments: r.TotalSegments, CurrentSourceRevisionID: r.CurrentSourceRevisionID, SourceGeneration: r.SourceGeneration, TranslationGeneration: r.TranslationGeneration, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	var err error
	result.TranslatedSegments, err = client.Segment.Query().Where(segment.ResourceIDEQ(r.ID), segment.TargetTextNotNil(), segment.TargetTextNEQ("")).Count(ctx)
	if err != nil {
		return nil, err
	}
	result.ApprovedSegments, err = client.Segment.Query().Where(segment.ResourceIDEQ(r.ID), segment.StatusEQ(segment.StatusApproved)).Count(ctx)
	return result, err
}
func completeUploadBatchItem(ctx context.Context, tx *ent.Client, taskID int, r *ent.Resource) error {
	rows, err := tx.StorageUploadBatchItem.Query().Where(storageuploadbatchitem.TaskIDEQ(taskID), storageuploadbatchitem.StatusNEQ("completed")).All(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	value, err := freezeUploadResource(ctx, tx, r)
	if err != nil {
		return err
	}
	for _, item := range rows {
		data, err := json.Marshal(StoredUploadBatchItem{Path: item.Path, Action: "created", Resource: value})
		if err != nil {
			return err
		}
		if err = tx.StorageUploadBatchItem.UpdateOneID(item.ID).SetStatus("completed").SetResponse(data).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// UploadResourceBatch fixes the complete ordered manifest before registering any
// per-file operation. Only bounded request buffers are retained, never file bytes
// in the batch ledger.
func (s *ResourceService) UploadResourceBatch(ctx context.Context, actor, projectID int, key string, files []UploadedFile) (*StoredUploadBatchResult, error) {
	if err := s.ensureStorage(ctx); err != nil {
		return nil, err
	}
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return nil, err
	}
	if err := ValidateStorageIdempotencyKey(key); err != nil {
		return nil, err
	}
	if len(files) == 0 || len(files) > 100 {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	readers := make([]io.ReadSeeker, len(files))
	var temporary []*StorageFile
	defer func() {
		for _, f := range temporary {
			_ = f.Close()
		}
	}()
	manifest := make([]uploadManifestItem, 0, len(files))
	var total int64
	for i, f := range files {
		p, e := NormalizeResourcePath(firstNonEmpty(f.Path, f.Filename))
		if e != nil {
			return nil, e
		}
		if f.Size < 0 || f.Size > s.storage.writeLimit("upload") {
			return nil, ErrStorageTooLarge
		}
		total += f.Size
		if total > s.storage.maxTempBytes/2 {
			return nil, ErrStorageTooLarge
		}
		h := sha256.New()
		var reader io.ReadSeeker
		if seek, ok := f.Reader.(io.ReadSeeker); ok {
			reader = seek
			if _, e = seek.Seek(0, io.SeekStart); e != nil {
				return nil, e
			}
		} else {
			spool, e := s.storage.temporary(f.Size)
			if e != nil {
				return nil, e
			}
			temporary = append(temporary, spool)
			n, e := io.Copy(io.MultiWriter(spool, h), io.LimitReader(&storageContextReader{ctx: ctx, r: f.Reader}, f.Size+1))
			if e != nil {
				return nil, e
			}
			if n != f.Size {
				return nil, ErrInvalidInput
			}
			reader = spool
		}
		if _, ok := f.Reader.(io.ReadSeeker); ok {
			n, e := io.Copy(h, io.LimitReader(&storageContextReader{ctx: ctx, r: reader}, f.Size+1))
			if e != nil {
				return nil, e
			}
			if n != f.Size {
				return nil, ErrInvalidInput
			}
		}
		if _, e = reader.Seek(0, io.SeekStart); e != nil {
			return nil, e
		}
		readers[i] = reader
		manifest = append(manifest, uploadManifestItem{p, f.Size, hex.EncodeToString(h.Sum(nil))})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	var batch *ent.StorageUploadBatch
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		var e error
		batch, e = tx.StorageUploadBatch.Query().Where(storageuploadbatch.ActorIDEQ(actor), storageuploadbatch.ProjectIDEQ(projectID), storageuploadbatch.IdempotencyKeyEQ(key)).Only(ctx)
		if e == nil {
			if batch.ManifestHash != digest {
				return ErrStorageIdempotency
			}
			return nil
		}
		if !ent.IsNotFound(e) {
			return e
		}
		p, e := tx.Project.Get(ctx, projectID)
		if e != nil {
			return e
		}
		frozen, e := json.Marshal(uploadBatchManifest{Version: 1, StorageGeneration: p.StorageGeneration, Items: manifest})
		if e != nil {
			return e
		}
		batch, e = tx.StorageUploadBatch.Create().SetActorID(actor).SetProjectID(projectID).SetIdempotencyKey(key).SetOperationID(generateUniqueID()).SetManifestHash(digest).SetManifest(frozen).Save(ctx)
		if e != nil {
			return e
		}
		for i, item := range manifest {
			if e = tx.StorageUploadBatchItem.Create().SetBatchID(batch.ID).SetOrdinal(i).SetPath(item.Path).SetSize(item.Size).SetSha256(item.SHA256).Exec(ctx); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	operationError := func(e error) error { return &StorageOperationError{Err: e, OperationID: batch.OperationID} }
	var frozen uploadBatchManifest
	if err = json.Unmarshal(batch.Manifest, &frozen); err != nil || frozen.Version != 1 {
		return nil, operationError(ErrStorageIdempotency)
	}
	token := generateUniqueID()
	now := time.Now().UTC()
	n, err := s.client.StorageUploadBatch.Update().Where(storageuploadbatch.IDEQ(batch.ID), storageuploadbatch.Or(storageuploadbatch.LeaseUntilIsNil(), storageuploadbatch.LeaseUntilLTE(now))).SetLeaseToken(token).SetLeaseUntil(now.Add(s.storage.cfg.TransferTimeout + s.storage.cfg.MetadataTimeout)).Save(ctx)
	if err != nil {
		return nil, operationError(err)
	}
	if n != 1 {
		return nil, operationError(ErrStorageInProgress)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.storage.cfg.MetadataTimeout)
		defer cancel()
		_, _ = s.client.StorageUploadBatch.Update().Where(storageuploadbatch.IDEQ(batch.ID), storageuploadbatch.LeaseTokenEQ(token)).SetLeaseToken("").ClearLeaseUntil().Save(c)
	}()
	rows, err := s.client.StorageUploadBatchItem.Query().Where(storageuploadbatchitem.BatchIDEQ(batch.ID)).Order(ent.Asc(storageuploadbatchitem.FieldOrdinal)).All(ctx)
	if err != nil {
		return nil, operationError(err)
	}
	result := &StoredUploadBatchResult{OperationID: batch.OperationID, Items: make([]StoredUploadBatchItem, 0, len(rows))}
	for _, item := range rows {
		if len(item.Response) > 0 {
			var value StoredUploadBatchItem
			if err = json.Unmarshal(item.Response, &value); err != nil {
				return nil, operationError(err)
			}
			result.Items = append(result.Items, value)
			continue
		}
		if err = ctx.Err(); err != nil {
			return nil, operationError(err)
		}
		value := StoredUploadBatchItem{Path: item.Path}
		taskKey := "batch:" + batch.OperationID + ":" + strconv.Itoa(item.Ordinal)
		t, e := s.storage.Begin(ctx, actor, projectID, StorageIntent{Kind: "upload", IdempotencyKey: taskKey, Path: item.Path, Size: item.Size, StorageGeneration: frozen.StorageGeneration, RequireStorageGeneration: true})
		if e == nil {
			if e = s.client.StorageUploadBatchItem.UpdateOneID(item.ID).SetTaskID(t.ID).Exec(ctx); e != nil {
				return nil, operationError(e)
			}
			if t.Phase == "committed" {
				r, _, replayErr := replayResourceResult(t)
				e = replayErr
				if e == nil {
					e = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error { return completeUploadBatchItem(ctx, tx, t.ID, r) })
				}
			} else {
				var existing *ent.Resource
				existing, e = s.FindResourceByPath(ctx, projectID, item.Path)
				if e == nil && existing != nil {
					value.Action = "conflict"
					value.ExistingResource, e = freezeUploadResource(ctx, s.client, existing)
				} else if e == nil {
					f := files[item.Ordinal]
					f.Path = item.Path
					f.Reader = readers[item.Ordinal]
					f.IdempotencyKey = taskKey
					f.ExpectedStorageGeneration = &frozen.StorageGeneration
					_, e = s.uploadStoredResource(ctx, actor, projectID, f)
				}
			}
		}
		if e != nil {
			if errors.Is(e, ErrStorageInProgress) || errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return nil, operationError(e)
			}
			if t != nil && errors.Is(e, ErrStorageConflict) {
				busy, checkErr := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseNotIn("prepared", "committed", "cleaned")).Exist(ctx)
				if checkErr != nil {
					return nil, operationError(checkErr)
				}
				if busy {
					return nil, operationError(ErrStorageInProgress)
				}
			}
			// A registered nonterminal Write is recoverable; do not freeze a transient
			// failure while the same task can still publish in the executor.
			if t != nil {
				current, getErr := s.client.StorageTask.Get(ctx, t.ID)
				if getErr != nil {
					return nil, operationError(getErr)
				}
				if current.Status == storagetask.StatusRunning || current.Status == storagetask.StatusPending || current.Status == storagetask.StatusWaitingRetry {
					return nil, operationError(ErrStorageInProgress)
				}
			}
			value.Action = "failed"
			value.ErrorCode = StorageErrorCode(e)
			value.Error = value.ErrorCode
		}
		if value.Action != "" {
			encoded, e := json.Marshal(value)
			if e != nil {
				return nil, operationError(e)
			}
			e = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
				if t != nil {
					current, e := tx.StorageTask.Get(ctx, t.ID)
					if e != nil {
						return e
					}
					if current.Phase == "committed" {
						return ErrStorageConflict
					}
					n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.PhaseEQ(current.Phase), storagetask.StatusEQ(current.Status)).SetPhase("batch_failed").SetStatus(storagetask.StatusFailed).SetErrorCode(value.ErrorCode).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx)
					if e != nil {
						return e
					}
					if n != 1 {
						return ErrStorageConflict
					}
					if _, e = tx.StorageWrite.Update().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseNotIn("committed", "cleaned", "reconciling")).SetPhase("reconcile").Save(ctx); e != nil {
						return e
					}
				}
				return tx.StorageUploadBatchItem.UpdateOneID(item.ID).SetStatus("completed").SetErrorCode(value.ErrorCode).SetResponse(encoded).Exec(ctx)
			})
			if e != nil {
				return nil, operationError(e)
			}
		} else {
			current, e := s.client.StorageUploadBatchItem.Get(ctx, item.ID)
			if e != nil {
				return nil, operationError(e)
			}
			if len(current.Response) == 0 {
				return nil, operationError(ErrStorageInProgress)
			}
			if e = json.Unmarshal(current.Response, &value); e != nil {
				return nil, operationError(e)
			}
		}
		result.Items = append(result.Items, value)
	}
	if err = s.client.StorageUploadBatch.UpdateOneID(batch.ID).SetStatus("completed").Exec(ctx); err != nil {
		return nil, operationError(err)
	}
	return result, nil
}
