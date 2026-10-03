package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ziputil"
)

type StorageFile struct {
	*os.File
	s        *StorageService
	reserved int64
	once     sync.Once
}

func (f *StorageFile) Close() error {
	var err error
	f.once.Do(func() {
		name := f.Name()
		err = f.File.Close()
		remove := os.Remove(name)
		if !errors.Is(remove, os.ErrNotExist) {
			err = errors.Join(err, remove)
		}
		f.s.mu.Lock()
		f.s.tempBytes -= f.reserved
		f.s.mu.Unlock()
	})
	return err
}
func (s *StorageService) temporary(size int64) (*StorageFile, error) {
	s.mu.Lock()
	if size < 0 || size > s.maxTempBytes-s.tempBytes {
		s.mu.Unlock()
		return nil, storage.ErrLimit
	}
	s.tempBytes += size
	s.mu.Unlock()
	f, e := os.CreateTemp(s.workDir, "read-*")
	if e != nil {
		s.mu.Lock()
		s.tempBytes -= size
		s.mu.Unlock()
		return nil, e
	}
	return &StorageFile{File: f, s: s, reserved: size}, nil
}

func (s *StorageService) materialize(ctx context.Context, space int, key, version string, size int64, digest string) (*StorageFile, error) {
	f, err := s.temporary(size)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = f.Close()
		}
	}()
	d, err := s.driver(ctx, space, false)
	if err != nil {
		return nil, err
	}
	r, err := d.Open(ctx, storage.Object{Key: key, Version: version})
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(&storageContextReader{ctx: ctx, r: r}, size+1))
	closeErr := r.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return nil, storage.ErrCorrupt
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	success = true
	return f, nil
}

func (s *StorageService) readBlob(ctx context.Context, blobID int) (*StorageFile, error) {
	s.mu.Lock()
	b, err := s.client.Blob.Get(ctx, blobID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if b.ActiveLocationID == nil || b.Size == nil || b.Sha256 == nil {
		s.mu.Unlock()
		return nil, storage.ErrCorrupt
	}
	loc, err := s.client.BlobLocation.Get(ctx, *b.ActiveLocationID)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if loc.Status != bloblocation.StatusLive {
		s.mu.Unlock()
		return nil, storage.ErrNotFound
	}
	s.readers[loc.ID]++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.readers[loc.ID]--; s.mu.Unlock() }()
	f, err := s.materialize(ctx, loc.SpaceID, loc.ObjectKey, loc.ProviderVersion, *b.Size, *b.Sha256)
	if errors.Is(err, storage.ErrNotFound) {
		_ = s.client.BlobLocation.UpdateOneID(loc.ID).SetIntegrity(bloblocation.IntegrityMissing).Exec(ctx)
	} else if errors.Is(err, storage.ErrCorrupt) {
		_ = s.client.BlobLocation.UpdateOneID(loc.ID).SetIntegrity(bloblocation.IntegrityCorrupt).Exec(ctx)
	}
	return f, err
}

type resourceSnapshot struct {
	ResourceID            int                     `json:"resource_id"`
	ProjectID             int                     `json:"project_id"`
	RevisionID            *int                    `json:"source_revision_id"`
	SourceGeneration      int64                   `json:"source_generation"`
	TranslationGeneration int64                   `json:"translation_generation"`
	OutputGeneration      int64                   `json:"output_generation"`
	SourceLang            string                  `json:"source_lang"`
	TargetLang            string                  `json:"target_lang"`
	Format                string                  `json:"format"`
	Segments              []pipeline.SegmentInput `json:"segments"`
	legacyPath            string
}

func (s *ResourceService) captureSnapshot(ctx context.Context, actor, projectID, resourceID int) (*resourceSnapshot, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, false); err != nil {
		return nil, err
	}
	var tx *ent.Tx
	var err error
	if s.storage != nil && s.storage.dialect == "postgres" {
		tx, err = s.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else {
		tx, err = s.client.Tx(ctx)
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Resource.Query().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	p, err := tx.Project.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	snapshot := &resourceSnapshot{ResourceID: res.ID, ProjectID: projectID, RevisionID: res.CurrentSourceRevisionID, SourceGeneration: res.SourceGeneration, TranslationGeneration: res.TranslationGeneration, OutputGeneration: p.OutputGeneration, SourceLang: p.SourceLang, TargetLang: p.TargetLang, Format: res.Format, legacyPath: res.StoragePath}
	limits := config.DefaultStorageConfig().Limits
	if s.storage != nil {
		limits = s.storage.cfg.Limits
	}
	var cursor int
	var total int
	for {
		rows, e := tx.Segment.Query().Where(segment.ResourceIDEQ(resourceID), segment.IDGT(cursor)).Order(ent.Asc(segment.FieldID)).Limit(500).All(ctx)
		if e != nil {
			return nil, e
		}
		if len(rows) == 0 {
			break
		}
		if len(snapshot.Segments)+len(rows) > limits.MaxSegments {
			return nil, ErrStorageTooLarge
		}
		for _, row := range rows {
			if row.Meta != nil {
				total += len(*row.Meta)
			}
			total += len(row.SourceText)
			if row.TargetText != nil {
				total += len(*row.TargetText)
			}
		}
		if int64(total) > limits.MaxMetadataBytes {
			return nil, ErrStorageTooLarge
		}
		snapshot.Segments = append(snapshot.Segments, BuildSegmentInputsWithTarget(rows)...)
		cursor = rows[len(rows)-1].ID
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(snapshot.Segments, func(i, j int) bool {
		a, _ := strconv.Atoi(snapshot.Segments[i].ID)
		b, _ := strconv.Atoi(snapshot.Segments[j].ID)
		return a < b
	})
	return snapshot, nil
}

func (s *ResourceService) OriginalFile(ctx context.Context, actor, projectID, resourceID int) (*StorageFile, error) {
	if err := s.ensureStorage(ctx); err != nil {
		return nil, err
	}
	res, err := s.GetResource(ctx, actor, projectID, resourceID)
	if err != nil {
		return nil, err
	}
	if res.Resource.CurrentSourceRevisionID == nil {
		return nil, storage.ErrCorrupt
	}
	rev, err := s.client.SourceRevision.Get(ctx, *res.Resource.CurrentSourceRevisionID)
	if err != nil {
		return nil, err
	}
	return s.storage.readBlob(ctx, rev.SourceBlobID)
}

func (s *ResourceService) renderSnapshot(ctx context.Context, snapshot *resourceSnapshot) (*StorageFile, error) {
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	limits := s.storage.cfg.Limits
	ctx = ziputil.WithLimits(ctx, ziputil.Limits{MaxEntries: limits.MaxArchiveEntries, MaxExpandedBytes: limits.MaxExpandedBytes, MaxOutputBytes: limits.MaxOutputBytes})
	for i := range snapshot.Segments {
		snapshot.Segments[i].Meta = normalizeMeta(snapshot.Segments[i].Meta)
	}
	if len(snapshot.Segments) == 0 {
		return nil, ErrNoTranslatedSegments
	}
	var original io.ReadCloser
	var err error
	if snapshot.RevisionID != nil {
		rev, e := s.client.SourceRevision.Get(ctx, *snapshot.RevisionID)
		if e != nil {
			return nil, e
		}
		original, err = s.storage.readBlob(ctx, rev.SourceBlobID)
	} else {
		// 旧版文件在显式迁移确立其身份之前保持可读。
		original, err = s.loadOriginalFile(snapshot.legacyPath)
	}
	if err != nil {
		return nil, err
	}
	defer original.Close()
	p, err := parser.Resolve(snapshot.Format)
	if err != nil {
		return nil, err
	}
	doc := pipeline.BuildDocumentFromSegments(snapshot.Segments, snapshot.SourceLang, snapshot.TargetLang, snapshot.Format)
	if defects := parser.InspectTargets(p, doc); len(defects) > 0 {
		return nil, &TargetMarkupError{Defects: defects}
	}
	output, err := s.storage.temporary(limits.MaxOutputBytes)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = output.Close()
		}
	}()
	writer := &storageBoundedWriter{ctx: ctx, w: output, remaining: limits.MaxOutputBytes}
	if err = p.Render(ctx, doc, original, writer); err != nil {
		return nil, err
	}
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	success = true
	return output, nil
}

type storageBoundedWriter struct {
	ctx       context.Context
	w         io.Writer
	remaining int64
}

func (w *storageBoundedWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, ErrStorageTooLarge
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}
