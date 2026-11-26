package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	entschema "entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// PersistenceFormatVersion changes only when an already published persistent
// representation changes. It is independent of account and timestamp formats.
const PersistenceFormatVersion = 1
const persistenceFormatTable = "linguaflow_persistence_format"

var ErrPersistenceFormatUnsupported = errors.New("unsupported persistence format; preserve the database and use a server supporting its recorded format")
var ErrPersistenceMigrationRequired = errors.New("persistence data migration required; stop other writers and start this version with auto_migrate=true")

type publishedQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func publishedColumns(ctx context.Context, q publishedQueryer, driver, table string) (map[string]bool, error) {
	query := "SELECT name FROM pragma_table_info($1)"
	if driver == "postgres" {
		query = "SELECT column_name FROM information_schema.columns WHERE table_schema=current_schema() AND table_name=$1"
	}
	rows, err := q.QueryContext(ctx, query, table)
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		columns[name] = true
	}
	return columns, errors.Join(err, rows.Err(), rows.Close())
}

// CheckPublishedDataFormat is a read-only preflight, before schema or business
// writes. Missing metadata denotes the supported, published pre-format data.
func CheckPublishedDataFormat(ctx context.Context, q publishedQueryer, driver string, autoMigrate bool) error {
	objectQuery := "SELECT type FROM sqlite_schema WHERE name=$1"
	if driver == "postgres" {
		objectQuery = "SELECT c.relkind::text FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname=$1"
	}
	objects, err := q.QueryContext(ctx, objectQuery, persistenceFormatTable)
	if err != nil {
		return err
	}
	invalid := false
	for objects.Next() {
		var kind string
		if err = objects.Scan(&kind); err != nil {
			break
		}
		if kind != "table" && kind != "r" {
			invalid = true
		}
	}
	if err := errors.Join(err, objects.Err(), objects.Close()); err != nil {
		return err
	}
	if invalid {
		return ErrPersistenceFormatUnsupported
	}
	columns, err := publishedColumns(ctx, q, driver, persistenceFormatTable)
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		if !autoMigrate {
			return ErrPersistenceMigrationRequired
		}
		return nil
	}
	if len(columns) != 2 || !columns["id"] || !columns["version"] {
		return ErrPersistenceFormatUnsupported
	}
	rows, err := q.QueryContext(ctx, "SELECT id,version FROM "+persistenceFormatTable+" LIMIT 2")
	if err != nil {
		return fmt.Errorf("%w: invalid marker", ErrPersistenceFormatUnsupported)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, version int
		if err := rows.Scan(&id, &version); err != nil || id != 1 || version != PersistenceFormatVersion {
			return ErrPersistenceFormatUnsupported
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return ErrPersistenceFormatUnsupported
	}
	return nil
}

const persistenceMarkerDDL = "CREATE TABLE " + persistenceFormatTable + " (id INTEGER NOT NULL PRIMARY KEY CHECK(id=1),version INTEGER NOT NULL CHECK(version>0))"

// COMPAT(sync-plan-v017): keep the unique published JSON plans until their
// conversion transaction commits. Atlas filters explicit DropColumn changes,
// but a SQLite rebuild still uses the desired table's complete column set.
// Tracking: backend/internal/migration/catalog/compatibility.md#sync-plan-v017
func CreatePublishedSchema(ctx context.Context, client *ent.Client, driver string) error {
	if err := CheckPublishedDataFormat(ctx, client, driver, true); err != nil {
		return err
	}
	columns, err := publishedColumns(ctx, client, driver, "sync_tasks")
	if err != nil {
		return err
	}
	if !columns["segment_ids"] && !columns["resource_ids"] {
		return client.Schema.Create(ctx)
	}
	if !columns["segment_ids"] || !columns["resource_ids"] {
		return errors.New("incomplete published sync task plan; preserve the database and restore its original plan before migration")
	}
	tables, err := entschema.CopyTables(migrate.Tables)
	if err != nil {
		return fmt.Errorf("copy published migration schema: %w", err)
	}
	for i, table := range tables {
		// CopyTables reconnects columns, indexes and foreign keys within the
		// copied graph. Keep the remaining exported table metadata as well.
		original := migrate.Tables[i]
		table.Schema, table.Comment, table.View, table.Pos = original.Schema, original.Comment, original.View, original.Pos
		if table.Name != "sync_tasks" {
			continue
		}
		for _, name := range []string{"segment_ids", "resource_ids"} {
			table.Columns = append(table.Columns, &entschema.Column{Name: name, Type: field.TypeString, Size: 2147483647})
		}
	}
	return migrate.Create(ctx, client.Schema, tables)
}

// COMPAT(sync-plan-v017): convert the published JSON checkpoint and binding
// evidence to the final persistence format once, before runtime recovery.
// Tracking: backend/internal/migration/catalog/compatibility.md#sync-plan-v017
// MigratePublishedData runs after CreatePublishedSchema has made the
// final child tables and expiry column available. Conversion, metadata and old
// column removal commit together; failure leaves the unique old plan intact.
func MigratePublishedData(ctx context.Context, client *ent.Client, driver string) (err error) {
	tx, err := client.Tx(WithJoinedRollback(ctx))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if err = MigratePublishedDataInTransaction(ctx, tx.Client(), driver); err != nil {
		return err
	}
	return tx.Commit()
}

// MigratePublishedDataInTransaction is for protected offline converters whose
// caller already owns a transaction (notably the supported v0.15 upgrade).
func MigratePublishedDataInTransaction(ctx context.Context, client *ent.Client, driver string) error {
	return migratePublishedDataInTransaction(ctx, client, driver, timeutil.NowUTC())
}

// MigratePublishedDataForUpgrade uses the protected operation's fixed time so
// source-baseline receipts can declare legacy recovery changes deterministically.
func MigratePublishedDataForUpgrade(ctx context.Context, client *ent.Client, driver string, at time.Time) error {
	return migratePublishedDataInTransaction(ctx, client, driver, timeutil.Normalize(at))
}

func migratePublishedDataInTransaction(ctx context.Context, client *ent.Client, driver string, at time.Time) error {
	if err := CheckPublishedDataFormat(ctx, client, driver, true); err != nil {
		return err
	}
	columns, err := publishedColumns(ctx, client, driver, persistenceFormatTable)
	if err != nil {
		return err
	}
	if len(columns) != 0 {
		return nil
	}
	// First write obtains SQLite's writer and serializes concurrent initializers.
	if _, err := client.ExecContext(ctx, persistenceMarkerDDL); err != nil {
		return err
	}
	if err := backfillBindingExpiry(ctx, client, driver); err != nil {
		return err
	}
	if err := convertPublishedSyncTasks(ctx, client, driver, at); err != nil {
		return err
	}
	if _, err := client.ExecContext(ctx, "INSERT INTO "+persistenceFormatTable+"(id,version) VALUES(1,$1)", PersistenceFormatVersion); err != nil {
		return err
	}
	return nil
}

func backfillBindingExpiry(ctx context.Context, client *ent.Client, driver string) error {
	columns, err := publishedColumns(ctx, client, driver, "login_bindings")
	if err != nil || len(columns) == 0 {
		return err
	}
	if !columns["evidence_expires_at"] {
		return errors.New("schema must create login_bindings.evidence_expires_at before data conversion")
	}
	after := 0
	for {
		rows, err := client.QueryContext(ctx, "SELECT id,evidence FROM login_bindings WHERE id>$1 ORDER BY id LIMIT 128", after)
		if err != nil {
			return err
		}
		type binding struct {
			id       int
			evidence sql.NullString
		}
		batch := make([]binding, 0, 128)
		for rows.Next() {
			var b binding
			if err = rows.Scan(&b.id, &b.evidence); err != nil {
				break
			}
			batch = append(batch, b)
		}
		if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, b := range batch {
			var evidence authrules.Evidence
			if b.evidence.Valid && b.evidence.String != "" {
				if err := json.Unmarshal([]byte(b.evidence.String), &evidence); err != nil {
					return fmt.Errorf("login binding %d has invalid evidence; preserve it and repair before migration", b.id)
				}
			}
			var expiry any
			if !evidence.ExpiresAt.IsZero() {
				instant := timeutil.Normalize(evidence.ExpiresAt)
				if driver == "postgres" {
					instant = timeutil.CeilMicrosecond(instant)
				}
				expiry = instant
			}
			if _, err := client.ExecContext(ctx, "UPDATE login_bindings SET evidence_expires_at=$1 WHERE id=$2", expiry, b.id); err != nil {
				return err
			}
			after = b.id
		}
	}
}

type publishedSyncTask struct {
	id, project, total, processed, checkpoint, next int
	status, ids, resources, result                  string
	started, cancelled                              sql.NullTime
}
type publishedSyncResource struct {
	ID      int    `json:"resource_id"`
	Path    string `json:"resource_path"`
	Updated int    `json:"updated_count"`
	Skipped int    `json:"skipped_count"`
}
type publishedSyncResult struct {
	Updated   int                      `json:"total_updated"`
	Skipped   int                      `json:"total_skipped"`
	Resources []*publishedSyncResource `json:"resources"`
}

// COMPAT(sync-plan-v017): v0.17.5 persisted immutable plan IDs and accumulated
// checkpoint results in JSON. This one-time decoder is retired only when those
// published databases no longer have a supported direct upgrade route.
// Tracking: backend/internal/migration/catalog/compatibility.md#sync-plan-v017
func decodePublishedSync(t publishedSyncTask) ([]int, []int, *publishedSyncResult, error) {
	var ids, resources []int
	if err := json.Unmarshal([]byte(t.ids), &ids); err != nil {
		return nil, nil, nil, err
	}
	if len(ids) == 0 || len(ids) != t.total {
		return nil, nil, nil, errors.New("invalid work list length")
	}
	for i, id := range ids {
		if id <= 0 || i > 0 && ids[i-1] >= id {
			return nil, nil, nil, errors.New("invalid ordered work list")
		}
	}
	if t.checkpoint != 1 || t.next < 0 || t.next > len(ids) || t.processed != t.next {
		return nil, nil, nil, errors.New("invalid checkpoint position")
	}
	if err := json.Unmarshal([]byte(t.resources), &resources); err != nil {
		return nil, nil, nil, err
	}
	for i, id := range resources {
		if id <= 0 || i > 0 && resources[i-1] >= id {
			return nil, nil, nil, errors.New("invalid resource lock set")
		}
	}
	var result *publishedSyncResult
	if err := json.Unmarshal([]byte(t.result), &result); err != nil {
		return nil, nil, nil, err
	}
	if result == nil || result.Updated < 0 || result.Skipped < 0 || result.Updated > t.next || result.Skipped != t.next-result.Updated {
		return nil, nil, nil, errors.New("invalid accumulated result")
	}
	seen := map[int]bool{}
	updated, skipped := 0, 0
	for _, row := range result.Resources {
		if row == nil || row.ID <= 0 || row.Updated < 0 || row.Skipped < 0 || seen[row.ID] {
			return nil, nil, nil, errors.New("invalid resource result")
		}
		at := sort.SearchInts(resources, row.ID)
		if at == len(resources) || resources[at] != row.ID || row.Updated > result.Updated-updated || row.Skipped > result.Skipped-skipped {
			return nil, nil, nil, errors.New("inconsistent resource result")
		}
		seen[row.ID] = true
		updated += row.Updated
		skipped += row.Skipped
	}
	if updated != result.Updated {
		return nil, nil, nil, errors.New("incomplete resource update result")
	}
	return ids, resources, result, nil
}

func convertPublishedSyncTasks(ctx context.Context, client *ent.Client, driver string, at time.Time) error {
	columns, err := publishedColumns(ctx, client, driver, "sync_tasks")
	if err != nil || len(columns) == 0 {
		return err
	}
	if !columns["segment_ids"] && !columns["resource_ids"] {
		// A final schema without its published source columns cannot recover
		// an active legacy checkpoint. Do not attest an empty conversion after
		// an interrupted or destructive schema preparation lost that source.
		rows, err := client.QueryContext(ctx, "SELECT COUNT(*) FROM sync_tasks WHERE status IN ('pending','running') AND checkpoint_version<>2")
		if err != nil {
			return err
		}
		var incomplete int
		if !rows.Next() {
			err = errors.New("cannot inspect active sync checkpoint formats")
		} else {
			err = rows.Scan(&incomplete)
		}
		if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return err
		}
		if incomplete != 0 {
			return errors.New("active legacy sync checkpoint has no published plan; preserve the database and restore its original plan or backup before migration")
		}
		return nil
	}
	if !columns["segment_ids"] || !columns["resource_ids"] || !columns["total_updated"] || !columns["total_skipped"] {
		return errors.New("incomplete sync task schema for published conversion")
	}
	for after := 0; ; {
		rows, err := client.QueryContext(ctx, "SELECT id,project_id,total_segments,processed_segments,checkpoint_version,next_segment_index,status,segment_ids,resource_ids,COALESCE(result,''),started_at,cancelled_at FROM sync_tasks WHERE id>$1 ORDER BY id LIMIT 1", after)
		if err != nil {
			return err
		}
		var task publishedSyncTask
		found := rows.Next()
		if found {
			err = rows.Scan(&task.id, &task.project, &task.total, &task.processed, &task.checkpoint, &task.next, &task.status, &task.ids, &task.resources, &task.result, &task.started, &task.cancelled)
		}
		if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return err
		}
		if !found {
			break
		}
		if err := convertPublishedSyncTask(ctx, client, task, at); err != nil {
			return fmt.Errorf("convert sync task %d: %w", task.id, err)
		}
		after = task.id
	}
	for _, column := range []string{"segment_ids", "resource_ids"} {
		if _, err := client.ExecContext(ctx, "ALTER TABLE sync_tasks DROP COLUMN "+column); err != nil {
			return err
		}
	}
	return nil
}

func publishedSegmentResources(ctx context.Context, client *ent.Client, project int, ids []int) (map[int]int, error) {
	result := make(map[int]int, len(ids))
	for start := 0; start < len(ids); start += 200 {
		part := ids[start:min(start+200, len(ids))]
		args := make([]any, 0, len(part)+1)
		args = append(args, project)
		placeholders := make([]string, len(part))
		for i, id := range part {
			args = append(args, id)
			placeholders[i] = fmt.Sprintf("$%d", i+2)
		}
		rows, err := client.QueryContext(ctx, "SELECT s.id,s.resource_id FROM segments s JOIN resources r ON r.id=s.resource_id WHERE r.project_id=$1 AND s.id IN ("+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, resource int
			if err = rows.Scan(&id, &resource); err != nil {
				break
			}
			result[id] = resource
		}
		if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return result, nil
}

const publishedSyncFailure = "术语同步恢复失败：缺少可信批次检查点，已提交内容保留，请重新分析后创建新任务"

// PublishedSyncCheckpoint is the frozen v0.15-v0.17 source contract needed by
// the protected converter's baseline recipe. No runtime service reads it.
type PublishedSyncCheckpoint struct {
	Status, SegmentIDs, ResourceIDs, Result                               string
	TotalSegments, ProcessedSegments, CheckpointVersion, NextSegmentIndex int
	Started, Cancelled                                                    bool
}

// PublishedSyncRecovery describes the same finite decision used by conversion,
// allowing offline baseline receipts to declare changed fields before writing.
func PublishedSyncRecovery(source PublishedSyncCheckpoint) (resume bool, message string) {
	task := publishedSyncTask{status: source.Status, ids: source.SegmentIDs, resources: source.ResourceIDs, result: source.Result, total: source.TotalSegments, processed: source.ProcessedSegments, checkpoint: source.CheckpointVersion, next: source.NextSegmentIndex}
	if task.checkpoint == 0 && task.status == "pending" && task.processed == 0 && task.next == 0 && task.result == "" && !source.Started && !source.Cancelled {
		task.checkpoint = 1
		task.resources = "[]"
		task.result = `{"resources":[]}`
	}
	_, _, _, err := decodePublishedSync(task)
	return err == nil, publishedSyncFailure
}

func convertPublishedSyncTask(ctx context.Context, client *ent.Client, task publishedSyncTask, at time.Time) error {
	active := task.status == "pending" || task.status == "running"
	ids, resources, result, decodeErr := decodePublishedSync(task)
	if task.checkpoint == 0 && task.status == "pending" && task.processed == 0 && task.next == 0 && task.result == "" && !task.started.Valid && !task.cancelled.Valid {
		candidate := task
		candidate.checkpoint = 1
		candidate.resources = "[]"
		candidate.result = `{"resources":[]}`
		ids, resources, result, decodeErr = decodePublishedSync(candidate)
		if decodeErr == nil {
			mapped, err := publishedSegmentResources(ctx, client, task.project, ids)
			if err != nil {
				return err
			}
			seen := map[int]bool{}
			for _, id := range mapped {
				seen[id] = true
			}
			for id := range seen {
				resources = append(resources, id)
			}
			sort.Ints(resources)
		}
	}
	if decodeErr != nil {
		if !active {
			return nil
		}
		now := at
		_, err := client.ExecContext(ctx, "UPDATE sync_tasks SET status='failed',error=$1,finished_at=$2,retention_anchor_at=$2,updated_at=$2 WHERE id=$3", publishedSyncFailure, now, task.id)
		return err
	}
	if !active {
		_, err := client.ExecContext(ctx, "UPDATE sync_tasks SET total_updated=$1,total_skipped=$2 WHERE id=$3", result.Updated, result.Skipped, task.id)
		return err
	}
	mapped, err := publishedSegmentResources(ctx, client, task.project, ids)
	if err != nil {
		return err
	}
	for id, resourceID := range mapped {
		at := sort.SearchInts(resources, resourceID)
		if at == len(resources) || resources[at] != resourceID {
			mapped[id] = 0
		}
	}
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		args := make([]any, 0, (end-start)*4)
		values := make([]string, 0, end-start)
		for ordinal := start; ordinal < end; ordinal++ {
			base := len(args)
			args = append(args, task.id, ordinal, ids[ordinal], mapped[ids[ordinal]])
			values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4))
		}
		if _, err := client.ExecContext(ctx, "INSERT INTO sync_task_items(task_id,ordinal,segment_id,resource_id) VALUES "+strings.Join(values, ","), args...); err != nil {
			return err
		}
	}
	byResource := map[int]*publishedSyncResource{}
	for _, row := range result.Resources {
		byResource[row.ID] = row
	}
	for start := 0; start < len(resources); start += 160 {
		end := min(start+160, len(resources))
		args := make([]any, 0, (end-start)*5)
		values := make([]string, 0, end-start)
		for _, id := range resources[start:end] {
			row := byResource[id]
			if row == nil {
				row = &publishedSyncResource{ID: id}
			}
			base := len(args)
			args = append(args, task.id, id, row.Path, row.Updated, row.Skipped)
			values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", base+1, base+2, base+3, base+4, base+5))
		}
		if _, err := client.ExecContext(ctx, "INSERT INTO sync_task_results(task_id,resource_id,resource_path,updated_count,skipped_count) VALUES "+strings.Join(values, ","), args...); err != nil {
			return err
		}
	}
	_, err = client.ExecContext(ctx, "UPDATE sync_tasks SET checkpoint_version=2,total_updated=$1,total_skipped=$2,result='' WHERE id=$3", result.Updated, result.Skipped, task.id)
	return err
}
