package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctaskitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctaskresult"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

var syncTaskHeaderFields = []string{
	synctask.FieldID, synctask.FieldProjectID, synctask.FieldEntryID, synctask.FieldActorUserID,
	synctask.FieldStatus, synctask.FieldCheckpointVersion, synctask.FieldTotalSegments,
	synctask.FieldProcessedSegments, synctask.FieldNextSegmentIndex, synctask.FieldTotalUpdated,
	synctask.FieldTotalSkipped, synctask.FieldStartedAt, synctask.FieldCancelledAt,
	synctask.FieldCreatedAt, synctask.FieldUpdatedAt, synctask.FieldFinishedAt, synctask.FieldRetentionAnchorAt,
}

var errInvalidSyncCheckpoint = errors.New("invalid sync checkpoint")

func syncCheckpointError(detail string) error {
	return fmt.Errorf("%w: %s", errInvalidSyncCheckpoint, detail)
}

// Submission uses one snapshot for every identity page. PostgreSQL's ordinary
// READ COMMITTED transaction would otherwise select a different plan per page.
// The first writes fence project permissions and SQLite's read snapshot.
func (s *GlossarySyncService) withSyncPlanTransaction(ctx context.Context, actor, projectID int, apply func(*ent.Client) error) error {
	ctx, cancel := context.WithTimeout(ctx, organizationTransactionRetryBudget)
	defer cancel()
	current, err := s.projects.requireProjectAccess(ctx, actor, projectID, true)
	if err != nil {
		return err
	}
	return s.withSyncSnapshotTransaction(ctx, func(client *ent.Client) error {
		if orgID := EffectiveProjectOrgID(current); orgID != nil {
			if _, err := client.Organization.Update().Where(organization.IDEQ(*orgID)).SetUpdatedAt(timeutil.NowUTC()).Save(ctx); err != nil {
				return err
			}
		}
		if _, err := client.Project.Update().Where(project.IDEQ(projectID)).SetUpdatedAt(timeutil.NowUTC()).Save(ctx); err != nil {
			return err
		}
		if _, err := NewProjectService(client, NewUserService(client, nil)).requireProjectAccess(ctx, actor, projectID, true); err != nil {
			return err
		}
		return apply(client)
	})
}

func (s *GlossarySyncService) withSyncSnapshotTransaction(ctx context.Context, apply func(*ent.Client) error) error {
	// One absolute policy/caller deadline covers discovery, BeginTx, every SQL
	// statement and every retry. The database boundary also applies it to SQL
	// whose callback captures an earlier, unbounded context.
	ctx, cancel := context.WithTimeout(ctx, organizationTransactionRetryBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	ctx = database.WithJoinedCleanupDeadline(database.WithJoinedRollback(ctx), deadline)
	var databaseDialect string
	if _, err := s.client.SyncTask.Query().Select(synctask.FieldID).Limit(1).Modify(func(q *sql.Selector) { databaseDialect = q.Dialect() }).Ints(ctx); err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		if err := organizationTransactionContextError(ctx); err != nil {
			return err
		}
		var err error
		var tx *ent.Tx
		if databaseDialect == dialect.Postgres {
			tx, err = s.client.BeginTx(ctx, &stdsql.TxOptions{Isolation: stdsql.LevelRepeatableRead})
		} else {
			tx, err = s.client.Tx(ctx)
		}
		if err != nil {
			return errors.Join(organizationTransactionContextError(ctx), err)
		}
		err = func() error {
			defer tx.Rollback()
			client := tx.Client()
			if err := apply(client); err != nil {
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					return &organizationRollbackError{cause: err, rollback: rollbackErr}
				}
				return err
			}
			if err := tx.Commit(); err != nil {
				return &organizationCommitError{cause: err}
			}
			return nil
		}()
		if contextErr := organizationTransactionContextError(ctx); err != nil && contextErr != nil {
			return errors.Join(contextErr, err)
		}
		if err == nil || !isOrganizationTransactionConflict(err) || !time.Now().Before(deadline) {
			return err
		}
		delay := min(organizationTransactionRetryDelay(attempt), time.Until(deadline))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
	}
}

func createSyncPlan(ctx context.Context, client *ent.Client, task *ent.SyncTask, entry *ent.GlossaryEntry, resourceIDs []int) (int, error) {
	query := func() *ent.SegmentQuery {
		return affectedSegmentsQuery(client, task.ProjectID, entry.Source, entry.CaseSensitive, task.OldTarget, resourceIDs)
	}
	upper, err := query().Order(ent.Desc(segment.FieldID)).Limit(1).Select(segment.FieldID).Ints(ctx)
	if err != nil || len(upper) == 0 {
		return 0, err
	}
	ordinal, after := 0, 0
	for {
		rows, err := query().Where(segment.IDGT(after), segment.IDLTE(upper[0])).Order(ent.Asc(segment.FieldID)).Limit(syncBatchSize).
			Select(segment.FieldID, segment.FieldResourceID).All(ctx)
		if err != nil {
			return 0, err
		}
		if len(rows) == 0 {
			return ordinal, nil
		}
		items := make([]*ent.SyncTaskItemCreate, 0, len(rows))
		for _, row := range rows {
			if row.ResourceID == nil {
				return 0, errors.New("sync plan segment has no resource")
			}
			items = append(items, client.SyncTaskItem.Create().SetTaskID(task.ID).SetOrdinal(ordinal).SetSegmentID(row.ID).SetResourceID(*row.ResourceID))
			ordinal++
		}
		if err := client.SyncTaskItem.CreateBulk(items...).Exec(ctx); err != nil {
			return 0, err
		}
		pageResources := syncResourceIDs(rows)
		existing, err := client.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(task.ID), synctaskresult.ResourceIDIn(pageResources...)).Select(synctaskresult.FieldResourceID).Ints(ctx)
		if err != nil {
			return 0, err
		}
		seen := make(map[int]bool, len(existing))
		for _, id := range existing {
			seen[id] = true
		}
		results := make([]*ent.SyncTaskResultCreate, 0, len(pageResources))
		for _, id := range pageResources {
			if !seen[id] {
				results = append(results, client.SyncTaskResult.Create().SetTaskID(task.ID).SetResourceID(id))
			}
		}
		if len(results) > 0 {
			if err := client.SyncTaskResult.CreateBulk(results...).Exec(ctx); err != nil {
				return 0, err
			}
		}
		after = rows[len(rows)-1].ID
	}
}

func loadSyncPlanEntry(ctx context.Context, client *ent.Client, projectID, entryID int, b *backend.BuildBounds) (*ent.GlossaryEntry, error) {
	var stats []struct{ Bytes int64 }
	query := func() *ent.GlossaryEntryQuery {
		return client.GlossaryEntry.Query().Where(glossaryentry.IDEQ(entryID), glossaryentry.ProjectIDEQ(projectID))
	}
	if err := query().Select(glossaryentry.FieldID).Modify(func(q *sql.Selector) { q.Select(inputTextLength(q, glossaryentry.FieldSource) + " AS bytes") }).Scan(ctx, &stats); err != nil {
		return nil, err
	}
	if len(stats) != 1 {
		return nil, ErrGlossaryEntryNotFound
	}
	if err := reserveSyncProjection(b, stats[0].Bytes, 1, int64(unsafe.Sizeof(ent.GlossaryEntry{}))); err != nil {
		return nil, err
	}
	return query().Select(glossaryentry.FieldID, glossaryentry.FieldSource, glossaryentry.FieldCaseSensitive).Only(ctx)
}

func validateSyncCheckpoint(task *ent.SyncTask) error {
	if task.CheckpointVersion != syncCheckpointVersion || task.TotalSegments <= 0 || task.NextSegmentIndex < 0 ||
		task.NextSegmentIndex > task.TotalSegments || task.ProcessedSegments != task.NextSegmentIndex ||
		task.TotalUpdated < 0 || task.TotalUpdated > task.NextSegmentIndex || task.TotalSkipped != task.NextSegmentIndex-task.TotalUpdated {
		return syncCheckpointError("version, position or counts")
	}
	return nil
}

// Full plan cardinality and accumulated totals are validated once at recovery
// or claim using scalar projections, rather than reading all IDs every batch.
func validateSyncPlanSummary(ctx context.Context, client *ent.Client, task *ent.SyncTask) error {
	if err := validateSyncCheckpoint(task); err != nil {
		return err
	}
	var plan []struct{ Count, First, Last int }
	if err := client.SyncTaskItem.Query().Where(synctaskitem.TaskIDEQ(task.ID)).Select(synctaskitem.FieldID).Modify(func(q *sql.Selector) {
		q.Select("COUNT(*) AS count", "COALESCE(MIN("+q.C(synctaskitem.FieldOrdinal)+"),0) AS first", "COALESCE(MAX("+q.C(synctaskitem.FieldOrdinal)+"),0) AS last")
	}).Scan(ctx, &plan); err != nil {
		return err
	}
	if len(plan) != 1 || plan[0].Count != task.TotalSegments || plan[0].First != 0 || plan[0].Last != task.TotalSegments-1 {
		return syncCheckpointError("plan cardinality")
	}
	var result []struct{ Updated, Skipped int }
	if err := client.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(task.ID)).Select(synctaskresult.FieldID).Modify(func(q *sql.Selector) {
		q.Select("COALESCE(SUM("+q.C(synctaskresult.FieldUpdatedCount)+"),0) AS updated", "COALESCE(SUM("+q.C(synctaskresult.FieldSkippedCount)+"),0) AS skipped")
	}).Scan(ctx, &result); err != nil {
		return err
	}
	if len(result) != 1 || result[0].Updated != task.TotalUpdated || result[0].Skipped < 0 || result[0].Skipped > task.TotalSkipped {
		return syncCheckpointError("accumulated result")
	}
	return nil
}

func hydrateSyncTaskResult(ctx context.Context, client *ent.Client, task *ent.SyncTask) error {
	var stats []struct {
		ResultBytes int64 `sql:"result_bytes"`
		ErrorBytes  int64 `sql:"error_bytes"`
	}
	if err := client.SyncTask.Query().Where(synctask.IDEQ(task.ID)).Select(synctask.FieldID).Modify(func(q *sql.Selector) {
		q.Select(inputTextLength(q, synctask.FieldResult)+" AS result_bytes", inputTextLength(q, synctask.FieldError)+" AS error_bytes")
	}).Scan(ctx, &stats); err != nil {
		return err
	}
	if len(stats) != 1 || stats[0].ResultBytes < 0 || stats[0].ErrorBytes < 0 || stats[0].ResultBytes > DefaultExecutionInputBytes-stats[0].ErrorBytes {
		return backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, DefaultExecutionInputBytes)
	if err != nil {
		return err
	}
	defer b.CloseRetired()
	if err := reserveSyncProjection(b, stats[0].ResultBytes+stats[0].ErrorBytes, 1, int64(unsafe.Sizeof(ent.SyncTask{}))); err != nil {
		return err
	}
	row, err := client.SyncTask.Query().Where(synctask.IDEQ(task.ID)).Select(synctask.FieldResult, synctask.FieldError).Only(ctx)
	if err != nil {
		return err
	}
	task.Error = row.Error
	if row.Result != "" {
		task.Result = row.Result
		return nil
	}
	if task.CheckpointVersion == syncCheckpointVersion {
		task.Result, err = syncResultSnapshot(ctx, client, task)
	}
	return err
}

func syncPlanPage(ctx context.Context, client *ent.Client, task *ent.SyncTask) ([]*ent.SyncTaskItem, error) {
	if err := validateSyncCheckpoint(task); err != nil {
		return nil, err
	}
	want := min(syncBatchSize, task.TotalSegments-task.NextSegmentIndex)
	rows, err := client.SyncTaskItem.Query().Where(synctaskitem.TaskIDEQ(task.ID), synctaskitem.OrdinalGTE(task.NextSegmentIndex)).
		Order(ent.Asc(synctaskitem.FieldOrdinal)).Limit(want).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) != want {
		return nil, syncCheckpointError("incomplete plan page")
	}
	for i, row := range rows {
		if row.Ordinal != task.NextSegmentIndex+i || row.SegmentID <= 0 || row.ResourceID < 0 {
			return nil, syncCheckpointError("plan position")
		}
	}
	return rows, nil
}

// SyncTaskResourceIDs pages the fixed lock set without decoding a plan. The
// worker keeps its sorted acquisition order and holds locks through execution.
func (s *GlossarySyncService) SyncTaskResourceIDs(ctx context.Context, taskID, afterID, limit int) ([]int, error) {
	if taskID <= 0 || afterID < 0 || limit <= 0 {
		return nil, ErrInvalidInput
	}
	return s.client.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(taskID), synctaskresult.ResourceIDGT(afterID)).
		Order(ent.Asc(synctaskresult.FieldResourceID)).Limit(min(limit, syncBatchSize)).Select(synctaskresult.FieldResourceID).Ints(ctx)
}

func reserveSyncProjection(b *backend.BuildBounds, bytes, count, unit int64) error {
	objects, err := backend.AllocationBytes(count, unit)
	if err != nil {
		return err
	}
	credit, err := backend.CheckedAdd(bytes, objects)
	if err != nil || bytes < 0 || credit > b.InputLimit()-b.Snapshot().LiveBytes {
		return backend.ErrBuildCapacity
	}
	if err := b.ReserveText(bytes, bytes); err != nil {
		return err
	}
	return b.ReserveAllocation(count, unit)
}

func loadSyncBatchBodies(ctx context.Context, client *ent.Client, task *ent.SyncTask, ids, resourceIDs []int, b *backend.BuildBounds) (*ent.GlossaryEntry, *ent.Project, []*ent.Segment, []*ent.Resource, error) {
	var stats []struct{ Bytes int64 }
	err := client.SyncTask.Query().Where(synctask.IDEQ(task.ID)).Select(synctask.FieldID).Modify(func(q *sql.Selector) {
		q.Select(inputTextLength(q, synctask.FieldOldTarget) + "+" + inputTextLength(q, synctask.FieldNewTarget) + " AS bytes")
	}).Scan(ctx, &stats)
	if err != nil || len(stats) != 1 {
		return nil, nil, nil, nil, errors.Join(err, errors.New("missing sync task body"))
	}
	if err := reserveSyncProjection(b, stats[0].Bytes, 1, int64(unsafe.Sizeof(ent.SyncTask{}))); err != nil {
		return nil, nil, nil, nil, err
	}
	body, err := client.SyncTask.Query().Where(synctask.IDEQ(task.ID)).Select(synctask.FieldOldTarget, synctask.FieldNewTarget).Only(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	task.OldTarget, task.NewTarget = body.OldTarget, body.NewTarget
	stats = nil
	err = client.GlossaryEntry.Query().Where(glossaryentry.IDEQ(task.EntryID), glossaryentry.ProjectIDEQ(task.ProjectID)).Select(glossaryentry.FieldID).Modify(func(q *sql.Selector) {
		q.Select(inputTextLength(q, glossaryentry.FieldSource) + " AS bytes")
	}).Scan(ctx, &stats)
	if err != nil || len(stats) != 1 {
		return nil, nil, nil, nil, errors.Join(err, ErrGlossaryEntryNotFound)
	}
	if err := reserveSyncProjection(b, stats[0].Bytes, 1, int64(unsafe.Sizeof(ent.GlossaryEntry{}))); err != nil {
		return nil, nil, nil, nil, err
	}
	entry, err := client.GlossaryEntry.Query().Where(glossaryentry.IDEQ(task.EntryID), glossaryentry.ProjectIDEQ(task.ProjectID)).Select(glossaryentry.FieldSource, glossaryentry.FieldCaseSensitive).Only(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	projectRow, err := client.Project.Query().Where(project.IDEQ(task.ProjectID)).Select(project.FieldTargetLang).Only(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stats = nil
	segmentQuery := func() *ent.SegmentQuery {
		return client.Segment.Query().Where(segment.IDIn(ids...), segment.HasResourceWith(resource.ProjectIDEQ(task.ProjectID)))
	}
	err = segmentQuery().Select(segment.FieldID).Modify(func(q *sql.Selector) {
		q.Select("COALESCE(SUM(" + inputTextLength(q, segment.FieldSourceText) + "+" + inputTextLength(q, segment.FieldTargetText) + "),0) AS bytes")
	}).Scan(ctx, &stats)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(stats) != 1 {
		return nil, nil, nil, nil, backend.ErrBuildCapacity
	}
	if err := reserveSyncProjection(b, stats[0].Bytes, int64(len(ids)), int64(unsafe.Sizeof(ent.Segment{}))); err != nil {
		return nil, nil, nil, nil, err
	}
	rows, err := segmentQuery().Select(segment.FieldID, segment.FieldResourceID, segment.FieldSourceText, segment.FieldTargetText, segment.FieldContentVersion, segment.FieldStatus).All(ctx)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stats = nil
	resourceQuery := func() *ent.ResourceQuery {
		return client.Resource.Query().Where(resource.IDIn(resourceIDs...), resource.ProjectIDEQ(task.ProjectID))
	}
	err = resourceQuery().Select(resource.FieldID).Modify(func(q *sql.Selector) {
		q.Select("COALESCE(SUM(" + inputTextLength(q, resource.FieldPath) + "+" + inputTextLength(q, resource.FieldFormat) + "),0) AS bytes")
	}).Scan(ctx, &stats)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(stats) != 1 {
		return nil, nil, nil, nil, backend.ErrBuildCapacity
	}
	if err := reserveSyncProjection(b, stats[0].Bytes, int64(len(resourceIDs)), int64(unsafe.Sizeof(ent.Resource{}))); err != nil {
		return nil, nil, nil, nil, err
	}
	resources, err := resourceQuery().Order(ent.Asc(resource.FieldID)).Select(resource.FieldID, resource.FieldPath, resource.FieldFormat, resource.FieldSourceGeneration).All(ctx)
	return entry, projectRow, rows, resources, err
}

// syncResultSnapshot is called once at a terminal transition or for a requested
// API snapshot, never for every batch. SQL byte/count preflight precedes paths.
func syncResultSnapshot(ctx context.Context, client *ent.Client, task *ent.SyncTask) (string, error) {
	query := func() *ent.SyncTaskResultQuery {
		return client.SyncTaskResult.Query().Where(synctaskresult.TaskIDEQ(task.ID), synctaskresult.Or(synctaskresult.UpdatedCountGT(0), synctaskresult.SkippedCountGT(0)))
	}
	var stats []struct{ Count, Bytes int64 }
	if err := query().Select(synctaskresult.FieldID).Modify(func(q *sql.Selector) {
		q.Select("COUNT(*) AS count", "COALESCE(SUM("+inputTextLength(q, synctaskresult.FieldResourcePath)+"),0) AS bytes")
	}).Scan(ctx, &stats); err != nil {
		return "", err
	}
	if len(stats) != 1 {
		return "", backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, DefaultExecutionInputBytes, DefaultExecutionInputBytes)
	if err != nil {
		return "", err
	}
	defer b.CloseRetired()
	if err := reserveSyncProjection(b, stats[0].Bytes, stats[0].Count, int64(unsafe.Sizeof(GlossarySyncExecuteResourceResult{}))+int64(unsafe.Sizeof((*GlossarySyncExecuteResourceResult)(nil)))); err != nil {
		return "", err
	}
	rows, err := query().Order(ent.Asc(synctaskresult.FieldResourceID)).All(ctx)
	if err != nil {
		return "", err
	}
	if int64(len(rows)) != stats[0].Count {
		return "", errors.New("sync result snapshot changed")
	}
	result := glossarySyncResultJSON{TotalUpdated: task.TotalUpdated, TotalSkipped: task.TotalSkipped, Resources: make([]*GlossarySyncExecuteResourceResult, 0, len(rows))}
	updated, skipped := 0, 0
	for _, row := range rows {
		if row.UpdatedCount < 0 || row.SkippedCount < 0 || row.UpdatedCount > task.TotalUpdated-updated || row.SkippedCount > task.TotalSkipped-skipped {
			return "", errors.New("inconsistent sync resource counts")
		}
		updated += row.UpdatedCount
		skipped += row.SkippedCount
		result.Resources = append(result.Resources, &GlossarySyncExecuteResourceResult{ResourceID: row.ResourceID, ResourcePath: row.ResourcePath, UpdatedCount: row.UpdatedCount, SkippedCount: row.SkippedCount})
	}
	if updated != task.TotalUpdated {
		return "", errors.New("incomplete sync resource updates")
	}
	raw, err := b.EncodeJSON(result, DefaultExecutionInputBytes)
	if err != nil {
		return "", fmt.Errorf("build sync result: %w", err)
	}
	if err := b.ReserveText(int64(len(raw)), int64(len(raw))); err != nil {
		return "", err
	}
	return string(raw), nil
}

// Match folded runes while retaining original byte positions. Lowercasing full
// strings would allocate another body and can change byte offsets (for example İ).
func syncFoldSpan(text, term string, at int) (int, bool) {
	pos := at
	for _, want := range term {
		if pos >= len(text) {
			return pos, false
		}
		r, width := utf8.DecodeRuneInString(text[pos:])
		if unicode.ToLower(r) != unicode.ToLower(want) {
			return pos, false
		}
		pos += width
	}
	return pos, term != ""
}

func syncContainsFold(text, term string) bool {
	if term == "" {
		return true
	}
	for at := range text {
		if _, ok := syncFoldSpan(text, term, at); ok {
			return true
		}
	}
	return false
}

func syncCaseInsensitiveReplace(b *backend.BuildBounds, text, old, replacement string) (string, bool, error) {
	if old == "" {
		return text, false, nil
	}
	size, matches := int64(len(text)), 0
	for pos := 0; pos < len(text); {
		end, ok := syncFoldSpan(text, old, pos)
		if ok {
			size -= int64(end - pos)
			if int64(len(replacement)) > b.OutputLimit()-size {
				return "", false, backend.ErrBuildCapacity
			}
			size += int64(len(replacement))
			matches++
			pos = end
		} else {
			_, width := utf8.DecodeRuneInString(text[pos:])
			pos += width
		}
	}
	if matches == 0 {
		return text, false, nil
	}
	w := backend.NewBoundedWriter(b, size)
	defer w.Release()
	for pos := 0; pos < len(text); {
		end, ok := syncFoldSpan(text, old, pos)
		if ok {
			if _, err := w.WriteString(replacement); err != nil {
				return "", false, err
			}
			pos = end
		} else {
			_, width := utf8.DecodeRuneInString(text[pos:])
			if _, err := w.WriteString(text[pos : pos+width]); err != nil {
				return "", false, err
			}
			pos += width
		}
	}
	if err := b.ReserveText(size, size); err != nil {
		return "", false, err
	}
	return string(w.Bytes()), true, nil
}
