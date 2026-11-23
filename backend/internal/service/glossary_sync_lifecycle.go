package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctaskresult"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

var (
	ErrSyncTaskNotFound      = errors.New("sync task not found")
	ErrSyncTaskStateConflict = errors.New("sync task state conflict")
)

const syncCheckpointVersion = 2
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
	err := s.withSyncPlanTransaction(ctx, actorUserID, projectID, func(client *ent.Client) error {
		info = nil
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, min(executionInputState(ctx).limit, DefaultExecutionInputBytes))
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		if err := reserveSyncProjection(b, int64(len(input.OldTarget))+int64(len(input.NewTarget)), 1, 0); err != nil {
			return err
		}
		entry, err := loadSyncPlanEntry(ctx, client, projectID, entryID, b)
		if err != nil {
			return err
		}
		if err := validateSyncResources(ctx, client, projectID, input.ResourceIDs); err != nil {
			return err
		}
		task, err := client.SyncTask.Create().SetProjectID(projectID).SetEntryID(entryID).SetActorUserID(actorUserID).
			SetOldTarget(input.OldTarget).SetNewTarget(input.NewTarget).SetTotalSegments(0).
			SetStatus(SyncTaskStatusPending).SetCheckpointVersion(syncCheckpointVersion).Save(ctx)
		if err != nil {
			return err
		}
		total, err := createSyncPlan(ctx, client, task, entry, input.ResourceIDs)
		if err != nil {
			return err
		}
		if total == 0 {
			return ErrNoAffectedSegments
		}
		if _, err := client.SyncTask.Update().Where(synctask.IDEQ(task.ID)).SetTotalSegments(total).Save(ctx); err != nil {
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
	var task *ent.SyncTask
	err := s.withSyncSnapshotTransaction(ctx, func(client *ent.Client) error {
		var err error
		task, err = getProjectSyncTask(ctx, client, projectID, taskID)
		if err != nil {
			return err
		}
		return hydrateSyncTaskResult(ctx, client, task)
	})
	return task, err
}

func getProjectSyncTask(ctx context.Context, client *ent.Client, projectID, taskID int) (*ent.SyncTask, error) {
	task, err := client.SyncTask.Query().Where(synctask.IDEQ(taskID), synctask.ProjectIDEQ(projectID)).Select(syncTaskHeaderFields...).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrSyncTaskNotFound
	}
	return task, err
}

func (s *GlossarySyncService) CancelSyncTask(ctx context.Context, actorUserID, projectID, taskID int) (*ent.SyncTask, error) {
	if actorUserID <= 0 || projectID <= 0 || taskID <= 0 {
		return nil, ErrInvalidInput
	}
	guard, err := s.lifecycle.Lock(ctx, "glossary_sync", taskID)
	if err != nil {
		return nil, err
	}
	defer guard.Release()
	if _, err := s.projects.requireProjectAccess(ctx, actorUserID, projectID, true); err != nil {
		return nil, err
	}
	current, err := getProjectSyncTask(ctx, s.client, projectID, taskID)
	if err != nil {
		return nil, err
	}
	if current.Status == SyncTaskStatusCancelled {
		if s.cancelTask != nil {
			s.cancelTask(taskID)
		}
		return s.GetSyncTaskStatus(ctx, actorUserID, projectID, taskID)
	}
	if current.Status == SyncTaskStatusCompleted || current.Status == SyncTaskStatusFailed {
		return nil, ErrSyncTaskStateConflict
	}
	var task *ent.SyncTask
	err = s.projects.mutateProject(ctx, actorUserID, projectID, func(client *ent.Client, _ *ent.Project) error {
		// 这条 UPDATE 与每个批次的首条 UPDATE 竞争同一任务行。
		// SQLite 获取写锁；PostgreSQL 串行化该行更新。
		now := timeutil.NowUTC()
		_, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.ProjectIDEQ(projectID),
			synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
			SetStatus(SyncTaskStatusCancelled).SetCancelledAt(now).SetFinishedAt(now).SetRetentionAnchorAt(now).Save(ctx)
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
		stored, err := syncResultSnapshot(ctx, client, task)
		if err != nil {
			return err
		}
		if _, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID)).SetResult(stored).Save(ctx); err != nil {
			return err
		}
		// Persisting the summary also advances updated_at. Return the final
		// header so a repeated cancellation observes the same committed state.
		task, err = getProjectSyncTask(ctx, client, projectID, taskID)
		if err != nil {
			return err
		}
		task.Result = stored
		return nil
	})
	if err == nil && s.cancelTask != nil {
		s.cancelTask(taskID)
	}
	return task, err
}

// prepareSyncTask validates the final checkpoint model. Published JSON plans are
// converted once by database migration before runners start; runtime never guesses.
func prepareSyncTask(ctx context.Context, client *ent.Client, task *ent.SyncTask) (*ent.SyncTask, error) {
	if err := validateSyncPlanSummary(ctx, client, task); err != nil {
		if !errors.Is(err, errInvalidSyncCheckpoint) {
			return nil, err
		}
		now := timeutil.NowUTC()
		_, err = client.SyncTask.Update().Where(synctask.IDEQ(task.ID), synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
			SetStatus(SyncTaskStatusFailed).SetError(syncRecoveryFailure).SetFinishedAt(now).SetRetentionAnchorAt(now).Save(ctx)
		task.Status, task.Error, task.FinishedAt, task.RetentionAnchorAt = SyncTaskStatusFailed, syncRecoveryFailure, &now, &now
		return task, err
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
			err := func() error {
				guard, err := s.lifecycle.Lock(ctx, "glossary_sync", id)
				if err != nil {
					return err
				}
				defer guard.Release()
				if guard.Active() {
					return tasklife.ErrBusy
				}
				return withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
					// Recovery 在执行之前运行。保留已初始化 pending 行的
					// 时间戳，使重复的启动迁移不产生副作用。
					count, err := client.SyncTask.Update().Where(synctask.IDEQ(id), synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).AddProcessedSegments(0).ClearFinishedAt().ClearRetentionAnchorAt().SetUpdatedAt(snapshot.UpdatedAt).Save(ctx)
					if err != nil || count == 0 {
						return err
					}
					task, err := client.SyncTask.Query().Where(synctask.IDEQ(id)).Select(syncTaskHeaderFields...).Only(ctx)
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
			}()
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
	err := func() error {
		guard, err := s.lifecycle.Lock(ctx, "glossary_sync", taskID)
		if err != nil {
			return err
		}
		defer guard.Release()
		return withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
			claimed = false
			count, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusEQ(SyncTaskStatusPending)).AddProcessedSegments(0).Save(ctx)
			if err != nil || count == 0 {
				return err
			}
			task, err := client.SyncTask.Query().Where(synctask.IDEQ(taskID)).Select(syncTaskHeaderFields...).Only(ctx)
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
			update := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusEQ(SyncTaskStatusPending)).SetStatus(SyncTaskStatusRunning).ClearFinishedAt().ClearRetentionAnchorAt()
			if task.StartedAt == nil {
				update.SetStartedAt(timeutil.NowUTC())
			}
			if _, err := update.Save(ctx); err != nil {
				return err
			}
			claimed = true
			return nil
		})
	}()
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
	guard, err := s.lifecycle.Lock(ctx, "glossary_sync", taskID)
	if err != nil {
		return false, err
	}
	defer guard.Release()
	done := false
	err = s.withSyncSnapshotTransaction(ctx, func(client *ent.Client) error {
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
		task, err := client.SyncTask.Query().Where(synctask.IDEQ(taskID)).Select(syncTaskHeaderFields...).Only(ctx)
		if err != nil {
			return err
		}
		plan, err := syncPlanPage(ctx, client, task)
		if err != nil {
			return err
		}
		b, err := backend.NewBuildBoundsFromContext(ctx, DefaultExecutionInputBytes, min(executionInputState(ctx).limit, DefaultExecutionInputBytes))
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		batchIDs := make([]int, 0, len(plan))
		resourceSet := make(map[int]bool, len(plan))
		for _, item := range plan {
			batchIDs = append(batchIDs, item.SegmentID)
			if item.ResourceID > 0 {
				resourceSet[item.ResourceID] = true
			}
		}
		resourceIDs := make([]int, 0, len(resourceSet))
		for id := range resourceSet {
			resourceIDs = append(resourceIDs, id)
		}
		entry, projectRow, rows, resources, err := loadSyncBatchBodies(ctx, client, task, batchIDs, resourceIDs, b)
		if err != nil {
			return err
		}
		byID := make(map[int]*ent.Segment, len(rows))
		for _, row := range rows {
			byID[row.ID] = row
		}
		storedResults, err := client.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(task.ID), synctaskresult.ResourceIDIn(resourceIDs...)).
			Select(synctaskresult.FieldID, synctaskresult.FieldResourceID, synctaskresult.FieldUpdatedCount, synctaskresult.FieldSkippedCount).All(ctx)
		if err != nil {
			return err
		}
		if len(storedResults) != len(resourceIDs) {
			return errors.New("sync plan resource missing from fixed lock set")
		}
		result := &GlossarySyncResult{TotalUpdated: task.TotalUpdated, TotalSkipped: task.TotalSkipped, Resources: make(map[int]*GlossarySyncExecuteResourceResult, len(resourceIDs))}
		for _, row := range storedResults {
			result.Resources[row.ResourceID] = &GlossarySyncExecuteResourceResult{ResourceID: row.ResourceID, UpdatedCount: row.UpdatedCount, SkippedCount: row.SkippedCount}
		}
		byResource := make(map[int]*ent.Resource, len(resources))
		for _, row := range resources {
			if err := GuardSourceGeneration(ctx, client, row.ID, row.SourceGeneration); err != nil {
				return err
			}
			byResource[row.ID] = row
		}
		advanced := make(map[int]bool, len(resources))
		for _, item := range plan {
			row := byID[item.SegmentID]
			if row == nil || row.ResourceID == nil || *row.ResourceID != item.ResourceID || byResource[item.ResourceID] == nil {
				result.TotalSkipped++
				continue
			}
			res := byResource[*row.ResourceID]
			stats := result.Resources[res.ID]
			if stats == nil {
				stats = &GlossarySyncExecuteResourceResult{ResourceID: res.ID, ResourcePath: res.Path}
				result.Resources[res.ID] = stats
			}
			if stats.UpdatedCount+stats.SkippedCount == 0 {
				stats.ResourcePath = res.Path
			}
			skip := func() { result.TotalSkipped++; stats.SkippedCount++ }
			if row.TargetText == nil || !syncSegmentMatches(row, entry) {
				skip()
				continue
			}
			newText, replaced, err := glossary.SafeReplaceBounded(b, *row.TargetText, task.OldTarget, task.NewTarget, projectRow.TargetLang, b.OutputLimit())
			if err != nil {
				return err
			}
			if !replaced && !entry.CaseSensitive {
				newText, replaced, err = syncCaseInsensitiveReplace(b, *row.TargetText, task.OldTarget, task.NewTarget)
				if err != nil {
					return err
				}
			}
			if !replaced {
				skip()
				continue
			}
			if markup.RequiresWellFormedTargets(res.Format) {
				if err := markup.ValidateFragmentBounded(b, newText); err != nil {
					if errors.Is(err, backend.ErrBuildCapacity) {
						return err
					}
					skip()
					continue
				}
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
				segment.ContentVersionEQ(row.ContentVersion), segment.SourceTextEQ(row.SourceText), segment.TargetTextEQ(*row.TargetText), segment.StatusEQ(row.Status)).
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
		for _, previous := range storedResults {
			stats := result.Resources[previous.ResourceID]
			u := client.SyncTaskResult.Update().Where(synctaskresult.IDEQ(previous.ID)).SetUpdatedCount(stats.UpdatedCount).SetSkippedCount(stats.SkippedCount)
			if previous.UpdatedCount+previous.SkippedCount == 0 && stats.UpdatedCount+stats.SkippedCount > 0 {
				u.SetResourcePath(stats.ResourcePath)
			}
			if stats.UpdatedCount != previous.UpdatedCount || stats.SkippedCount != previous.SkippedCount {
				if _, err := u.Save(ctx); err != nil {
					return err
				}
			}
		}
		end := task.NextSegmentIndex + len(plan)
		update := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusEQ(SyncTaskStatusRunning)).
			SetProcessedSegments(end).SetNextSegmentIndex(end).SetTotalUpdated(result.TotalUpdated).SetTotalSkipped(result.TotalSkipped)
		done = end == task.TotalSegments
		if done {
			task.TotalUpdated, task.TotalSkipped = result.TotalUpdated, result.TotalSkipped
			stored, err := syncResultSnapshot(ctx, client, task)
			if err != nil {
				return err
			}
			now := timeutil.NowUTC()
			update.SetResult(stored).SetStatus(SyncTaskStatusCompleted).SetFinishedAt(now).SetRetentionAnchorAt(now)
		}
		if _, err := update.Save(ctx); err != nil {
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
	return syncContainsFold(row.SourceText, entry.Source)
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
	guard, err := s.lifecycle.Lock(ctx, "glossary_sync", taskID)
	if err != nil {
		return err
	}
	defer guard.Release()
	s.logger.Error("sync task failed", "task_id", taskID, "error", cause)
	err = withOrganizationTransaction(ctx, s.client, func(client *ent.Client) error {
		now := timeutil.NowUTC()
		count, err := client.SyncTask.Update().Where(synctask.IDEQ(taskID), synctask.StatusIn(SyncTaskStatusPending, SyncTaskStatusRunning)).
			SetStatus(SyncTaskStatusFailed).SetError(cause.Error()).SetFinishedAt(now).SetRetentionAnchorAt(now).Save(ctx)
		if err != nil || count == 0 {
			return err
		}
		task, err := client.SyncTask.Query().Where(synctask.IDEQ(taskID)).Select(syncTaskHeaderFields...).Only(ctx)
		if err != nil {
			return err
		}
		stored, summaryErr := syncResultSnapshot(ctx, client, task)
		if summaryErr != nil {
			// A corrupt/oversized summary cannot erase a valid failure transition.
			s.logger.Warn("sync failure summary unavailable", "task_id", taskID, "error", summaryErr)
			return nil
		}
		_, err = client.SyncTask.Update().Where(synctask.IDEQ(taskID)).SetResult(stored).Save(ctx)
		return err
	})
	if err != nil {
		s.logger.Error("failed to mark sync task as failed", "task_id", taskID, "error", err)
	}
	return cause
}
