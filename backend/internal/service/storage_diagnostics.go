package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagebackup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

type StorageDiagnostics struct {
	Spaces               []StorageSpaceDiagnostics `json:"spaces"`
	NextCursor           *int                      `json:"next_cursor"`
	TemporaryBytes       int64                     `json:"temporary_bytes"`
	OldestIntentAt       *time.Time                `json:"oldest_intent_at"`
	RecoveryBacklog      int                       `json:"recovery_backlog"`
	BlockedCleanupByCode map[string]int            `json:"blocked_cleanup_by_code"`
	MigrationsByPhase    map[string]int            `json:"migrations_by_phase"`
	LatestBackup         *StorageBackupDiagnostic  `json:"latest_backup"`
}

type StorageSpaceDiagnostics struct {
	ID                 int   `json:"id"`
	ReservedBytes      int64 `json:"reserved_bytes"`
	CandidateBytes     int64 `json:"candidate_bytes"`
	LiveBytes          int64 `json:"live_bytes"`
	PendingDeleteBytes int64 `json:"pending_delete_bytes"`
	UncheckedObjects   int   `json:"unchecked_objects"`
	MissingObjects     int   `json:"missing_objects"`
	CorruptObjects     int   `json:"corrupt_objects"`
	// LastCheckedAt 是最近一次持久化的成功全字节校验时间。
	// 在当前存储模型中，提供方失败没有专门的时间戳。
	LastCheckedAt *time.Time `json:"last_checked_at"`
}

type StorageBackupDiagnostic struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Diagnostics 只读取已登记的元数据。它绝不解析驱动、探测存储、
// 枚举目录，也不包含物理身份信息。
func (s *StorageService) Diagnostics(ctx context.Context, actor, afterSpaceID, limit int) (*StorageDiagnostics, error) {
	admin, err := s.client.User.Query().Where(user.IDEQ(actor), user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, ErrForbidden
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || afterSpaceID < 0 {
		return nil, ErrInvalidInput
	}
	var transaction *ent.Tx
	if s.dialect == "postgres" {
		transaction, err = s.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else {
		transaction, err = s.client.Tx(ctx)
	}
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	client := transaction.Client()
	result := &StorageDiagnostics{Spaces: make([]StorageSpaceDiagnostics, 0, limit), BlockedCleanupByCode: map[string]int{}, MigrationsByPhase: map[string]int{}}
	spaces, err := client.StorageSpace.Query().Where(storagespace.IDGT(afterSpaceID)).Select(storagespace.FieldID, storagespace.FieldReservedBytes, storagespace.FieldCandidateBytes, storagespace.FieldLiveBytes, storagespace.FieldPendingDeleteBytes).Order(ent.Asc(storagespace.FieldID)).Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(spaces) > limit {
		spaces = spaces[:limit]
		last := spaces[len(spaces)-1].ID
		result.NextCursor = &last
	}
	ids := make([]int, 0, len(spaces))
	positions := make(map[int]int, len(spaces))
	for _, space := range spaces {
		positions[space.ID] = len(result.Spaces)
		ids = append(ids, space.ID)
		result.Spaces = append(result.Spaces, StorageSpaceDiagnostics{ID: space.ID, ReservedBytes: space.ReservedBytes, CandidateBytes: space.CandidateBytes, LiveBytes: space.LiveBytes, PendingDeleteBytes: space.PendingDeleteBytes})
	}
	if len(ids) > 0 {
		var health []struct {
			SpaceID   int    `json:"space_id"`
			Integrity string `json:"integrity"`
			Count     int    `json:"count"`
		}
		err = client.BlobLocation.Query().Where(bloblocation.SpaceIDIn(ids...), bloblocation.StatusNEQ(bloblocation.StatusDeleted)).GroupBy(bloblocation.FieldSpaceID, bloblocation.FieldIntegrity).Aggregate(ent.Count()).Scan(ctx, &health)
		if err != nil {
			return nil, err
		}
		for _, row := range health {
			space := &result.Spaces[positions[row.SpaceID]]
			switch row.Integrity {
			case string(bloblocation.IntegrityUnknown):
				space.UncheckedObjects = row.Count
			case string(bloblocation.IntegrityMissing):
				space.MissingObjects = row.Count
			case string(bloblocation.IntegrityCorrupt):
				space.CorruptObjects = row.Count
			}
		}
		var checked []struct {
			SpaceID       int            `json:"space_id"`
			LastCheckedAt diagnosticTime `json:"last_checked_at"`
		}
		err = client.BlobLocation.Query().Where(bloblocation.SpaceIDIn(ids...), bloblocation.StatusNEQ(bloblocation.StatusDeleted), bloblocation.VerifiedAtNotNil()).GroupBy(bloblocation.FieldSpaceID).Aggregate(ent.As(ent.Max(bloblocation.FieldVerifiedAt), "last_checked_at")).Scan(ctx, &checked)
		if err != nil {
			return nil, err
		}
		for _, row := range checked {
			result.Spaces[positions[row.SpaceID]].LastCheckedAt = row.LastCheckedAt.Value
		}
	}
	oldest, err := client.StorageWrite.Query().Where(storagewrite.PhaseNotIn("committed", "cleaned")).Select(storagewrite.FieldID, storagewrite.FieldCreatedAt).Order(ent.Asc(storagewrite.FieldCreatedAt), ent.Asc(storagewrite.FieldID)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if oldest != nil {
		result.OldestIntentAt = &oldest.CreatedAt
	}
	result.RecoveryBacklog, err = client.StorageTask.Query().Where(storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled)).Count(ctx)
	if err != nil {
		return nil, err
	}
	var blocked []struct {
		ErrorCode string `json:"error_code"`
		Count     int    `json:"count"`
	}
	err = client.DeletionEntry.Query().Where(deletionentry.StatusEQ(deletionentry.StatusBlocked)).GroupBy(deletionentry.FieldErrorCode).Aggregate(ent.Count()).Scan(ctx, &blocked)
	if err != nil {
		return nil, err
	}
	for _, row := range blocked {
		result.BlockedCleanupByCode[diagnosticCleanupCode(row.ErrorCode)] += row.Count
	}
	// 旧版墓碑记录刻意没有可执行的 location/删除条目。
	blocked = nil
	err = client.StorageTask.Query().Where(storagetask.KindEQ("legacy_cleanup"), storagetask.CleanupStatusEQ(storagetask.CleanupStatusBlocked)).GroupBy(storagetask.FieldErrorCode).Aggregate(ent.Count()).Scan(ctx, &blocked)
	if err != nil {
		return nil, err
	}
	for _, row := range blocked {
		result.BlockedCleanupByCode[diagnosticCleanupCode(row.ErrorCode)] += row.Count
	}
	var migrations []struct {
		Phase string `json:"phase"`
		Count int    `json:"count"`
	}
	err = client.StorageTask.Query().Where(storagetask.KindEQ("migration"), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled)).GroupBy(storagetask.FieldPhase).Aggregate(ent.Count()).Scan(ctx, &migrations)
	if err != nil {
		return nil, err
	}
	for _, row := range migrations {
		phase := row.Phase
		switch phase {
		case "accepted", "draining", "copy", "verify", "cutover", "cleanup":
		default:
			phase = "other"
		}
		result.MigrationsByPhase[phase] += row.Count
	}
	backup, err := client.StorageBackup.Query().Select(storagebackup.FieldID, storagebackup.FieldStatus, storagebackup.FieldCreatedAt).Order(ent.Desc(storagebackup.FieldCreatedAt), ent.Desc(storagebackup.FieldID)).First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if backup != nil {
		result.LatestBackup = &StorageBackupDiagnostic{ID: backup.ID, Status: string(backup.Status), CreatedAt: backup.CreatedAt}
	}
	if err = transaction.Commit(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	result.TemporaryBytes = s.tempBytes
	s.mu.Unlock()
	return result, nil
}

func diagnosticCleanupCode(code string) string {
	switch code {
	case "storage_payload_too_large", "storage_timeout", "storage_intent_expired", "storage_idempotency_conflict", "storage_operation_in_progress", "storage_crypto_unavailable", "storage_policy_violation", "storage_deployment_disabled", "invalid_input", "storage_parse_failed":
		return code
	case "source_missing", "source_corrupt", "storage_permission_denied", "storage_auth_required", "storage_quota_exceeded", "storage_unavailable", "storage_generation_conflict", "storage_transfer_interrupted", "legacy_location_unverified", "storage_retry_exhausted", "storage_cancelled", "storage_maintenance", "storage_file_too_large", "repair_content_mismatch", "storage_invalid_key", "storage_capability_unsupported", "source_revision_conflict":
		return code
	default:
		return "other"
	}
}

// SQLite 把时间戳聚合为字符串；PostgreSQL 返回 time.Time。
type diagnosticTime struct{ Value *time.Time }

func (t *diagnosticTime) Scan(value any) error {
	if value == nil {
		t.Value = nil
		return nil
	}
	if stamp, ok := value.(time.Time); ok {
		t.Value = &stamp
		return nil
	}
	var encoded string
	switch value := value.(type) {
	case string:
		encoded = value
	case []byte:
		encoded = string(value)
	default:
		return fmt.Errorf("unsupported diagnostic timestamp type %T", value)
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999"} {
		if stamp, err := time.Parse(layout, encoded); err == nil {
			t.Value = &stamp
			return nil
		}
	}
	return fmt.Errorf("invalid diagnostic timestamp")
}
