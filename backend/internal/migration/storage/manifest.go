// Package storagemigrate 实现显式的离线遗留文件清单盘点与
// 元数据迁移。常规服务启动绝不能导入此包。
package storagemigrate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// COMPAT(migration-storage-import): the published manifest contract remains 1.
// COMPAT(migration-journal-readers): explicit takeover binds its immutable bytes
// and operation ID; new mutable receipts are stored separately.
// Tracking: backend/internal/migration/catalog/compatibility.md
const ManifestVersion = 1
const blockedJobError = "storage_migration_needs_action: legacy job input cannot be proven; create a new task from the current source"

type Manifest struct {
	CapacityBytes     *int64               `json:"capacity_bytes"`
	LogicalLimitBytes *int64               `json:"logical_limit_bytes"`
	Version           int                  `json:"version"`
	OperationID       string               `json:"operation_id"`
	CreatedAt         time.Time            `json:"created_at"`
	Phase             string               `json:"phase"`
	LegacyRoot        string               `json:"legacy_root"`
	DefaultBackendID  string               `json:"default_backend_id"`
	DefaultRoot       string               `json:"default_root"`
	LegacySpaceID     int                  `json:"legacy_space_id,omitempty"`
	DefaultSpaceID    int                  `json:"default_space_id,omitempty"`
	KnownBytes        int64                `json:"known_bytes"`
	UnknownObjects    int                  `json:"unknown_objects"`
	Entries           []Entry              `json:"entries"`
	Projects          []ProjectCheckpoint  `json:"projects"`
	Jobs              []JobCheckpoint      `json:"jobs"`
	LegacyCleanup     []CleanupObservation `json:"legacy_cleanup,omitempty"`
	Warnings          []string             `json:"warnings"`
}

// CleanupObservation 是诊断证据，绝不是已授权的删除
// 请求，也不是可信的来源标识。原始业务行可能已不存在。
type CleanupObservation struct {
	TaskID         int    `json:"task_id"`
	OperationID    string `json:"operation_id"`
	ProjectID      int    `json:"project_id"`
	ResourceID     int    `json:"resource_id"`
	OwnerKind      string `json:"owner_kind"`
	OwnerID        int    `json:"owner_id"`
	StoragePath    string `json:"storage_path"`
	ObservedSize   *int64 `json:"observed_size,omitempty"`
	ObservedSHA256 string `json:"observed_sha256,omitempty"`
	Evidence       string `json:"evidence"`
	Rejected       bool   `json:"rejected"`
}

type Entry struct {
	ResourceID            int    `json:"resource_id"`
	ProjectID             int    `json:"project_id"`
	Path                  string `json:"path"`
	Format                string `json:"format"`
	StoragePath           string `json:"storage_path"`
	ObjectKey             string `json:"object_key"`
	DatabaseDigest        string `json:"database_digest"`
	ObservedSize          *int64 `json:"observed_size,omitempty"`
	ObservedSHA256        string `json:"observed_sha256,omitempty"`
	Verification          string `json:"verification"`
	Evidence              string `json:"evidence"`
	Integrity             string `json:"integrity"`
	Rejected              bool   `json:"rejected"`
	BlobID                int    `json:"blob_id,omitempty"`
	LocationID            int    `json:"location_id,omitempty"`
	RevisionID            int    `json:"revision_id,omitempty"`
	Applied               bool   `json:"applied"`
	SourceGeneration      int64  `json:"source_generation"`
	TranslationGeneration int64  `json:"translation_generation"`
}

type ProjectCheckpoint struct {
	ID               int   `json:"id"`
	PreviousSpaceID  *int  `json:"previous_space_id,omitempty"`
	Generation       int64 `json:"generation"`
	OutputGeneration int64 `json:"output_generation"`
	ResourceIDs      []int `json:"resource_ids"`
	Applied          bool  `json:"applied"`
}

type JobCheckpoint struct {
	ID     int     `json:"id"`
	Status string  `json:"status"`
	Error  *string `json:"error,omitempty"`
}

func newID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ReadManifest(path string) (*Manifest, error) {
	var manifest Manifest
	if err := operation.Read(path, &manifest); err != nil {
		return nil, fmt.Errorf("read storage manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *Manifest) validate() error {
	if m.Version != ManifestVersion || len(m.OperationID) != 32 {
		return errors.New("unsupported storage manifest")
	}
	if _, err := hex.DecodeString(m.OperationID); err != nil {
		return errors.New("invalid storage operation identity")
	}
	if !filepath.IsAbs(m.LegacyRoot) || !filepath.IsAbs(m.DefaultRoot) || m.DefaultBackendID == "" || m.DefaultBackendID == "legacy" {
		return errors.New("manifest must identify distinct legacy and default local backends")
	}
	if rootsOverlap(m.LegacyRoot, m.DefaultRoot) {
		return errors.New("legacy and default object roots must be separate, non-nested directories")
	}
	seen := map[int]bool{}
	for _, entry := range m.Entries {
		if entry.ResourceID <= 0 || (!entry.Rejected && entry.ProjectID <= 0) || seen[entry.ResourceID] {
			return errors.New("invalid or duplicate manifest resource")
		}
		seen[entry.ResourceID] = true
		if entry.ObjectKey != strings.ReplaceAll(entry.StoragePath, "\\", "/") {
			return errors.New("manifest object key differs from original storage path")
		}
		if entry.Verification != "verified" && entry.Verification != "legacy_unverified" {
			return errors.New("invalid source verification state")
		}
	}
	return nil
}

func (m *Manifest) Validate() error { return m.validate() }

func rootsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		left, right = strings.ToLower(left), strings.ToLower(right)
	}
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// SaveManifest 采用同级检查点、文件同步与原子替换。它
// 绝不包含凭据、已翻译文本或明文数据库 DSN。
func SaveManifest(path string, manifest *Manifest, create bool) error {
	if err := manifest.validate(); err != nil {
		return err
	}
	if !create {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			create = true
		}
	}
	return operation.Write(path, manifest, create)
}
