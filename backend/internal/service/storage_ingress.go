package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// BeginUpload 将请求解析与对象传输 worker 的限额分开控制。
// 保持独立的准入限制，可避免在等待嵌套的 Stage 调用取得
// 传输槽位时占满所有槽位。
func (s *ResourceService) BeginUpload(ctx context.Context) (context.Context, config.StorageLimits, func(), error) {
	if err := s.ensureStorage(ctx); err != nil {
		return ctx, config.StorageLimits{}, nil, err
	}
	if s.storage.maintenance {
		return ctx, config.StorageLimits{}, nil, ErrStorageMaintenance
	}
	select {
	case s.storage.ingressSlots <- struct{}{}:
	default:
		return ctx, config.StorageLimits{}, nil, storage.ErrLimit
	}
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	return ctx, s.storage.cfg.Limits, func() { cancel(); <-s.storage.ingressSlots }, nil
}

func (s *ResourceService) UploadBuffer(size int64) (*StorageFile, error) {
	return s.storage.temporary(size)
}

// Seal 在接收完有界流后释放未使用的预留。
func (f *StorageFile) Seal(size int64) error {
	if size < 0 || size > f.reserved {
		return storage.ErrLimit
	}
	f.s.mu.Lock()
	f.s.tempBytes -= f.reserved - size
	f.reserved = size
	f.s.mu.Unlock()
	return nil
}
