package service

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagebackup"
)

// CreateBackupManifest 记录仅含元数据的备份与持久的精确位置
// 固定记录。完整的数据库/密钥环捕获由独立的离线 CLI 执行。
func (s *StorageService) CreateBackupManifest(ctx context.Context, actor int, expires time.Time) (*ent.StorageBackup, error) {
	admin, err := s.client.User.Query().Where(user.IDEQ(actor), user.RoleEQ(SystemRoleAdmin), user.ActiveEQ(true)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !admin {
		return nil, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.maintenance {
		return nil, ErrStorageMaintenance
	}
	row, _, err := storagebackup.CaptureMetadata(ctx, s.client, expires)
	return row, err
}
