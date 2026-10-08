package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
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
	valid := s.taskWriteAdmission(ctx, s.client, task) == nil
	out := []string{}
	for _, action := range actions {
		if action == "cancel" || valid && (action != "retry" || s.taskRetryReady(ctx, s.client, task) == nil) {
			out = append(out, action)
		}
	}
	return out
}

// taskWriteAdmission is the shared current-state check for task actions,
// retries and execution. It never resolves a driver or probes the provider.
func (s *StorageService) taskWriteAdmission(ctx context.Context, client *ent.Client, task *ent.StorageTask) error {
	if err := storageTerminalError(task); err != nil {
		return err
	}
	p, err := client.Project.Get(ctx, task.ProjectID)
	if err != nil {
		return err
	}
	if p.StorageGeneration != task.ExpectedStorageGeneration {
		return ErrStorageConflict
	}
	if task.Kind != "migration" && task.Kind != "repair" && p.StorageState != "active" {
		return ErrStorageMaintenance
	}
	if task.Kind == "source_update" && task.ResourceID != nil {
		r, err := client.Resource.Get(ctx, *task.ResourceID)
		if err != nil {
			return err
		}
		if r.SourceGeneration != task.ExpectedSourceGeneration || r.TranslationGeneration != task.ExpectedTranslationGeneration {
			return ErrSourceRevisionConflict
		}
	}
	if task.TargetSpaceID == nil {
		return ErrStoragePolicy
	}
	if task.Kind == "repair" {
		if task.SourceRevisionID == nil {
			return ErrInvalidInput
		}
		return s.allowedRepairTarget(ctx, client, p, *task.SourceRevisionID, *task.TargetSpaceID, 0)
	}
	if task.Kind == "migration" {
		return s.validateStorageTarget(ctx, client, p, *task.TargetSpaceID, 0, false)
	}
	return s.allowedExistingTarget(ctx, client, p, *task.TargetSpaceID, 0)
}

func (s *StorageService) taskRetryReady(ctx context.Context, client *ent.Client, task *ent.StorageTask) error {
	writes, err := client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseNotIn("committed", "cleaned", "prepared")).Exist(ctx)
	if err != nil {
		return err
	}
	if writes {
		return ErrStorageConflict
	}
	return nil
}
