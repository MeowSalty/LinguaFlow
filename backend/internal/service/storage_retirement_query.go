package service

import (
	"context"
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"sort"
)

func retirementPageBounds(cursor, limit int) (int, int, error) {
	if cursor < 0 || limit < 0 || limit > 100 {
		return 0, 0, ErrInvalidInput
	}
	if limit == 0 {
		limit = 50
	}
	return cursor, limit, nil
}
func (s *StorageRetirementService) record(ctx context.Context, actor int, r *ent.StorageRetirement) (map[string]any, error) {
	target, err := retirementScope(r)
	if err != nil {
		return nil, err
	}
	actions := []string{}
	if !s.cfg.Maintenance && r.Status != storageretirement.StatusCompleted && r.Status != storageretirement.StatusCancelled {
		if !r.CancelRequested && r.Phase != "finalizing" && r.Phase != "waiting_configuration" {
			actions = append(actions, "update_plan", "cancel")
		}
		if r.Status == storageretirement.StatusNeedsAction || r.Status == storageretirement.StatusFailed || r.Status == storageretirement.StatusWaitingRetry {
			actions = append(actions, "resume")
		}
		if r.Phase == "finalizing" && target.ConfigurationSource == "stored" && r.Status == storageretirement.StatusNeedsAction {
			actions = append(actions, "recover_authorization")
		}
	}
	reasons := []string{}
	if r.ErrorCode != "" {
		reasons = append(reasons, r.ErrorCode)
	}
	cleanup, configuration := "pending", "not_required"
	configurationActions := []string{}
	if r.Phase == "finalizing" {
		cleanup = "running"
	}
	if r.Status == storageretirement.StatusNeedsAction {
		cleanup = "blocked"
	}
	if target.ConfigurationSource == "deployment" {
		configuration = "pending"
	}
	if r.Phase == "waiting_configuration" {
		cleanup = "completed"
		configurationActions = []string{"remove_backend_configuration", "reload_configuration"}
	}
	if r.Status == storageretirement.StatusCompleted {
		cleanup = "completed"
		if target.ConfigurationSource == "deployment" {
			configuration = "completed"
		}
	}
	return map[string]any{"retirement_id": r.ID, "contract_version": 1, "target": target, "status": r.Status, "phase": r.Phase, "generation": r.Generation, "cancel_requested": r.CancelRequested, "next_check_at": r.NextCheckAt, "reason_codes": reasons, "allowed_actions": actions, "created_at": r.CreatedAt, "updated_at": r.UpdatedAt, "result": map[string]any{"cleanup_status": cleanup, "configuration_source": target.ConfigurationSource, "configuration_status": configuration, "configuration_actions": configurationActions}}, nil
}
func (s *StorageRetirementService) Record(ctx context.Context, actor, id int) (map[string]any, error) {
	r, err := s.authorized(ctx, s.client, actor, id)
	if err != nil {
		return nil, err
	}
	return s.record(ctx, actor, r)
}
func (s *StorageRetirementService) List(ctx context.Context, actor int, scope string, owner, cursor, limit int) (map[string]any, error) {
	if err := s.connections.requireOwner(ctx, s.client, actor, scope, owner); err != nil {
		return nil, err
	}
	cursor, limit, err := retirementPageBounds(cursor, limit)
	if err != nil {
		return nil, err
	}
	q := s.client.StorageRetirement.Query().Where(storageretirement.OwnerKindEQ(storageretirement.OwnerKind(scope)), storageretirement.OwnerIDEQ(owner))
	if cursor > 0 {
		q.Where(storageretirement.IDLT(cursor))
	}
	rows, err := q.Order(ent.Desc(storageretirement.FieldID)).Limit(limit + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	items := []map[string]any{}
	var next *int
	for _, r := range rows {
		item, err := s.record(ctx, actor, r)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if more {
		id := rows[len(rows)-1].ID
		next = &id
	}
	return map[string]any{"items": items, "next_cursor": next, "has_more": more}, nil
}
func (s *StorageRetirementService) Detail(ctx context.Context, actor, id, cursor, limit int) (map[string]any, error) {
	cursor, limit, err := retirementPageBounds(cursor, limit)
	if err != nil {
		return nil, err
	}
	r, err := s.authorized(ctx, s.client, actor, id)
	if err != nil {
		return nil, err
	}
	record, err := s.record(ctx, actor, r)
	if err != nil {
		return nil, err
	}
	target, err := retirementScope(r)
	if err != nil {
		return nil, err
	}
	inv, err := s.inventory(ctx, s.client, target)
	if err != nil {
		return nil, err
	}
	visible := map[int]bool{}
	projects := []map[string]any{}
	entries, err := s.client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(id), storageretiremententry.KindEQ("project")).Order(ent.Asc(storageretiremententry.FieldProjectID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if _, err = s.storage.projects.requireProjectAccess(ctx, actor, e.ProjectID, false); err != nil {
			continue
		}
		visible[e.ProjectID] = true
		_, writeErr := s.storage.projects.requireProjectAccess(ctx, actor, e.ProjectID, true)
		assessment := func(allowed bool) map[string]any {
			reasons := []string{}
			if !allowed {
				reasons = append(reasons, "forbidden")
			}
			return map[string]any{"allowed": allowed, "reason_codes": reasons}
		}
		item := map[string]any{"project_id": e.ProjectID, "permissions": map[string]any{"view": assessment(true), "migrate": assessment(writeErr == nil), "update_plan": assessment(writeErr == nil && e.AcceptedAt == nil)}, "target_check": map[string]any{"allowed": e.AcceptedAt != nil, "reason_codes": []string{}}, "accepted": e.AcceptedAt != nil}
		if e.TargetSpaceID != nil {
			item["plan"] = RetirementProjectPlan{ProjectID: e.ProjectID, TargetSpaceID: *e.TargetSpaceID, ExpectedStorageGeneration: e.ExpectedStorageGeneration, ExpectedTargetManagementGeneration: e.ExpectedTargetManagementGeneration, ExpectedTargetExecutionGeneration: e.ExpectedTargetExecutionGeneration}
		} else {
			item["target_check"] = map[string]any{"allowed": false, "reason_codes": []string{"selection_required"}}
		}
		if e.MigrationTaskID != nil {
			item["migration_task_id"] = *e.MigrationTaskID
		}
		projects = append(projects, item)
	}
	data, err := json.Marshal(inv.Dependencies)
	if err != nil {
		return nil, err
	}
	deps := []map[string]any{}
	if err = json.Unmarshal(data, &deps); err != nil {
		return nil, err
	}
	filtered := []map[string]any{}
	hidden := map[string]int{}
	for _, dep := range deps {
		pid, _ := dep["project_id"].(float64)
		if pid > 0 && !visible[int(pid)] {
			kind, _ := dep["kind"].(string)
			count, _ := dep["count"].(float64)
			hidden[kind] += max(int(count), 1)
			continue
		}
		filtered = append(filtered, dep)
	}
	kinds := []string{}
	for kind := range hidden {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		filtered = append(filtered, map[string]any{"kind": kind, "status": "needs_action", "redacted": true, "count": hidden[kind], "reason_codes": []string{"forbidden"}})
	}
	if cursor > len(filtered) {
		return nil, ErrInvalidInput
	}
	end := min(cursor+limit, len(filtered))
	more := end < len(filtered)
	var next *int
	if more {
		next = &end
	}
	return map[string]any{"retirement": record, "projects": projects, "dependencies": map[string]any{"items": filtered[cursor:end], "next_cursor": next, "has_more": more}}, nil
}
