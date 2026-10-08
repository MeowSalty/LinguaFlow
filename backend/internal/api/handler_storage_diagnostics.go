package api

import (
	"net/http"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func (s *Server) GetStorageDiagnostics(w http.ResponseWriter, r *http.Request, params GetStorageDiagnosticsParams) {
	s.storageRequest(w, r, true, func(w http.ResponseWriter, r *http.Request, actor int) {
		cursor, limit := 0, 50
		if params.Cursor != nil {
			cursor = *params.Cursor
		}
		if params.Limit != nil {
			limit = *params.Limit
		}
		result, err := s.storageSvc.Diagnostics(r.Context(), actor, cursor, limit)
		if err != nil {
			s.writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toStorageDiagnosticsResponse(result))
	})
}

// toStorageDiagnosticsResponse 映射到 OpenAPI 生成的响应类型。
// 可选字段（next_cursor、oldest_intent_at、latest_backup、last_checked_at）
// 的"无值即省略"语义由生成类型的 omitempty 保证，禁止绕过它直接序列化 service 结构体。
func toStorageDiagnosticsResponse(in *service.StorageDiagnostics) StorageDiagnostics {
	out := StorageDiagnostics{
		BlockedCleanupByCode: in.BlockedCleanupByCode,
		Disks:                make([]StorageDiskDiagnostic, 0, len(in.Disks)),
		MigrationsByPhase:    in.MigrationsByPhase,
		NextCursor:           in.NextCursor,
		OldestIntentAt:       in.OldestIntentAt,
		RecoveryBacklog:      in.RecoveryBacklog,
		Spaces:               make([]StorageSpaceDiagnostics, 0, len(in.Spaces)),
		TemporaryBytes:       in.TemporaryBytes,
	}
	for _, disk := range in.Disks {
		out.Disks = append(out.Disks, StorageDiskDiagnostic{
			AvailableBytes:   disk.AvailableBytes,
			MinimumFreeBytes: disk.MinimumFreeBytes,
			ObservedAt:       disk.ObservedAt,
			Roles:            disk.Roles,
			State:            StorageDiskDiagnosticState(disk.State),
			TotalBytes:       disk.TotalBytes,
		})
	}
	for _, space := range in.Spaces {
		out.Spaces = append(out.Spaces, StorageSpaceDiagnostics{
			CandidateBytes:     space.CandidateBytes,
			CorruptObjects:     space.CorruptObjects,
			Id:                 space.ID,
			LastCheckedAt:      space.LastCheckedAt,
			LiveBytes:          space.LiveBytes,
			MissingObjects:     space.MissingObjects,
			PendingDeleteBytes: space.PendingDeleteBytes,
			ReservedBytes:      space.ReservedBytes,
			UncheckedObjects:   space.UncheckedObjects,
		})
	}
	if in.LatestBackup != nil {
		out.LatestBackup = &struct {
			CreatedAt time.Time                            `json:"created_at"`
			Id        int                                  `json:"id"`
			Status    StorageDiagnosticsLatestBackupStatus `json:"status"`
		}{
			CreatedAt: in.LatestBackup.CreatedAt,
			Id:        in.LatestBackup.ID,
			Status:    StorageDiagnosticsLatestBackupStatus(in.LatestBackup.Status),
		}
	}
	return out
}
