package storagemigrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
)

type Options struct {
	LegacyRoot        string
	DefaultRoot       string
	DefaultBackendID  string
	MaxFileBytes      int64
	CapacityBytes     *int64
	LogicalLimitBytes *int64
	CapacitySet       bool
	LogicalSet        bool
	Local             bool
	Offline           bool
	BackupConfirmed   bool
}

type Migrator struct {
	db      *sql.DB
	client  *ent.Client
	dialect string
	options Options
}

func New(db *sql.DB, client *ent.Client, dialect string, options Options) (*Migrator, error) {
	if db == nil || client == nil {
		return nil, errors.New("storage migration requires database and ORM clients")
	}
	if dialect != "sqlite" && dialect != "postgres" {
		return nil, errors.New("unsupported database driver")
	}
	var err error
	options.LegacyRoot, err = filepath.Abs(options.LegacyRoot)
	if err != nil {
		return nil, err
	}
	options.DefaultRoot, err = filepath.Abs(options.DefaultRoot)
	if err != nil {
		return nil, err
	}
	if options.DefaultBackendID == "" {
		options.DefaultBackendID = "local"
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = 100 << 20
	}
	row, policyErr := client.SystemSetting.Query().Where(systemsetting.KeyEQ("storage_policy")).Only(context.Background())
	if policyErr == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row.Value), &fields); err != nil {
			return nil, err
		}
		if value, ok := fields["default_space_capacity_bytes"]; ok {
			if err := json.Unmarshal(value, &options.CapacityBytes); err != nil {
				return nil, err
			}
		} else if !options.Local && !options.CapacitySet {
			return nil, errors.New("storage migration requires missing default space quota choice")
		}
		if value, ok := fields["logical_limit_bytes"]; ok {
			if err := json.Unmarshal(value, &options.LogicalLimitBytes); err != nil {
				return nil, err
			}
		} else if !options.Local && !options.LogicalSet {
			return nil, errors.New("storage migration requires missing logical quota choice")
		}
	} else if ent.IsNotFound(policyErr) {
		if !options.Local && (!options.CapacitySet || !options.LogicalSet) {
			return nil, errors.New("storage migration requires both initialization quota choices for serve")
		}
		if !options.CapacitySet {
			options.CapacityBytes = nil
		}
		if !options.LogicalSet {
			options.LogicalLimitBytes = nil
		}
	} else {
		return nil, policyErr
	}

	for _, n := range []*int64{options.CapacityBytes, options.LogicalLimitBytes} {
		if n != nil && (*n <= 0 || *n > 1<<53-1) {
			return nil, errors.New("invalid storage initialization quota")
		}
	}
	if rootsOverlap(options.DefaultRoot, options.LegacyRoot) || options.DefaultBackendID == "legacy" {
		return nil, errors.New("legacy root and default local storage must be separate, non-nested directories")
	}
	return &Migrator{db: db, client: client, dialect: dialect, options: options}, nil
}

type segmentSnapshot struct {
	ID       int
	Index    int
	Source   string
	Target   *string
	Status   string
	Review   *string
	Meta     *string
	Quality  any
	Reviewer *int
}

type resourceSnapshot struct {
	ID, ProjectID, Total      int
	Path, Format, StoragePath string
	Segments                  []segmentSnapshot
}

// Inventory 只读取旧列，因此在增量存储 schema
// 创建之前也能工作。它绝不扫描目录，也不改写解析器输出。
func (m *Migrator) Inventory(ctx context.Context) (*Manifest, error) {
	operation, err := newID()
	if err != nil {
		return nil, err
	}
	manifest := &Manifest{Version: ManifestVersion, OperationID: operation, CreatedAt: time.Now().UTC(), Phase: "inventoried", LegacyRoot: m.options.LegacyRoot, DefaultRoot: m.options.DefaultRoot, DefaultBackendID: m.options.DefaultBackendID, CapacityBytes: m.options.CapacityBytes, LogicalLimitBytes: m.options.LogicalLimitBytes,
		Warnings: []string{"Stop all service writers and back up the database and keyring before apply.", "Configure deployment backend legacy with the exact legacy_root; do not point it at objects.", "parser_version remains legacy_unknown; observed hashes cannot prove bytes were not replaced before inventory.", "Rollback reverses unchanged migration metadata only; restoring an old binary also requires its compatible database backup."}}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	columns, err := tableColumns(ctx, tx, "resources")
	if err != nil {
		return nil, err
	}
	generation := "0,0"
	filter := ""
	if columns["source_generation"] {
		generation = "source_generation,translation_generation"
	}
	if columns["current_source_revision_id"] {
		filter = " WHERE current_source_revision_id IS NULL"
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,project_id,path,format,storage_path,total_segments,"+generation+" FROM resources"+filter+" ORDER BY id")
	if err != nil {
		return nil, err
	}
	var snapshots []resourceSnapshot
	for rows.Next() {
		var row resourceSnapshot
		var projectID sql.NullInt64
		var sourceGeneration, translationGeneration int64
		if err := rows.Scan(&row.ID, &projectID, &row.Path, &row.Format, &row.StoragePath, &row.Total, &sourceGeneration, &translationGeneration); err != nil {
			rows.Close()
			return nil, err
		}
		row.ProjectID = int(projectID.Int64)
		snapshots = append(snapshots, row)
		manifest.Entries = append(manifest.Entries, Entry{ResourceID: row.ID, ProjectID: row.ProjectID, Path: row.Path, Format: row.Format, StoragePath: row.StoragePath, ObjectKey: strings.ReplaceAll(row.StoragePath, "\\", "/"), SourceGeneration: sourceGeneration, TranslationGeneration: translationGeneration, Verification: "legacy_unverified", Integrity: "unknown"})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	root, rootErr := localstore.OpenExisting(m.options.LegacyRoot)
	if rootErr != nil && !errors.Is(rootErr, storage.ErrNotFound) {
		return nil, fmt.Errorf("legacy root is unsafe or unreadable: %w", rootErr)
	}
	if root != nil {
		defer root.Close()
	}
	projectSeen := map[int]bool{}
	keyCounts := map[string]int{}
	for _, entry := range manifest.Entries {
		key := entry.ObjectKey
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		keyCounts[key]++
	}
	for index := range snapshots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := &snapshots[index]
		entry := &manifest.Entries[index]
		row.Segments, err = m.readSegments(ctx, tx, row.ID)
		if err != nil {
			return nil, err
		}
		entry.DatabaseDigest = digest(row)
		if row.ProjectID <= 0 {
			entry.Rejected = true
			entry.Evidence = "resource_has_no_project"
			continue
		}
		if err := storeutil.ValidateKey(entry.ObjectKey); err != nil {
			entry.Rejected = true
			entry.Evidence = "unsafe_storage_path"
			continue
		}
		key := entry.ObjectKey
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if keyCounts[key] > 1 {
			entry.Rejected = true
			entry.Evidence = "shared_storage_path_unproven"
			continue
		}
		if root == nil {
			entry.Integrity = "missing"
			entry.Evidence = "legacy_root_missing"
		} else {
			m.inspectFile(ctx, root, entry, row.Segments)
		}
		if entry.Rejected {
			continue
		}
		if !projectSeen[row.ProjectID] {
			manifest.Projects = append(manifest.Projects, ProjectCheckpoint{ID: row.ProjectID})
			projectSeen[row.ProjectID] = true
		}
		if entry.ObservedSize != nil {
			manifest.KnownBytes += *entry.ObservedSize
		} else {
			manifest.UnknownObjects++
		}
	}
	projectColumns, err := tableColumns(ctx, tx, "projects")
	if err != nil {
		return nil, err
	}
	for i := range manifest.Projects {
		ids, err := tx.QueryContext(ctx, "SELECT id FROM resources WHERE project_id="+m.bind(1)+" ORDER BY id", manifest.Projects[i].ID)
		if err != nil {
			return nil, err
		}
		for ids.Next() {
			var id int
			if err := ids.Scan(&id); err != nil {
				ids.Close()
				return nil, err
			}
			manifest.Projects[i].ResourceIDs = append(manifest.Projects[i].ResourceIDs, id)
		}
		if err := ids.Err(); err != nil {
			ids.Close()
			return nil, err
		}
		ids.Close()
		if projectColumns["storage_space_id"] {
			var space sql.NullInt64
			err = tx.QueryRowContext(ctx, "SELECT storage_space_id,storage_generation,output_generation FROM projects WHERE id="+m.bind(1), manifest.Projects[i].ID).Scan(&space, &manifest.Projects[i].Generation, &manifest.Projects[i].OutputGeneration)
			if err != nil {
				return nil, err
			}
			if space.Valid {
				value := int(space.Int64)
				manifest.Projects[i].PreviousSpaceID = &value
			}
		}
	}
	eligible := map[int]bool{}
	for _, entry := range manifest.Entries {
		if !entry.Rejected {
			eligible[entry.ResourceID] = true
		}
	}
	seenJobs := map[int]bool{}
	rows, err = tx.QueryContext(ctx, "SELECT j.id,j.status,j.error_message,jr.resource_job_resources FROM jobs j JOIN job_resources jr ON jr.job_job_resources=j.id WHERE j.status IN ('pending','running','paused') ORDER BY j.id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var job JobCheckpoint
		var message sql.NullString
		var resourceID int
		if err = rows.Scan(&job.ID, &job.Status, &message, &resourceID); err != nil {
			rows.Close()
			return nil, err
		}
		if !eligible[resourceID] || seenJobs[job.ID] {
			continue
		}
		seenJobs[job.ID] = true
		job.Error = nullableString(message)
		manifest.Jobs = append(manifest.Jobs, job)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	manifest.LegacyCleanup, err = m.inventoryCleanup(ctx, tx, root)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return manifest, nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table+" WHERE 1=0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, name := range names {
		result[name] = true
	}
	return result, nil
}

func (m *Migrator) bind(index int) string {
	if m.dialect == "postgres" {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func (m *Migrator) readSegments(ctx context.Context, tx *sql.Tx, resourceID int) ([]segmentSnapshot, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,segment_index,source_text,target_text,status,review_comment,meta,quality_issues,user_reviewed_segments FROM segments WHERE resource_id="+m.bind(1)+" ORDER BY id", resourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []segmentSnapshot
	for rows.Next() {
		var item segmentSnapshot
		var target, review, meta, quality sql.NullString
		var reviewer sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Index, &item.Source, &target, &item.Status, &review, &meta, &quality, &reviewer); err != nil {
			return nil, err
		}
		item.Target = nullableString(target)
		item.Review = nullableString(review)
		item.Meta = nullableString(meta)
		if quality.Valid {
			item.Quality = canonicalJSON([]byte(quality.String))
		}
		if reviewer.Valid {
			value := int(reviewer.Int64)
			item.Reviewer = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
func canonicalJSON(data []byte) any {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return string(data)
	}
	return value
}

func snapshotEnt(ctx context.Context, client *ent.Client, row *ent.Resource) (resourceSnapshot, error) {
	result := resourceSnapshot{ID: row.ID, Path: row.Path, Format: row.Format, StoragePath: row.StoragePath, Total: row.TotalSegments}
	if row.ProjectID != nil {
		result.ProjectID = *row.ProjectID
	}
	segments, err := client.Segment.Query().Where(segment.ResourceIDEQ(row.ID)).Order(ent.Asc(segment.FieldID)).WithReviewedBy().All(ctx)
	if err != nil {
		return result, err
	}
	for _, s := range segments {
		qa, _ := json.Marshal(s.QualityIssues)
		item := segmentSnapshot{ID: s.ID, Index: s.SegmentIndex, Source: s.SourceText, Target: s.TargetText, Status: string(s.Status), Review: s.ReviewComment, Meta: s.Meta, Quality: canonicalJSON(qa)}
		if s.Edges.ReviewedBy != nil {
			id := s.Edges.ReviewedBy.ID
			item.Reviewer = &id
		}
		result.Segments = append(result.Segments, item)
	}
	return result, nil
}

func (m *Migrator) inspectFile(ctx context.Context, root *localstore.Store, entry *Entry, segments []segmentSnapshot) {
	entry.ObservedSize = nil
	entry.ObservedSHA256 = ""
	entry.Integrity = "unknown"
	entry.Verification = "legacy_unverified"
	object := storage.Object{Key: entry.ObjectKey}
	stat, err := root.Stat(ctx, object)
	if errors.Is(err, storage.ErrNotFound) {
		entry.Integrity = "missing"
		entry.Evidence = "original_missing"
		return
	}
	if errors.Is(err, storage.ErrInvalidKey) {
		entry.Rejected = true
		entry.Evidence = "link_or_special_file"
		return
	}
	if err != nil {
		entry.Evidence = "original_unreadable"
		return
	}
	if stat.Size > m.options.MaxFileBytes {
		entry.Rejected = true
		entry.Evidence = "file_exceeds_inventory_limit"
		return
	}
	file, err := root.Open(ctx, object)
	if err != nil {
		entry.Evidence = "original_unreadable"
		return
	}
	defer file.Close()
	hash := sha256.New()
	var content bytes.Buffer
	var destination io.Writer = hash
	if entry.Format == "txt" {
		destination = io.MultiWriter(hash, &content)
	}
	size, err := io.Copy(destination, io.LimitReader(file, m.options.MaxFileBytes+1))
	if err != nil || size != stat.Size || size > m.options.MaxFileBytes {
		entry.Evidence = "original_changed_during_inventory"
		return
	}
	entry.ObservedSize = &size
	entry.ObservedSHA256 = hex.EncodeToString(hash.Sum(nil))
	entry.Integrity = "available"
	entry.Evidence = "current_bytes_hashed_historical_mapping_unproven"
	if entry.Format == "txt" && verifyTextLocations(content.String(), segments) {
		entry.Verification = "verified"
		entry.Evidence = "existing_pos_lines_cover_all_nonempty_lines_and_match_source"
	}
}

// 直接校验现有元数据；不要调用当前的解析器，再
// 假装其版本描述了历史解析操作。
func verifyTextLocations(content string, segments []segmentSnapshot) bool {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	covered := make([]bool, len(lines))
	for _, segment := range segments {
		if segment.Meta == nil {
			return false
		}
		var metadata struct {
			Positions []int `json:"pos_lines"`
		}
		if json.Unmarshal([]byte(*segment.Meta), &metadata) != nil || len(metadata.Positions) != 2 {
			return false
		}
		first, last := metadata.Positions[0]-1, metadata.Positions[1]
		if first < 0 || last > len(lines) || first >= last {
			return false
		}
		if strings.TrimSpace(strings.Join(lines[first:last], "\n")) != segment.Source {
			return false
		}
		for i := first; i < last; i++ {
			if covered[i] {
				return false
			}
			covered[i] = true
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) != "" && !covered[i] {
			return false
		}
	}
	return true
}
