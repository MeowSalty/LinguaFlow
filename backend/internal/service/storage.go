package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

var (
	ErrStorageConflict    = errors.New("storage_generation_conflict")
	ErrStorageMaintenance = errors.New("storage_maintenance")
	ErrStoragePolicy      = errors.New("storage_policy_violation")
	ErrStorageIdempotency = errors.New("storage_idempotency_conflict")
	ErrRepairMismatch     = errors.New("repair_content_mismatch")
	ErrStorageTooLarge    = errors.New("storage_file_too_large")
	ErrStorageCancelled   = errors.New("storage_cancelled")
)

// ObjectDriver 是存储生命周期服务消费的字节接口。
type ObjectDriver interface {
	PutNew(context.Context, string, io.Reader, int64) (storage.Object, error)
	Open(context.Context, storage.Object) (io.ReadCloser, error)
	Stat(context.Context, storage.Object) (storage.Object, error)
	Delete(context.Context, storage.Object) error
}

type StorageService struct {
	client               *ent.Client
	projects             *ProjectService
	resources            *ResourceService
	cfg                  config.StorageConfig
	mu                   sync.Mutex
	transferMu           sync.Mutex
	drivers              map[int]storage.Driver
	resolve              func(context.Context, int, bool) (storage.Driver, error)
	workDir              string
	defaultSpaceID       int
	maxFileBytes         int64
	maxTempBytes         int64
	tempBytes            int64
	inFlight             map[int]bool
	readers              map[int]int
	slots                chan struct{}
	ingressSlots         chan struct{}
	maintenance          bool
	deleteGrace          time.Duration
	sourceRetention      time.Duration
	dialect              string
	legacySnapshotCursor int
}

func NewStorageService(client *ent.Client, projects *ProjectService, workDir string) (*StorageService, error) {
	if err := os.MkdirAll(workDir, 0700); err != nil {
		return nil, err
	}
	return &StorageService{client: client, projects: projects, cfg: config.DefaultStorageConfig(), drivers: map[int]storage.Driver{}, workDir: workDir, maxFileBytes: 100 << 20, maxTempBytes: 4 << 30, inFlight: map[int]bool{}, readers: map[int]int{}, slots: make(chan struct{}, 2), ingressSlots: make(chan struct{}, 2), deleteGrace: 24 * time.Hour, sourceRetention: 30 * 24 * time.Hour}, nil
}

func (s *StorageService) RegisterDriver(spaceID int, driver storage.Driver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drivers[spaceID] = driver
}
func (s *StorageService) SetResolver(resolve func(context.Context, int, bool) (storage.Driver, error)) {
	s.resolve = resolve
}

func (s *StorageService) driver(ctx context.Context, spaceID int, write bool) (storage.Driver, error) {
	space, err := s.client.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	conn, err := s.client.StorageConnection.Get(ctx, space.ConnectionID)
	if err != nil {
		return nil, err
	}
	if conn.Status != storageconnection.StatusEnabled || space.Status == storagespace.StatusDisabled {
		return nil, storage.ErrPermission
	}
	if write && (s.maintenance || space.Status != storagespace.StatusActive) {
		return nil, ErrStorageMaintenance
	}
	s.mu.Lock()
	d := s.drivers[spaceID]
	s.mu.Unlock()
	if d != nil {
		return d, nil
	}
	if s.resolve != nil {
		return s.resolve(ctx, spaceID, write)
	}
	return nil, storage.ErrUnavailable
}

type StorageIntent struct {
	Kind                     string `json:"kind"`
	IdempotencyKey           string `json:"-"`
	ResourceID               int    `json:"resource_id,omitempty"`
	SourceRevisionID         int    `json:"source_revision_id,omitempty"`
	ArtifactID               int    `json:"artifact_id,omitempty"`
	TargetSpaceID            int    `json:"target_space_id,omitempty"`
	Path                     string `json:"path,omitempty"`
	Size                     int64  `json:"size"`
	SourceGeneration         int64  `json:"source_generation"`
	TranslationGeneration    int64  `json:"translation_generation"`
	StorageGeneration        int64  `json:"storage_generation"`
	RequireStorageGeneration bool   `json:"-"`
	CaptureSourceBaseline    bool   `json:"-"`
	LocationGeneration       int64  `json:"location_generation"`
}

func (s *StorageService) Begin(ctx context.Context, actorID, projectID int, in StorageIntent) (*ent.StorageTask, error) {
	p, err := s.projects.requireProjectAccess(ctx, actorID, projectID, true)
	if err != nil {
		return nil, err
	}
	if in.Size < 0 || in.Size > s.writeLimit(in.Kind) {
		return nil, ErrStorageTooLarge
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = generateUniqueID()
	}
	if err = ValidateStorageIdempotencyKey(in.IdempotencyKey); err != nil {
		return nil, err
	}
	fingerprintInput := in
	originalKey := in.IdempotencyKey
	if in.CaptureSourceBaseline {
		keyHash := sha256.Sum256([]byte("source-preview\x00" + in.IdempotencyKey))
		in.IdempotencyKey = hex.EncodeToString(keyHash[:])
		fingerprintInput.Path = ""
		fingerprintInput.SourceGeneration = 0
		fingerprintInput.TranslationGeneration = 0
	} else if in.Kind == "source_update" {
		keyHash := sha256.Sum256([]byte("source-intent\x00" + in.IdempotencyKey))
		in.IdempotencyKey = hex.EncodeToString(keyHash[:])
	}
	data, err := json.Marshal(fingerprintInput)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(sum[:])
	var task *ent.StorageTask
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		var e error
		keyPredicate := storagetask.IdempotencyKeyEQ(in.IdempotencyKey)
		if in.Kind == "source_update" {
			keyPredicate = storagetask.Or(keyPredicate, storagetask.And(storagetask.ContractVersionEQ(0), storagetask.IdempotencyKeyEQ(originalKey)))
		}
		task, e = tx.StorageTask.Query().Where(storagetask.ActorIDEQ(actorID), storagetask.ProjectIDEQ(projectID), storagetask.KindEQ(in.Kind), keyPredicate).Only(ctx)
		if e == nil {
			if task.Kind == "source_update" && task.ContractVersion == 0 {
				return &StorageOperationError{Err: ErrSourceRevisionConflict, TaskID: task.ID}
			}
			if task.RequestHash != fingerprint {
				return ErrStorageIdempotency
			}
			return nil
		}
		if !ent.IsNotFound(e) {
			return e
		}
		if s.maintenance {
			return ErrStorageMaintenance
		}
		current, e := tx.Project.Get(ctx, p.ID)
		if e != nil {
			return e
		}
		// 在保留与替换所用的同一项目锁下注册 source 消费者，
		// 使已受理的任务不会与修订版本的退役产生竞争。
		if e = storageProjectGate(ctx, tx, current.ID, current.StorageGeneration); e != nil {
			return e
		}
		if current.StorageState != "active" && !(in.Kind == "repair" && (current.StorageState == "draining" || current.StorageState == "migrating")) {
			return ErrStorageMaintenance
		}
		if (in.Kind == "migration" || in.RequireStorageGeneration || in.StorageGeneration != 0) && current.StorageGeneration != in.StorageGeneration {
			return ErrStorageConflict
		}
		if in.ResourceID < 0 || in.SourceRevisionID < 0 || in.SourceGeneration < 0 || in.TranslationGeneration < 0 || in.LocationGeneration < 0 {
			return ErrInvalidInput
		}
		if (in.Kind == "repair" || in.Kind == "source_update") && in.ResourceID == 0 {
			return ErrInvalidInput
		}
		if in.Kind == "repair" && in.SourceRevisionID == 0 {
			return ErrInvalidInput
		}
		if in.ResourceID > 0 {
			var res *ent.Resource
			if res, e = tx.Resource.Query().Where(resource.IDEQ(in.ResourceID), resource.ProjectIDEQ(projectID)).Only(ctx); e != nil {
				return e
			}
			if in.CaptureSourceBaseline {
				in.Path = res.Path
				in.SourceGeneration = res.SourceGeneration
				in.TranslationGeneration = res.TranslationGeneration
			}
		}
		if in.SourceRevisionID > 0 {
			revision, e := tx.SourceRevision.Query().Where(sourcerevision.IDEQ(in.SourceRevisionID), sourcerevision.ProjectIDEQ(projectID), sourcerevision.DeletedEQ(false)).Only(ctx)
			if e != nil {
				return e
			}
			if revision.ResourceID != in.ResourceID {
				return ErrInvalidInput
			}
			if in.Kind == "repair" {
				if revision.VerificationState != sourcerevision.VerificationStateVerified || revision.Size == nil || revision.Sha256 == nil || *revision.Size != in.Size {
					return ErrRepairMismatch
				}
				object, e := tx.Blob.Get(ctx, revision.SourceBlobID)
				if e != nil {
					return e
				}
				if object.LocationGeneration != in.LocationGeneration {
					return ErrStorageConflict
				}
			}
		}
		if current.StorageSpaceID == nil {
			return ErrStoragePolicy
		}
		target := *current.StorageSpaceID
		if in.TargetSpaceID != 0 {
			target = in.TargetSpaceID
		}
		if target != *current.StorageSpaceID && in.Kind != "repair" {
			if e := s.allowedTarget(ctx, tx, current, target); e != nil {
				return e
			}
		}
		space, e := tx.StorageSpace.Get(ctx, target)
		if e != nil {
			return e
		}
		if space.Status != storagespace.StatusActive {
			return ErrStorageMaintenance
		}
		if in.Kind == "repair" {
			if e = s.allowedRepairTarget(ctx, tx, current, in.SourceRevisionID, target, in.Size); e != nil {
				return e
			}
		} else if in.Kind == "upload" || in.Kind == "source_update" {
			if e = s.allowedExistingTarget(ctx, tx, current, target, in.Size); e != nil {
				return e
			}
		}
		input := map[string]any{}
		resolved, e := json.Marshal(in)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(resolved, &input); e != nil {
			return e
		}
		create := tx.StorageTask.Create().SetContractVersion(1).SetInputSize(in.Size).SetOperationID(generateUniqueID()).SetIdempotencyKey(in.IdempotencyKey).SetRequestHash(fingerprint).SetActorID(actorID).SetProjectID(projectID).SetKind(in.Kind).SetTargetSpaceID(target).SetExpectedStorageGeneration(current.StorageGeneration).SetExpectedSourceGeneration(in.SourceGeneration).SetExpectedTranslationGeneration(in.TranslationGeneration).SetExpectedLocationGeneration(in.LocationGeneration).SetInput(input).SetDeadline(time.Now().UTC().Add(s.cfg.IntentTTL))
		if in.ResourceID > 0 {
			create.SetResourceID(in.ResourceID)
		}
		if in.SourceRevisionID > 0 {
			create.SetSourceRevisionID(in.SourceRevisionID)
		}
		task, e = create.Save(ctx)
		return e
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageTask.Get(ctx, task.ID)
}

type StagedObject struct {
	Task      *ent.StorageTask
	Write     *ent.StorageWrite
	File      *os.File
	service   *StorageService
	held      int64
	closeOnce sync.Once
}

func (f *StagedObject) Close() error {
	var err error
	f.closeOnce.Do(func() {
		if f.File != nil {
			name := f.File.Name()
			err = f.File.Close()
			err = errors.Join(err, os.Remove(name))
		}
		f.service.mu.Lock()
		f.service.tempBytes -= f.held
		delete(f.service.inFlight, f.Write.ID)
		f.service.mu.Unlock()
		<-f.service.slots
	})
	return err
}

func (s *StorageService) Stage(ctx context.Context, task *ent.StorageTask, r io.Reader, size int64) (out *StagedObject, err error) {
	if size < 0 || size > s.writeLimit(task.Kind) {
		return nil, ErrStorageTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.TransferTimeout)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	if s.tempBytes+size > s.maxTempBytes {
		s.mu.Unlock()
		<-s.slots
		return nil, storage.ErrPayloadTooLarge
	}
	s.tempBytes += size
	s.mu.Unlock()
	var write *ent.StorageWrite
	defer func() {
		if err != nil && out == nil {
			if write != nil {
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.MetadataTimeout)
				_ = s.failWrite(cleanupCtx, write.ID, err)
				cleanupCancel()
			}
			s.mu.Lock()
			s.tempBytes -= size
			if write != nil {
				delete(s.inFlight, write.ID)
			}
			s.mu.Unlock()
			<-s.slots
		}
	}()
	// 注册与清理认领共用这道闸门：已提交的意图在其传输即将开始时，
	// 不会被误判为空闲。
	s.transferMu.Lock()
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, e := tx.StorageTask.Get(ctx, task.ID)
		if e != nil {
			return e
		}
		if e = storageTerminalError(current); e != nil {
			return e
		}
		if current.Phase == "committed" {
			return ErrStorageConflict
		}
		if (task.Kind == "upload" || task.Kind == "repair" || task.Kind == "source_update") && current.Phase != "accepted" && current.Phase != "cleaned" {
			return ErrStorageConflict
		}
		if e = storageTaskGate(ctx, tx, current); e != nil {
			return e
		}
		p, e := tx.Project.Get(ctx, current.ProjectID)
		if e != nil {
			return e
		}
		if e = storageProjectGate(ctx, tx, p.ID, current.ExpectedStorageGeneration); e != nil {
			return e
		}
		if p.StorageState != "active" && !(current.Kind == "repair" || current.Kind == "migration") {
			return ErrStorageMaintenance
		}
		if current.Kind == "repair" {
			if current.SourceRevisionID == nil {
				return ErrInvalidInput
			}
			if e = s.allowedRepairTarget(ctx, tx, p, *current.SourceRevisionID, *current.TargetSpaceID, size); e != nil {
				return e
			}
		} else if current.Kind != "migration" {
			if e = s.allowedExistingTarget(ctx, tx, p, *current.TargetSpaceID, size); e != nil {
				return e
			}
		}
		if current.Kind == "export" {
			existing, e := tx.StorageWrite.Query().Where(storagewrite.TaskIDEQ(current.ID), storagewrite.PhaseNotIn("committed", "cleaned")).Exist(ctx)
			if e != nil {
				return e
			}
			if existing {
				return ErrStorageConflict
			}
		}
		space, e := tx.StorageSpace.Get(ctx, *current.TargetSpaceID)
		if e != nil {
			return e
		}
		conn, e := tx.StorageConnection.Get(ctx, space.ConnectionID)
		if e != nil {
			return e
		}
		if space.Status != storagespace.StatusActive || conn.Status != storageconnection.StatusEnabled {
			return storage.ErrPermission
		}
		total := space.ReservedBytes + space.CandidateBytes + space.LiveBytes + space.PendingDeleteBytes
		if size > space.CapacityBytes-total {
			return storage.ErrLimit
		}
		n, e := tx.StorageSpace.Update().Where(storagespace.IDEQ(space.ID), storagespace.ReservedBytesEQ(space.ReservedBytes), storagespace.CandidateBytesEQ(space.CandidateBytes), storagespace.LiveBytesEQ(space.LiveBytes), storagespace.PendingDeleteBytesEQ(space.PendingDeleteBytes)).AddReservedBytes(size).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		key := fmt.Sprintf("objects/%s/%d/%s", space.Identity, task.ProjectID, generateUniqueID())
		write, e = tx.StorageWrite.Create().SetTaskID(task.ID).SetSpaceID(space.ID).SetAttemptID(generateUniqueID()).SetObjectKey(key).SetMaxBytes(size).SetConnectionGeneration(conn.ManagementGeneration).SetSpaceGeneration(space.ManagementGeneration).SetAuthGeneration(conn.ActiveAuthGeneration).SetNillableExpiresAt(current.Deadline).Save(ctx)
		if e != nil {
			return e
		}
		if e = tx.StorageReservation.Create().SetWriteID(write.ID).SetSpaceID(space.ID).SetBytes(size).Exec(ctx); e != nil {
			return e
		}
		update := tx.StorageTask.UpdateOneID(task.ID).SetStatus(storagetask.StatusRunning)
		if task.Kind == "upload" || task.Kind == "repair" || task.Kind == "source_update" {
			update.SetPhase("receiving")
		}
		return update.Exec(ctx)
	})
	if err != nil {
		s.transferMu.Unlock()
		return nil, err
	}
	s.mu.Lock()
	s.inFlight[write.ID] = true
	s.mu.Unlock()
	s.transferMu.Unlock()
	f, e := os.OpenFile(filepath.Join(s.workDir, write.AttemptID+".input"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(&storageContextReader{ctx: ctx, r: r}, size+1))
	if e != nil {
		return nil, e
	}
	if n != size {
		return nil, ErrRepairMismatch
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if task.Kind == "upload" || task.Kind == "repair" || task.Kind == "source_update" {
		err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			current, e := tx.StorageTask.Get(ctx, task.ID)
			if e != nil {
				return e
			}
			if e = storageTaskGate(ctx, tx, current); e != nil {
				return e
			}
			if current.InputSha256 != "" && (current.InputSha256 != digest || current.InputSize != n) {
				return ErrStorageIdempotency
			}
			return tx.StorageTask.UpdateOneID(task.ID).SetInputSha256(digest).SetInputSize(n).Exec(ctx)
		})
		if err != nil {
			return nil, err
		}
	}
	if task.Kind == "repair" && task.SourceRevisionID != nil {
		rev, e := s.client.SourceRevision.Get(ctx, *task.SourceRevisionID)
		if e != nil {
			return nil, e
		}
		if rev.VerificationState != sourcerevision.VerificationStateVerified || rev.Sha256 == nil || rev.Size == nil || *rev.Sha256 != digest || *rev.Size != n {
			return nil, ErrRepairMismatch
		}
	}
	if e = f.Sync(); e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return nil, e
	}
	if e = s.client.StorageWrite.UpdateOneID(write.ID).SetActualBytes(n).SetSha256(digest).SetPhase("receiving").SetOutcomeUnknown(true).Exec(ctx); e != nil {
		return nil, e
	}
	d, e := s.driver(ctx, write.SpaceID, true)
	if e != nil {
		return nil, e
	}
	obj, e := d.PutNew(ctx, write.ObjectKey, f, size)
	if e != nil {
		return nil, e
	}
	if e = s.client.StorageWrite.UpdateOneID(write.ID).SetProviderVersion(obj.Version).SetPhase("verifying").Exec(ctx); e != nil {
		return nil, e
	}
	read, e := d.Open(ctx, obj)
	if e != nil {
		return nil, e
	}
	check := sha256.New()
	count, readErr := io.Copy(check, io.LimitReader(&storageContextReader{ctx: ctx, r: read}, size+1))
	closeErr := read.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if count != size || hex.EncodeToString(check.Sum(nil)) != digest {
		return nil, storage.ErrCorrupt
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, e := tx.StorageTask.Get(ctx, task.ID)
		if e != nil {
			return e
		}
		if e = storageTaskGate(ctx, tx, current); e != nil {
			return e
		}
		if e := tx.StorageWrite.UpdateOneID(write.ID).SetProviderVersion(obj.Version).SetPhase("prepared").SetOutcomeUnknown(false).Exec(ctx); e != nil {
			return e
		}
		if _, e := tx.StorageSpace.UpdateOneID(write.SpaceID).AddReservedBytes(-size).AddCandidateBytes(size).Save(ctx); e != nil {
			return e
		}
		if _, e := tx.StorageReservation.Update().Where(storagereservation.WriteIDEQ(write.ID), storagereservation.StateEQ(storagereservation.StateReserved)).SetState(storagereservation.StateCandidate).Save(ctx); e != nil {
			return e
		}
		if task.Kind == "upload" || task.Kind == "repair" || task.Kind == "source_update" {
			phase := "prepared"
			if task.Kind == "source_update" {
				phase = "parsing"
			}
			return tx.StorageTask.UpdateOneID(task.ID).SetPhase(phase).Exec(ctx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	write, e = s.client.StorageWrite.Get(ctx, write.ID)
	if e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return nil, e
	}
	out = &StagedObject{Task: task, Write: write, File: f, service: s, held: size}
	return out, nil
}

type storageContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *storageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// publish 只创建经过校验的引用。调用方需在同一个事务中提交业务变更。
func (s *StorageService) publish(ctx context.Context, tx *ent.Client, w *ent.StorageWrite, purpose blob.Purpose, existing *ent.Blob) (*ent.Blob, error) {
	task, err := tx.StorageTask.Get(ctx, w.TaskID)
	if err != nil {
		return nil, err
	}
	if err = storageTaskGate(ctx, tx, task); err != nil {
		return nil, err
	}
	n, err := tx.StorageWrite.Update().Where(storagewrite.IDEQ(w.ID), storagewrite.PhaseEQ("prepared")).SetPhase("publishing").Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrStorageConflict
	}
	w, err = tx.StorageWrite.Get(ctx, w.ID)
	if err != nil {
		return nil, err
	}
	p, err := tx.Project.Get(ctx, task.ProjectID)
	if err != nil {
		return nil, err
	}
	if p.StorageGeneration != task.ExpectedStorageGeneration {
		return nil, ErrStorageConflict
	}
	if p.StorageState != "active" && !((task.Kind == "repair" || task.Kind == "migration") && (p.StorageState == "draining" || p.StorageState == "migrating")) {
		return nil, ErrStorageMaintenance
	}
	if task.Kind == "repair" {
		if task.SourceRevisionID == nil {
			return nil, ErrInvalidInput
		}
		if err = s.allowedRepairTarget(ctx, tx, p, *task.SourceRevisionID, w.SpaceID, 0); err != nil {
			return nil, err
		}
	} else if task.Kind != "migration" {
		if err = s.allowedExistingTarget(ctx, tx, p, w.SpaceID, 0); err != nil {
			return nil, err
		}
	}
	sp, err := tx.StorageSpace.Get(ctx, w.SpaceID)
	if err != nil {
		return nil, err
	}
	conn, err := tx.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return nil, err
	}
	if sp.ManagementGeneration != w.SpaceGeneration || conn.ManagementGeneration != w.ConnectionGeneration || sp.Status != storagespace.StatusActive || conn.Status != storageconnection.StatusEnabled {
		return nil, ErrStorageConflict
	}
	b := existing
	if b == nil {
		ownerKind, ownerID := storageProjectOwner(p)
		b, err = tx.Blob.Create().SetIdentity(generateUniqueID()).SetProjectID(p.ID).SetOwnerKind(blob.OwnerKind(ownerKind)).SetOwnerID(ownerID).SetPurpose(purpose).SetSize(w.ActualBytes).SetSha256(w.Sha256).Save(ctx)
		if err != nil {
			return nil, err
		}
	} else if b.ProjectID != p.ID || b.Size == nil || b.Sha256 == nil || *b.Size != w.ActualBytes || *b.Sha256 != w.Sha256 || b.Status != blob.StatusReady {
		return nil, ErrRepairMismatch
	}
	location, err := tx.BlobLocation.Create().SetBlobID(b.ID).SetSpaceID(sp.ID).SetObjectKey(w.ObjectKey).SetProviderVersion(w.ProviderVersion).SetSize(w.ActualBytes).SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.IntegrityAvailable).SetVerifiedAt(time.Now().UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	update := tx.Blob.Update().Where(blob.IDEQ(b.ID), blob.LocationGenerationEQ(b.LocationGeneration)).SetActiveLocationID(location.ID).SetStatus(blob.StatusReady).AddLocationGeneration(1)
	n, err = update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrStorageConflict
	}
	if existing != nil && existing.ActiveLocationID != nil {
		if err = s.retireLocation(ctx, tx, *existing.ActiveLocationID, p.ID); err != nil {
			return nil, err
		}
	}
	if err = tx.StorageWrite.UpdateOneID(w.ID).SetLocationID(location.ID).SetPhase("committed").Exec(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.StorageSpace.UpdateOneID(sp.ID).AddCandidateBytes(-w.ActualBytes).AddLiveBytes(w.ActualBytes).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.StorageReservation.Update().Where(storagereservation.WriteIDEQ(w.ID)).SetState(storagereservation.StateLive).Save(ctx); err != nil {
		return nil, err
	}
	b, err = tx.Blob.Get(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil && task.Kind == "repair" {
		if err = s.reconcileMigrationRepair(ctx, tx, p, b, sp.ID); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func storageProjectOwner(p *ent.Project) (string, int) {
	if p.OwnerUserID != nil {
		return "user", *p.OwnerUserID
	}
	if p.OwnerOrgID != nil {
		return "org", *p.OwnerOrgID
	}
	return "site", 0
}

func (s *StorageService) finishTask(ctx context.Context, tx *ent.Client, id int) error {
	n, err := tx.StorageTask.Update().Where(storagetask.IDEQ(id), storagetask.StatusNEQ(storagetask.StatusCancelled), storagetask.PhaseNEQ("committed")).SetStatus(storagetask.StatusCompleted).SetPhase("committed").SetErrorCode("").Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStorageConflict
	}
	return nil
}

func (s *StorageService) task(ctx context.Context, actor, projectID, id int) (*ent.StorageTask, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, false); err != nil {
		return nil, err
	}
	return s.client.StorageTask.Query().Where(storagetask.IDEQ(id), storagetask.ProjectIDEQ(projectID)).Only(ctx)
}

func (s *StorageService) Cancel(ctx context.Context, actor, projectID, id int) (*ent.StorageTask, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return nil, err
	}
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		t, e := tx.StorageTask.Query().Where(storagetask.IDEQ(id), storagetask.ProjectIDEQ(projectID)).Only(ctx)
		if e != nil {
			return e
		}
		if !interactiveStorageTask(t.Kind) {
			return storage.ErrUnsupported
		}
		if t.Phase == "committed" {
			return nil
		}
		if t.Status == storagetask.StatusCancelled {
			return nil
		}
		if e = storageTerminalError(t); e != nil {
			return e
		}
		if t.Kind == "migration" && (t.Phase == "cutover" || t.Phase == "cleanup") {
			return ErrStorageConflict
		}
		n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(id), storagetask.PhaseEQ(t.Phase), storagetask.StatusEQ(t.Status)).SetStatus(storagetask.StatusCancelled).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	t, err := s.task(ctx, actor, projectID, id)
	if err != nil {
		return nil, err
	}
	if t.Kind == "migration" && t.Status == storagetask.StatusCancelled {
		if err = s.cancelMigration(ctx, t); err != nil {
			return nil, err
		}
		return s.task(ctx, actor, projectID, id)
	}
	return t, nil
}

func storageCode(err error) string {
	return StorageErrorCode(err)
}

func (s *StorageService) writeLimit(kind string) int64 {
	if kind == "upload" || kind == "repair" || kind == "source_update" {
		return s.maxFileBytes
	}
	return s.cfg.Limits.MaxOutputBytes
}

// 该行更新用于串行化取消、发布与阶段流转。
func storageTaskGate(ctx context.Context, tx *ent.Client, task *ent.StorageTask) error {
	if err := storageTerminalError(task); err != nil {
		return err
	}
	n, err := tx.StorageTask.Update().Where(storagetask.IDEQ(task.ID), storagetask.PhaseEQ(task.Phase), storagetask.StatusEQ(task.Status)).SetPhase(task.Phase).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStorageConflict
	}
	return nil
}

// 项目写闸门必须与发布、迁移处于同一事务。
func storageProjectGate(ctx context.Context, tx *ent.Client, id int, generation int64) error {
	n, err := tx.Project.Update().Where(project.IDEQ(id), project.StorageGenerationEQ(generation)).SetStorageGeneration(generation).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStorageConflict
	}
	return nil
}
