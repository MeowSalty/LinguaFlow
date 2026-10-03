package service

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// ValidateCurrentKeys 仅对活动密文执行本地认证。
// 存储授权损坏只会隔离其所属连接；它不会放宽
// 现有的 LLM 启动校验，也不会拖垮无关的健康连接。
func (s *StorageConnectionService) ValidateCurrentKeys(ctx context.Context) (unavailable int, err error) {
	last := 0
	for {
		rows, e := s.client.StorageConnection.Query().Where(storageconnection.IDGT(last), storageconnection.AuthSourceEQ(storageconnection.AuthSourceStored), storageconnection.ActiveAuthGenerationGT(0)).Order(ent.Asc(storageconnection.FieldID)).Limit(100).All(ctx)
		if e != nil {
			return unavailable, e
		}
		if len(rows) == 0 {
			return unavailable, nil
		}
		for _, c := range rows {
			last = c.ID
			if ctx.Err() != nil {
				return unavailable, ctx.Err()
			}
			if _, _, e = s.loadAuth(ctx, c, c.ActiveAuthGeneration, storageauthversion.StatusActive); e != nil {
				unavailable++
				health := "crypto_unavailable"
				if errors.Is(e, storage.ErrAuthRequired) {
					health = "auth_required"
				} else if !errors.Is(e, ErrStorageCrypto) {
					return unavailable, e
				}
				if _, e = s.client.StorageConnection.Update().Where(storageconnection.IDEQ(c.ID), storageconnection.ActiveAuthGenerationEQ(c.ActiveAuthGeneration), storageconnection.ManagementGenerationEQ(c.ManagementGeneration)).SetHealth(health).Save(ctx); e != nil {
					return unavailable, e
				}
			}
		}
	}
}
