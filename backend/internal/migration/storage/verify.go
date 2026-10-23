package storagemigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

// Baseline protects all preexisting rows, including full SourceRevision DTOs.
// Only the declared import's resource/project/job columns can change. Storage
// facts appended by the import are verified against its durable operation ID.
func Baseline(ctx context.Context, db *sql.DB, manifest *Manifest, maxIDs map[string]int64) (string, map[string]int64, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()
	if maxIDs == nil {
		maxIDs = map[string]int64{}
		for _, table := range []string{"blobs", "blob_locations", "source_revisions", "storage_connections", "storage_spaces", "storage_tasks", "system_settings"} {
			var max int64
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM "+table).Scan(&max); err != nil {
				return "", nil, err
			}
			maxIDs[table] = max
		}
	}
	resources, projects, jobs := map[int]bool{}, map[int]bool{}, map[int]bool{}
	for _, e := range manifest.Entries {
		if !e.Rejected {
			resources[e.ResourceID] = true
		}
	}
	for _, p := range manifest.Projects {
		projects[p.ID] = true
	}
	for _, j := range manifest.Jobs {
		jobs[j.ID] = true
	}
	var legacySpace int
	if err := tx.QueryRowContext(ctx, "SELECT sp.id FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.backend_id='legacy' AND co.owner_kind='site'").Scan(&legacySpace); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	h := sha256.New()
	for _, table := range migrate.Tables {
		columns, order := []string{}, []string{}
		for _, c := range table.Columns {
			columns = append(columns, `"`+c.Name+`"`)
		}
		for _, c := range table.PrimaryKey {
			order = append(order, `"`+c.Name+`"`)
		}
		query := "SELECT " + strings.Join(columns, ",") + " FROM " + `"` + table.Name + `"`
		if max, ok := maxIDs[table.Name]; ok {
			query += fmt.Sprintf(" WHERE id<=%d", max)
		}
		query += " ORDER BY " + strings.Join(order, ",")
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return "", nil, err
		}
		h.Write([]byte(table.Name + ":"))
		for rows.Next() {
			values := make([]any, len(columns))
			refs := make([]any, len(columns))
			for i := range values {
				refs[i] = &values[i]
			}
			if err := rows.Scan(refs...); err != nil {
				rows.Close()
				return "", nil, err
			}
			id := baselineID(values[0])
			ownTask := false
			storagePolicy := false
			if table.Name == "system_settings" {
				for i, c := range table.Columns {
					if c.Name == "key" && values[i] == "storage_policy" {
						storagePolicy = true
					}
				}
			}
			if table.Name == "storage_tasks" {
				for i, c := range table.Columns {
					if c.Name == "operation_id" && values[i] == manifest.OperationID {
						ownTask = true
					}
				}
			}
			for i, c := range table.Columns {
				if storagePolicy && c.Name == "value" {
					value, ok := values[i].(string)
					if !ok {
						rows.Close()
						return "", nil, ErrChanged
					}
					canonical, _, err := CanonicalPolicy(value, manifest.CapacityBytes, manifest.LogicalLimitBytes)
					if err != nil {
						rows.Close()
						return "", nil, err
					}
					values[i] = canonical
				}
				if storagePolicy && c.Name == "updated_at" {
					values[i] = nil
					continue
				}
				if (table.Name == "resources" && resources[id] && (c.Name == "current_source_revision_id" || c.Name == "source_generation" || c.Name == "updated_at")) || (table.Name == "projects" && projects[id] && (c.Name == "storage_space_id" || c.Name == "storage_generation" || c.Name == "storage_state" || c.Name == "updated_at")) || (table.Name == "jobs" && jobs[id] && (c.Name == "status" || c.Name == "error_message" || c.Name == "updated_at")) || (ownTask && (c.Name == "input" || c.Name == "status" || c.Name == "phase" || c.Name == "error_code" || c.Name == "updated_at")) || (table.Name == "storage_spaces" && legacySpace == id && (c.Name == "live_bytes" || c.Name == "updated_at")) {
					values[i] = nil
					continue
				}
				if b, ok := values[i].([]byte); ok {
					values[i] = struct{ Bytes string }{hex.EncodeToString(b)}
				}
				if v, ok := values[i].(time.Time); ok {
					values[i] = v.UTC().Format(time.RFC3339Nano)
				}
			}
			data, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return "", nil, err
			}
			fmt.Fprintf(h, "%d:", len(data))
			h.Write(data)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return "", nil, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), maxIDs, nil
}

func baselineID(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

// Verify reads the authoritative resource receipts without modifying a
// checkpoint. Later edits, task snapshots or pins retain rollback's refusal.
func (m *Migrator) Verify(ctx context.Context, manifest *Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	op, err := m.client.StorageTask.Query().Where(storagetask.OperationIDEQ(manifest.OperationID)).Only(ctx)
	if err != nil {
		return err
	}
	if op.Kind != "legacy_migration" || op.RequestHash != Fingerprint(manifest) || op.Phase != "committed" || op.Status != storagetask.StatusCompleted {
		return ErrChanged
	}
	copy := *manifest
	copy.Entries = append([]Entry(nil), manifest.Entries...)
	copy.LegacySpaceID = number(op.Input["legacy_space_id"])
	copy.DefaultSpaceID = number(op.Input["default_space_id"])
	for i := range copy.Entries {
		e := &copy.Entries[i]
		if e.Rejected {
			continue
		}
		if err := m.checkRollbackEntry(ctx, m.client, &copy, e); err != nil {
			return err
		}
		if !e.Applied {
			return ErrChanged
		}
	}
	for _, prior := range copy.Projects {
		p, err := m.client.Project.Get(ctx, prior.ID)
		if err != nil {
			return err
		}
		expected := prior.Generation + 1
		if number(op.Input["generation_protocol"]) >= 2 {
			expected++
		}
		if p.StorageState != "active" || p.StorageGeneration != expected || p.OutputGeneration != prior.OutputGeneration {
			return ErrChanged
		}
	}
	for _, prior := range copy.Jobs {
		job, err := m.client.Job.Get(ctx, prior.ID)
		if err != nil {
			return err
		}
		if job.Status != "failed" || job.ErrorMessage == nil || *job.ErrorMessage != blockedJobError {
			return ErrChanged
		}
	}
	return nil
}
