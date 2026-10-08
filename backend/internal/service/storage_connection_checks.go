package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagecheck"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagecheckwrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
)

type StorageCheckSpaceResult struct {
	SpaceID   int    `json:"space_id"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

// StorageCheckResult contains management-safe facts, never provider identities.
type StorageCheckResult struct {
	CheckID                int                       `json:"check_id"`
	ConnectionID           int                       `json:"connection_id"`
	Mode                   string                    `json:"mode"`
	ManagementGeneration   int64                     `json:"management_generation"`
	CreatedAt              time.Time                 `json:"created_at"`
	CompletedAt            *time.Time                `json:"completed_at"`
	Status                 string                    `json:"status"`
	ErrorCode              string                    `json:"error_code,omitempty"`
	AuthorizationActivated bool                      `json:"authorization_activated"`
	Results                []StorageCheckSpaceResult `json:"results"`
	CleanupStatus          string                    `json:"cleanup_status"`
	AccountedBytes         int64                     `json:"accounted_bytes"`
}

type storageCheckContextKey struct{}

func (s *StorageConnectionService) Authorize(ctx context.Context, actor, id int, in AuthorizeStorageInput) (*StorageConnectionRecord, error) {
	record, _, err := s.AuthorizeWithCheck(ctx, actor, id, in)
	return record, err
}

func (s *StorageConnectionService) AuthorizeWithCheck(ctx context.Context, actor, id int, in AuthorizeStorageInput) (*StorageConnectionRecord, *StorageCheckResult, error) {
	return s.runCheck(ctx, actor, id, in.WriteCheck, in.ExpectedManagementGeneration, true, func(ctx context.Context) (*StorageConnectionRecord, error) { return s.authorize(ctx, actor, id, in) })
}

func (s *StorageConnectionService) Check(ctx context.Context, actor, id int, write bool, generation int64) (*StorageConnectionRecord, error) {
	record, _, err := s.CheckWithResult(ctx, actor, id, write, generation)
	return record, err
}

func (s *StorageConnectionService) CheckWithResult(ctx context.Context, actor, id int, write bool, generation int64) (*StorageConnectionRecord, *StorageCheckResult, error) {
	return s.runCheck(ctx, actor, id, write, generation, false, func(ctx context.Context) (*StorageConnectionRecord, error) {
		return s.check(ctx, actor, id, write, generation)
	})
}

func (s *StorageConnectionService) runCheck(ctx context.Context, actor, id int, write bool, generation int64, authorize bool, run func(context.Context) (*StorageConnectionRecord, error)) (*StorageConnectionRecord, *StorageCheckResult, error) {
	ctx, cancelCheck := context.WithTimeout(ctx, s.cfg.TransferTimeout)
	defer cancelCheck()
	connection, err := s.authorized(ctx, s.client, actor, id)
	if err != nil {
		return nil, nil, err
	}
	if connection.ManagementGeneration != generation {
		return nil, nil, ErrStorageConflict
	}
	spaces, err := s.client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(id)).Order(ent.Asc(storagespace.FieldID)).Limit(101).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	results := make([]StorageCheckSpaceResult, 0, len(spaces))
	for _, space := range spaces {
		status := "not_checked"
		if space.Status == storagespace.StatusDisabled {
			status = "skipped"
		}
		results = append(results, StorageCheckSpaceResult{SpaceID: space.ID, Status: status})
	}
	data, err := json.Marshal(results)
	if err != nil {
		return nil, nil, err
	}
	mode := "read_only"
	if write {
		mode = "write"
	}
	deadline, _ := ctx.Deadline()
	check, err := s.client.StorageCheck.Create().SetConnectionID(id).SetActorID(actor).SetMode(mode).SetManagementGeneration(generation).SetDeadline(deadline.Add(5 * time.Second)).SetResults(data).Save(ctx)
	if err != nil {
		return nil, nil, err
	}
	ctx = context.WithValue(ctx, storageCheckContextKey{}, check.ID)
	record, runErr := run(ctx)
	// A disconnected caller must not discard the durable outcome.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	status := "completed"
	code := ""
	if runErr != nil {
		status = "failed"
		code = StorageErrorCode(runErr)
	}
	finishErr := s.client.StorageCheck.UpdateOneID(check.ID).SetStatus(status).SetErrorCode(code).SetCompletedAt(time.Now().UTC()).Exec(finishCtx)
	result, readErr := s.checkResult(finishCtx, check.ID)
	if result == nil {
		result = &StorageCheckResult{CheckID: check.ID, ConnectionID: id, Mode: mode, ManagementGeneration: generation, CreatedAt: check.CreatedAt, Status: status, ErrorCode: code, Results: results, CleanupStatus: "cleanup_pending"}
	}
	if runErr != nil {
		return record, result, runErr
	}
	if finishErr != nil {
		return record, result, finishErr
	}
	return record, result, readErr
}

func (s *StorageConnectionService) recordCheckSpace(ctx context.Context, spaceID int, cause error) error {
	checkID, ok := ctx.Value(storageCheckContextKey{}).(int)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	check, err := s.client.StorageCheck.Get(ctx, checkID)
	if err != nil {
		return err
	}
	var results []StorageCheckSpaceResult
	if err = json.Unmarshal(check.Results, &results); err != nil {
		return err
	}
	for i := range results {
		if results[i].SpaceID == spaceID {
			results[i].Status = "completed"
			results[i].ErrorCode = ""
			if cause != nil {
				results[i].Status = "failed"
				results[i].ErrorCode = StorageErrorCode(cause)
			}
		}
	}
	data, err := json.Marshal(results)
	if err != nil {
		return err
	}
	return s.client.StorageCheck.UpdateOneID(checkID).SetResults(data).Exec(ctx)
}

func (s *StorageConnectionService) associateCheckWrite(ctx context.Context, writeID int) error {
	checkID, ok := ctx.Value(storageCheckContextKey{}).(int)
	if !ok {
		return nil
	}
	exists, err := s.client.StorageCheckWrite.Query().Where(storagecheckwrite.CheckIDEQ(checkID), storagecheckwrite.WriteIDEQ(writeID)).Exist(ctx)
	if err != nil || exists {
		return err
	}
	return s.client.StorageCheckWrite.Create().SetCheckID(checkID).SetWriteID(writeID).Exec(ctx)
}

func (s *StorageConnectionService) checkResult(ctx context.Context, id int) (*StorageCheckResult, error) {
	row, err := s.client.StorageCheck.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Status == "running" && row.Deadline != nil && !row.Deadline.After(time.Now().UTC()) {
		if err = s.finalizeInterruptedCheck(ctx, row); err != nil {
			return nil, err
		}
		row, err = s.client.StorageCheck.Get(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	out := &StorageCheckResult{CheckID: row.ID, ConnectionID: row.ConnectionID, Mode: row.Mode, ManagementGeneration: row.ManagementGeneration, CreatedAt: row.CreatedAt, CompletedAt: row.CompletedAt, Status: row.Status, ErrorCode: row.ErrorCode, AuthorizationActivated: row.AuthorizationActivated, Results: []StorageCheckSpaceResult{}, CleanupStatus: "done"}
	if len(row.Results) > 0 {
		if err = json.Unmarshal(row.Results, &out.Results); err != nil {
			return nil, err
		}
	}
	links, err := s.client.StorageCheckWrite.Query().Where(storagecheckwrite.CheckIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(links))
	for _, link := range links {
		ids = append(ids, link.WriteID)
	}
	if len(ids) == 0 {
		return out, nil
	}
	writes, err := s.client.StorageWrite.Query().Where(storagewrite.IDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range writes {
		if w.Phase != "cleaned" && w.Phase != "committed" {
			if w.Phase == "reconcile" {
				out.CleanupStatus = "blocked"
			} else if out.CleanupStatus != "blocked" {
				out.CleanupStatus = "cleanup_pending"
			}
		}
	}
	reservations, err := s.client.StorageReservation.Query().Where(storagereservation.WriteIDIn(ids...), storagereservation.StateNEQ(storagereservation.StateFreed)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range reservations {
		out.AccountedBytes += r.Bytes
	}
	return out, nil
}

func (s *StorageConnectionService) GetCheck(ctx context.Context, actor, connectionID, checkID int) (*StorageCheckResult, error) {
	if _, err := s.authorized(ctx, s.client, actor, connectionID); err != nil {
		return nil, err
	}
	row, err := s.client.StorageCheck.Query().Where(storagecheck.IDEQ(checkID), storagecheck.ConnectionIDEQ(connectionID)).Only(ctx)
	if err != nil {
		return nil, err
	}
	return s.checkResult(ctx, row.ID)
}

func (s *StorageConnectionService) ListChecks(ctx context.Context, actor, connectionID, afterID, limit int) ([]*StorageCheckResult, error) {
	if _, err := s.authorized(ctx, s.client, actor, connectionID); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || afterID < 0 {
		return nil, ErrInvalidInput
	}
	q := s.client.StorageCheck.Query().Where(storagecheck.ConnectionIDEQ(connectionID)).Order(ent.Desc(storagecheck.FieldID)).Limit(limit)
	if afterID > 0 {
		q.Where(storagecheck.IDLT(afterID))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*StorageCheckResult, 0, len(rows))
	for _, row := range rows {
		r, e := s.checkResult(ctx, row.ID)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}

// Only checks beyond their bounded execution budget may be finalized after a restart.
// Authorization activation is already recorded in the activation transaction.
func (s *StorageConnectionService) recoverInterruptedChecks(ctx context.Context) error {
	rows, err := s.client.StorageCheck.Query().Where(storagecheck.StatusEQ("running"), storagecheck.DeadlineLTE(time.Now().UTC())).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err = s.finalizeInterruptedCheck(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (s *StorageConnectionService) finalizeInterruptedCheck(ctx context.Context, row *ent.StorageCheck) error {
	status, code := "failed", "storage_timeout"
	if row.AuthorizationActivated {
		status, code = "completed", ""
	}
	return s.client.StorageCheck.Update().Where(storagecheck.IDEQ(row.ID), storagecheck.StatusEQ("running"), storagecheck.DeadlineLTE(time.Now().UTC()), storagecheck.AuthorizationActivatedEQ(row.AuthorizationActivated)).SetStatus(status).SetErrorCode(code).SetCompletedAt(time.Now().UTC()).Exec(ctx)
}
