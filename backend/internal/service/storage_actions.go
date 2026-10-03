package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func interactiveStorageTask(kind string) bool {
	switch kind {
	case "upload", "source_update", "repair", "migration", "export":
		return true
	default:
		return false
	}
}

// TaskActions 同时投影当前写权限与任务状态。
// 提交动作时，各变更方法仍会重复进行授权检查。
func (s *StorageService) TaskActions(ctx context.Context, actor int, task *ent.StorageTask) []string {
	if task == nil {
		return []string{}
	}
	if _, err := s.projects.requireProjectAccess(ctx, actor, task.ProjectID, true); err != nil {
		return []string{}
	}
	return StorageAllowedActions(task)
}
