package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

var (
	ErrSyncTaskNotFound      = errors.New("sync task not found")
	ErrSyncTaskStateConflict = errors.New("sync task state conflict")
)

const syncCheckpointVersion = 1
const syncBatchSize = 100
const syncRecoveryFailure = "术语同步恢复失败：缺少可信批次检查点，已提交内容保留，请重新分析后创建新任务"

func validateSyncResources(ctx context.Context, client *ent.Client, projectID int, ids []int) error {
	unique := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return ErrInvalidInput
		}
		unique[id] = struct{}{}
	}
	if len(unique) == 0 {
		return nil
	}
	normalized := make([]int, 0, len(unique))
	for id := range unique {
		normalized = append(normalized, id)
	}
	count, err := client.Resource.Query().Where(resource.ProjectIDEQ(projectID), resource.IDIn(normalized...)).Count(ctx)
	if err != nil {
		return err
	}
	if count != len(unique) {
		return fmt.Errorf("%w: resource does not belong to project", ErrInvalidInput)
	}
	return nil
}

func syncResourceIDs(rows []*ent.Segment) []int {
	seen := make(map[int]struct{})
	for _, row := range rows {
		if row.ResourceID != nil {
			seen[*row.ResourceID] = struct{}{}
		}
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// SubmitSyncTask 在通知派发器之前提交不可变的工作清单。
func (s *GlossarySyncService) SubmitSyncTask(ctx context.Context, actorUserID, projectID, entryID int, input GlossarySyncExecuteInput) (*SyncTaskInfo, error) {
	if actorUserID <= 0 || projectID <= 0 || entryID <= 0 || input.OldTarget == "" || input.NewTarget == "" {
		return nil, ErrInvalidInput
	}
	var info *SyncTaskInfo
	err := s.projects.mutateProject(ctx, actorUserID, projectID, func(client *ent.Client, _ *ent.Project) error {
		entry, err := client.GlossaryEntry.Query().Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID)).Only(ctx)
		if ent.IsNotFound(err) {
			return ErrGlossaryEntryNotFound
		}
		if err != nil {
			return err
		}
		if err := validateSyncResources(ctx, client, projectID, input.ResourceIDs); err != nil {
			return err
		}
		txService := *s
		txService.client = client
		affected, err := txService.findAffectedSegments(ctx, projectID, entry.Source, entry.CaseSensitive, input.OldTarget, input.ResourceIDs)
		if err != nil {
			return err
		}
		if len(affected) == 0 {
			return ErrNoAffectedSegments
		}
		ids := make([]int, len(affected))
		for i, row := range affected {
			ids[i] = row.ID
		}
		idsJSON, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		resourceJSON, err := json.Marshal(syncResourceIDs(affected))
		if err != nil {
			return err
		}
		resultJSON, err := json.Marshal(glossarySyncResultJSON{Resources: []*GlossarySyncExecuteResourceResult{}})
		if err != nil {
			return err
		}
		task, err := client.SyncTask.Create().SetProjectID(projectID).SetEntryID(entryID).SetActorUserID(actorUserID).
			SetOldTarget(input.OldTarget).SetNewTarget(input.NewTarget).SetTotalSegments(len(ids)).
			SetProcessedSegments(0).SetStatus(SyncTaskStatusPending).SetSegmentIds(string(idsJSON)).
			SetResourceIds(string(resourceJSON)).SetCheckpointVersion(syncCheckpointVersion).SetNextSegmentIndex(0).
			SetResult(string(resultJSON)).Save(ctx)
		if err != nil {
			return err
		}
		info = &SyncTaskInfo{TaskID: task.ID, Status: task.Status, StatusURL: fmt.Sprintf("/projects/%d/sync-tasks/%d", projectID, task.ID)}
		return nil
	})
	return info, err
}

func (s *GlossarySyncService) GetSyncTaskStatus(ctx context.Context, actorUserID, projectID, taskID int) (*ent.SyncTask, error) {
	if actorUserID <= 0 || projectID <= 0 || taskID <= 0 {
		return nil, ErrInvalidInput
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, false); err != nil {
		return nil, err
	}
	return getProjectSyncTask(ctx, s.client, projectID, taskID)
}

func getProjectSyncTask(ctx context.Context, client *ent.Client, projectID, taskID int) (*ent.SyncTask, error) {
	task, err := client.SyncTask.Query().Where(synctask.IDEQ(taskID), synctask.ProjectIDEQ(projectID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrSyncTaskNotFound
	}
	return task, err
}

func (s *GlossarySyncService) CancelSyncTask(ctx context.Context, actorUserID, projectID, taskID int) (*ent.SyncTask, error) {
	if actorUserID <= 0 || projectID <= 0 || taskID <= 0 {
		return nil, ErrInvalidInput
	}
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	current, err := getProjectSyncTask(ctx, s.client, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if current.Status == SyncTaskStatusCancelled {
		return current, nil
	}
	if current.Status == SyncTaskStatusCompleted || current.Status == SyncTaskStatusFailed {
		return nil, ErrSyncTaskStateConflict
	}
	var task *ent.SyncTask
	err = s.projects.mutateProject(ctx, actorUserID, projectID, func(client *ent.Client, _ *ent.Project) error {
		// 这条 UPDATE 与每个批次的首条 UPDATE 竞争同一任务行。
		// SQLite 获取写锁；PostgreSQL 串行化该行更新。
		_, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.ProjectIDEQ(projectID),
			synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
			SetStatus(SyncTaskStatusCancelled).SetCancelledAt(timeutil.NowUTC()).Save(ctx)
		if err != nil {
			return err
		}
		task, err = getProjectSyncTask(ctx, client, projectID, taskID)
		if err != nil {
			return err
		}
		if task.Status != SyncTaskStatusCancelled {
			return ErrSyncTaskStateConflict
		}
		return nil
	})
	return task, err
}

// decodeSyncCheckpoint 独立于仍存在的行数校验进度：已删除的工作清单条目
// 仍会作为必须跳过的位置保留。
func decodeSyncCheckpoint(task *ent.SyncTask) ([]int, []int, *GlossarySyncResult, error) {
	var ids, resources []int
	if err := json.Unmarshal([]byte(task.SegmentIds), &ids); err != nil {
		return nil, nil, nil, err
	}
	if len(ids) != task.TotalSegments || len(ids) == 0 {
		return nil, nil, nil, errors.New("invalid work list length")
	}
	for i, id := range ids {
		if id <= 0 || i > 0 && ids[i-1] >= id {
			return nil, nil, nil, errors.New("invalid ordered work list")
		}
	}
	if task.CheckpointVersion != syncCheckpointVersion || task.NextSegmentIndex < 0 || task.NextSegmentIndex > len(ids) || task.ProcessedSegments != task.NextSegmentIndex {
		return nil, nil, nil, errors.New("invalid checkpoint version or position")
	}
	if err := json.Unmarshal([]byte(task.ResourceIds), &resources); err != nil {
		return nil, nil, nil, err
	}
	for i, id := range resources {
		if id <= 0 || i > 0 && resources[i-1] >= id {
			return nil, nil, nil, errors.New("invalid resource lock set")
		}
	}
	var stored *glossarySyncResultJSON
	if err := json.Unmarshal([]byte(task.Result), &stored); err != nil {
		return nil, nil, nil, err
	}
	if stored == nil {
		return nil, nil, nil, errors.New("missing accumulated result")
	}
	if stored.TotalUpdated < 0 || stored.TotalSkipped < 0 || stored.TotalUpdated > task.NextSegmentIndex || stored.TotalSkipped != task.NextSegmentIndex-stored.TotalUpdated {
		return nil, nil, nil, errors.New("invalid accumulated result")
	}
	result := &GlossarySyncResult{TotalUpdated: stored.TotalUpdated, TotalSkipped: stored.TotalSkipped, Resources: make(map[int]*GlossarySyncExecuteResourceResult)}
	resourceUpdates, resourceSkipped := 0, 0
	for _, row := range stored.Resources {
		if row == nil || row.ResourceID <= 0 || row.UpdatedCount < 0 || row.SkippedCount < 0 || result.Resources[row.ResourceID] != nil {
			return nil, nil, nil, errors.New("invalid resource result")
		}
		at := sort.SearchInts(resources, row.ResourceID)
		if at == len(resources) || resources[at] != row.ResourceID || row.UpdatedCount > stored.TotalUpdated-resourceUpdates || row.SkippedCount > stored.TotalSkipped-resourceSkipped {
			return nil, nil, nil, errors.New("inconsistent resource result")
		}
		resourceUpdates += row.UpdatedCount
		resourceSkipped += row.SkippedCount
		result.Resources[row.ResourceID] = row
	}
	if resourceUpdates != stored.TotalUpdated {
		return nil, nil, nil, errors.New("incomplete resource update result")
	}
	return ids, resources, result, nil
}

func encodeSyncResult(result *GlossarySyncResult) (string, error) {
	resources := make([]*GlossarySyncExecuteResourceResult, 0, len(result.Resources))
	for _, stats := range result.Resources {
		resources = append(resources, stats)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ResourceID < resources[j].ResourceID })
	data, err := json.Marshal(glossarySyncResultJSON{TotalUpdated: result.TotalUpdated, TotalSkipped: result.TotalSkipped, Resources: resources})
	return string(data), err
}

// prepareSyncTask 在持有任务行写锁时被调用。它绝不
// 猜测遗留 running 任务在进程停止前已提交了多少。
func prepareSyncTask(ctx context.Context, client *ent.Client, task *ent.SyncTask) (*ent.SyncTask, error) {
	if task.CheckpointVersion == 0 && task.Status == SyncTaskStatusPending && task.ProcessedSegments == 0 && task.NextSegmentIndex == 0 && task.Result == "" && task.StartedAt == nil && task.CancelledAt == nil {
		var ids []int
		if err := json.Unmarshal([]byte(task.SegmentIds), &ids); err == nil && len(ids) > 0 && len(ids) == task.TotalSegments {
			valid := true
			for i, id := range ids {
				if id <= 0 || i > 0 && ids[i-1] >= id {
					valid = false
				}
			}
			if valid {
				rows, err := client.Segment.Query().Where(segment.IDIn(ids...), segment.HasResourceWith(resource.ProjectIDEQ(task.ProjectID))).All(ctx)
				if err != nil {
					return nil, err
				}
				resources, err := json.Marshal(syncResourceIDs(rows))
				if err != nil {
					return nil, err
				}
				result, err := encodeSyncResult(&GlossarySyncResult{Resources: map[int]*GlossarySyncExecuteResourceResult{}})
				if err != nil {
					return nil, err
				}
				task, err = client.SyncTask.UpdateOneID(task.ID).SetCheckpointVersion(syncCheckpointVersion).SetNextSegmentIndex(0).
					SetResourceIds(string(resources)).SetResult(result).Save(ctx)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	if _, _, _, err := decodeSyncCheckpoint(task); err != nil {
		return client.SyncTask.UpdateOneID(task.ID).SetStatus(SyncTaskStatusFailed).SetError(syncRecoveryFailure).Save(ctx)
	}
	return task, nil
}

// PrepareRecovery 在本 runner 消费任何队列元素之前运行。终态
// 历史不受影响。该操作幂等，并使用有界的数据库分页。
func (s *GlossarySyncService) PrepareRecovery(ctx context.Context) error {
	last := 0
	for {
		rows, err := s.client.SyncTask.Query().Where(synctask.IDGT(last), synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
			Order(ent.Asc(synctask.FieldID)).Limit(200).Select(synctask.FieldID, synctask.FieldUpdatedAt).All(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, snapshot := range rows {
			id := snapshot.ID
			err := withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
				// Recovery 在执行之前运行。保留已初始化 pending 行的
				// 时间戳，使重复的启动迁移不产生副作用。
				count, err := client.SyncTask.Update().Where(synctask.IDEQ(id), synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).AddProcessedSegments(0).SetUpdatedAt(snapshot.UpdatedAt).Save(ctx)
				if err != nil || count == 0 {
					return err
				}
				task, err := client.SyncTask.Get(ctx, id)
				if err != nil {
					return err
				}
				task, err = prepareSyncTask(ctx, client, task)
				if err != nil {
					return err
				}
				if task.Status == SyncTaskStatusRunning {
					return client.SyncTask.UpdateOneID(id).SetStatus(SyncTaskStatusPending).Exec(ctx)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("prepare sync task %d: %w", id, err)
			}
		}
		last = rows[len(rows)-1].ID
	}
}

func (s *GlossarySyncService) PendingTaskIDs(ctx context.Context, afterID, limit int) ([]int, error) {
	if afterID < 0 || limit <= 0 {
		return nil, ErrInvalidInput
	}
	if limit > 200 {
		limit = 200
	}
	return s.client.SyncTask.Query().Where(synctask.IDGT(afterID), synctask.StatusEQ(SyncTaskStatusPending), synctask.CheckpointVersionEQ(syncCheckpointVersion)).
		Order(ent.Asc(synctask.FieldID)).Limit(limit).Select(synctask.FieldID).Ints(ctx)
}

func (s *GlossarySyncService) RecoverPendingJobs(ctx context.Context) ([]int, error) {
	if err := s.PrepareRecovery(ctx); err != nil {
		return nil, err
	}
	var all []int
	last := 0
	for {
		ids, err := s.PendingTaskIDs(ctx, last, 200)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return all, nil
		}
		all = append(all, ids...)
		last = ids[len(ids)-1]
	}
}

func (s *GlossarySyncService) ReconcileJob(context.Context, int) error { return nil }

// CleanupExpiredTasks 为旧调用方保留。创建时间年龄不是执行截止期限。
func (s *GlossarySyncService) CleanupExpiredTasks(context.Context) error { return nil }

// ExecuteSyncTask 负责 pending -> running 的抢占。重复投递与
// 终态队列元素均无害；关闭时保留可恢复的检查点。
func (s *GlossarySyncService) ExecuteSyncTask(ctx context.Context, taskID int) error {
	claimed := false
	err := withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		claimed = false
		count, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusEQ(SyncTaskStatusPending)).AddProcessedSegments(0).Save(ctx)
		if err != nil || count == 0 {
			return err
		}
		task, err := client.SyncTask.Get(ctx, taskID)
		if err != nil {
			return err
		}
		task, err = prepareSyncTask(ctx, client, task)
		if err != nil {
			return err
		}
		if task.Status != SyncTaskStatusPending {
			return nil
		}
		update := client.SyncTask.UpdateOneID(taskID).Where(synctask.StatusEQ(SyncTaskStatusPending)).SetStatus(SyncTaskStatusRunning)
		if task.StartedAt == nil {
			update.SetStartedAt(timeutil.NowUTC())
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := s.executeSyncBatch(ctx, taskID)
		if err != nil {
			return s.FailSyncTask(ctx, taskID, err)
		}
		if done {
			return nil
		}
	}
}

func (s *GlossarySyncService) executeSyncBatch(ctx context.Context, taskID int) (bool, error) {
	done := false
	err := withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		done = false
		// 首条语句在任何快照读取之前获取写锁。因此已提交的
		// 取消会阻止其后所有段落写入。
		count, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusEQ(SyncTaskStatusRunning)).AddProcessedSegments(0).Save(ctx)
		if err != nil {
			return err
		}
		if count == 0 {
			done = true
			return nil
		}
		task, err := client.SyncTask.Get(ctx, taskID)
		if err != nil {
			return err
		}
		ids, resourceIDs, result, err := decodeSyncCheckpoint(task)
		if err != nil {
			return err
		}
		entry, err := client.GlossaryEntry.Query().Where(glossaryentry.IDEQ(task.EntryID), glossaryentry.ProjectIDEQ(task.ProjectID)).Only(ctx)
		if err != nil {
			return err
		}
		projectRow, err := client.Project.Get(ctx, task.ProjectID)
		if err != nil {
			return err
		}
		end := min(task.NextSegmentIndex+syncBatchSize, len(ids))
		batchIDs := ids[task.NextSegmentIndex:end]
		rows, err := client.Segment.Query().Where(segment.IDIn(batchIDs...), segment.HasResourceWith(resource.ProjectIDEQ(task.ProjectID))).All(ctx)
		if err != nil {
			return err
		}
		byID := make(map[int]*ent.Segment, len(rows))
		for _, row := range rows {
			byID[row.ID] = row
		}
		batchResourceIDs := syncResourceIDs(rows)
		lockedResourceIDs := make([]int, 0, len(batchResourceIDs))
		for _, id := range batchResourceIDs {
			at := sort.SearchInts(resourceIDs, id)
			if at < len(resourceIDs) && resourceIDs[at] == id {
				lockedResourceIDs = append(lockedResourceIDs, id)
			}
		}
		resources, err := client.Resource.Query().Where(resource.IDIn(lockedResourceIDs...), resource.ProjectIDEQ(task.ProjectID)).All(ctx)
		if err != nil {
			return err
		}
		byResource := make(map[int]*ent.Resource, len(resources))
		sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
		for _, row := range resources {
			if err := GuardSourceGeneration(ctx, client, row.ID, row.SourceGeneration); err != nil {
				return err
			}
			byResource[row.ID] = row
		}
		advanced := make(map[int]bool, len(resources))
		for _, id := range batchIDs {
			row := byID[id]
			if row == nil || row.ResourceID == nil || byResource[*row.ResourceID] == nil {
				result.TotalSkipped++
				continue
			}
			res := byResource[*row.ResourceID]
			stats := result.Resources[res.ID]
			if stats == nil {
				stats = &GlossarySyncExecuteResourceResult{ResourceID: res.ID, ResourcePath: res.Path}
				result.Resources[res.ID] = stats
			}
			skip := func() { result.TotalSkipped++; stats.SkippedCount++ }
			if row.TargetText == nil || !syncSegmentMatches(row, entry) {
				skip()
				continue
			}
			newText, replaced, _ := glossary.SafeReplace(*row.TargetText, task.OldTarget, task.NewTarget, projectRow.TargetLang)
			if !replaced && !entry.CaseSensitive {
				newText, replaced = glossary.CaseInsensitiveReplace(*row.TargetText, task.OldTarget, task.NewTarget)
			}
			if !replaced {
				skip()
				continue
			}
			if markup.RequiresWellFormedTargets(res.Format) && markup.ValidateFragment(newText) != nil {
				skip()
				continue
			}
			if !advanced[res.ID] {
				if err := AdvanceTranslationGeneration(ctx, client, res.ID, res.SourceGeneration); err != nil {
					return err
				}
				advanced[res.ID] = true
			}
			// 防止读取与 UPDATE 之间的并发手动编辑，
			// 包括资源迁移或将译文重置为 pending。
			updated, err := client.Segment.Update().Where(segment.IDEQ(row.ID), segment.ResourceIDEQ(res.ID),
				segment.SourceTextEQ(row.SourceText), segment.TargetTextEQ(*row.TargetText), segment.StatusEQ(row.Status)).
				SetTargetText(newText).SetStatus(SegmentStatusEdited).ClearReviewedBy().ClearReviewComment().Save(ctx)
			if err != nil {
				return err
			}
			if updated == 0 {
				skip()
				continue
			}
			result.TotalUpdated++
			stats.UpdatedCount++
		}
		stored, err := encodeSyncResult(result)
		if err != nil {
			return err
		}
		update := client.SyncTask.UpdateOneID(taskID).Where(synctask.StatusEQ(SyncTaskStatusRunning)).
			SetProcessedSegments(end).SetNextSegmentIndex(end).SetResult(stored)
		done = end == len(ids)
		if done {
			update.SetStatus(SyncTaskStatusCompleted)
		}
		if err := update.Exec(ctx); err != nil {
			return err
		}
		if done && s.auditSvc != nil {
			return recordAuditEvent(ctx, client, AuditEvent{ActorUserID: task.ActorUserID, ProjectID: &task.ProjectID,
				Action: "glossary.sync_execute", ResourceType: "glossary_entry", ResourceID: task.EntryID,
				Message: fmt.Sprintf("术语同步更新完成：更新 %d 个段落，跳过 %d 个段落", result.TotalUpdated, result.TotalSkipped)})
		}
		return nil
	})
	return done, err
}

func syncSegmentMatches(row *ent.Segment, entry *ent.GlossaryEntry) bool {
	validStatus := false
	for _, status := range syncableSegmentStatuses {
		if row.Status == status {
			validStatus = true
			break
		}
	}
	if !validStatus {
		return false
	}
	if entry.CaseSensitive {
		return strings.Contains(row.SourceText, entry.Source)
	}
	return strings.Contains(strings.ToLower(row.SourceText), strings.ToLower(entry.Source))
}

// FailSyncTask 不会把关闭或已持久化的终态变成失败。
// runner 的校验失败与执行失败共用这同一道防护。
func (s *GlossarySyncService) FailSyncTask(ctx context.Context, taskID int, cause error) error {
	if cause == nil {
		return nil
	}
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	s.logger.Error("sync task failed", "task_id", taskID, "error", cause)
	err := withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		_, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
			SetStatus(SyncTaskStatusFailed).SetError(cause.Error()).Save(ctx)
		return err
	})
	if err != nil {
		s.logger.Error("failed to mark sync task as failed", "task_id", taskID, "error", err)
	}
	return cause
}
