package storagemigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
)

func (m *Migrator) inventoryCleanup(ctx context.Context, tx *sql.Tx, root *localstore.Store) ([]CleanupObservation, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='storage_tasks')"
	if m.dialect == "postgres" {
		query = "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='storage_tasks')"
	}
	if err := tx.QueryRowContext(ctx, query).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,operation_id,project_id,resource_id,input FROM storage_tasks WHERE kind='legacy_cleanup' AND cleanup_status<>'done' ORDER BY id LIMIT 100001")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CleanupObservation
	for rows.Next() {
		if len(result) == 100000 {
			return nil, storage.ErrLimit
		}
		var item CleanupObservation
		var resourceID sql.NullInt64
		var payload []byte
		if err := rows.Scan(&item.TaskID, &item.OperationID, &item.ProjectID, &resourceID, &payload); err != nil {
			return nil, err
		}
		item.ResourceID = int(resourceID.Int64)
		var input struct {
			LegacyPath string `json:"legacy_path"`
			Format     string `json:"format"`
			OwnerKind  string `json:"owner_kind"`
			OwnerID    int    `json:"owner_id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			item.Rejected, item.Evidence = true, "invalid_legacy_cleanup_input"
			result = append(result, item)
			continue
		}
		item.StoragePath, item.OwnerKind, item.OwnerID = input.LegacyPath, input.OwnerKind, input.OwnerID
		key := strings.ReplaceAll(input.LegacyPath, "\\", "/")
		if err := storeutil.ValidateKey(key); err != nil {
			item.Rejected, item.Evidence = true, "unsafe_storage_path"
		} else if root == nil {
			item.Evidence = "legacy_root_missing"
		} else {
			// 只检查确切的持久化 key。当前哈希匹配并不能
			// 证明历史归属，因此这里绝不会触发物理清理。
			entry := Entry{ObjectKey: key, Format: input.Format}
			m.inspectFile(ctx, root, &entry, nil)
			item.ObservedSize, item.ObservedSHA256, item.Rejected = entry.ObservedSize, entry.ObservedSHA256, entry.Rejected
			item.Evidence = entry.Evidence
			if entry.Integrity == "available" {
				item.Evidence = "current_bytes_observed_ownership_unproven"
			}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
