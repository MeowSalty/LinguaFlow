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
	actions := StorageAllowedActions(task)
	if len(actions) == 0 {
		return actions
	}
	p, err := s.client.Project.Get(ctx, task.ProjectID)
	if err != nil {
		return []string{}
	}
	valid := p.StorageGeneration == task.ExpectedStorageGeneration
	if task.Kind != "migration" && task.Kind != "repair" && p.StorageState != "active" {
		valid = false
	}
	if task.Kind == "source_update" && task.ResourceID != nil {
		r, e := s.client.Resource.Get(ctx, *task.ResourceID)
		if e != nil || r.SourceGeneration != task.ExpectedSourceGeneration || r.TranslationGeneration != task.ExpectedTranslationGeneration {
			valid = false
		}
	}
	if s.maintenance {
		valid = false
	}
	if task.TargetSpaceID != nil {
		var e error
		if task.Kind == "repair" && task.SourceRevisionID != nil {
			e = s.allowedRepairTarget(ctx, s.client, p, *task.SourceRevisionID, *task.TargetSpaceID, 0)
		} else if task.Kind == "migration" {
			e = s.validateStorageTarget(ctx, s.client, p, *task.TargetSpaceID, 0, false)
		} else {
			e = s.allowedExistingTarget(ctx, s.client, p, *task.TargetSpaceID, 0)
		}
		if e != nil {
			valid = false
		}
	}
	if !valid {
		out := []string{}
		for _, action := range actions {
			if action == "cancel" {
				out = append(out, action)
			}
		}
		return out
	}
	return actions
}
