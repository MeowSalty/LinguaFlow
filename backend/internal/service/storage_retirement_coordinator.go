package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagepathclaim"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

type RetirementCreateInput struct {
	PreviewRevision string                  `json:"preview_revision"`
	ConfirmedScope  RetirementTarget        `json:"confirmed_scope"`
	Plan            []RetirementProjectPlan `json:"plan"`
}
type RetirementPlanInput struct {
	RetirementCreateInput
	ExpectedGeneration int64 `json:"expected_generation"`
}

func retirementHash(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func retirementScope(r *ent.StorageRetirement) (RetirementTarget, error) {
	var t RetirementTarget
	err := json.Unmarshal(r.ConfirmedScope, &t)
	return t, err
}
func retirementSpaceIDs(t RetirementTarget) []int {
	ids := make([]int, 0, len(t.Spaces))
	for _, sp := range t.Spaces {
		ids = append(ids, sp.SpaceID)
	}
	sort.Ints(ids)
	return ids
}
func retirementSameIdentity(a, b RetirementTarget) bool {
	if a.TargetKind != b.TargetKind || a.TargetID != b.TargetID || a.TargetIdentity != b.TargetIdentity || len(a.Spaces) != len(b.Spaces) {
		return false
	}
	for i, sp := range a.Spaces {
		other := b.Spaces[i]
		if sp.SpaceID != other.SpaceID || sp.SpaceIdentity != other.SpaceIdentity || sp.ConnectionID != other.ConnectionID {
			return false
		}
	}
	return true
}

func (s *StorageRetirementService) Create(ctx context.Context, actor int, key string, in RetirementCreateInput) (*ent.StorageRetirement, error) {
	if err := ValidateStorageIdempotencyKey(key); err != nil {
		return nil, err
	}
	if s.local {
		return nil, storage.ErrUnsupported
	}
	if in.Plan == nil || in.PreviewRevision == "" {
		return nil, ErrInvalidInput
	}
	requestHash := retirementHash(in)
	var id int
	err := s.storage.ioGuard.Exclusive(func() error {
		return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			if err := s.connections.requireOwner(ctx, tx, actor, string(in.ConfirmedScope.Scope), in.ConfirmedScope.OwnerID); err != nil {
				return err
			}
			prior, err := tx.StorageRetirement.Query().Where(storageretirement.OwnerKindEQ(storageretirement.OwnerKind(in.ConfirmedScope.Scope)), storageretirement.OwnerIDEQ(in.ConfirmedScope.OwnerID), storageretirement.TargetKindEQ(storageretirement.TargetKind(in.ConfirmedScope.TargetKind)), storageretirement.TargetIdentityEQ(in.ConfirmedScope.TargetIdentity), storageretirement.IdempotencyKeyEQ(key)).Only(ctx)
			if err == nil {
				if prior.RequestHash != requestHash {
					return ErrStorageIdempotency
				}
				id = prior.ID
				return nil
			}
			if !ent.IsNotFound(err) {
				return err
			}
			if s.cfg.Maintenance {
				return ErrStorageMaintenance
			}
			target, err := s.normalizeTarget(ctx, tx, actor, string(in.ConfirmedScope.TargetKind), in.ConfirmedScope.TargetID)
			if err != nil {
				return err
			}
			if err = s.lockScope(ctx, tx, target); err != nil {
				return err
			}
			target, err = s.normalizeTarget(ctx, tx, actor, string(in.ConfirmedScope.TargetKind), in.ConfirmedScope.TargetID)
			if err != nil {
				return err
			}
			if retirementHash(target) != retirementHash(in.ConfirmedScope) {
				return ErrStorageRetirementPreviewStale
			}
			inv, err := s.inventory(ctx, tx, target)
			if err != nil {
				return err
			}
			if err = s.verifyPreview(in.PreviewRevision, actor, retirementPreviewFingerprint(inv.Fingerprint, in.Plan)); err != nil {
				return err
			}
			if err = s.checkDefaults(ctx, tx, target); err != nil {
				return err
			}
			if err = s.scopeAvailable(ctx, tx, target); err != nil {
				return err
			}
			scope, err := json.Marshal(target)
			if err != nil {
				return err
			}
			r, err := tx.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKind(target.TargetKind)).SetTargetID(target.TargetID).SetTargetIdentity(target.TargetIdentity).SetOwnerKind(storageretirement.OwnerKind(target.Scope)).SetOwnerID(target.OwnerID).SetActorID(actor).SetConfirmedScope(scope).SetContractVersion(1).SetIdempotencyKey(key).SetRequestHash(requestHash).Save(ctx)
			if err != nil {
				return err
			}
			id = r.ID
			if target.TargetKind == "connection" {
				if err = tx.StorageConnection.UpdateOneID(target.TargetID).SetLifecycle(storageconnection.LifecycleRetiring).SetRetirementID(id).AddManagementGeneration(1).AddExecutionGeneration(1).Exec(ctx); err != nil {
					return err
				}
			}
			for _, sp := range target.Spaces {
				row, err := tx.StorageSpace.UpdateOneID(sp.SpaceID).SetLifecycle(storagespace.LifecycleRetiring).SetRetirementID(id).AddManagementGeneration(1).AddExecutionGeneration(1).Save(ctx)
				if err != nil {
					return err
				}
				if err = ensureStoragePathClaim(ctx, tx, row); err != nil {
					return err
				}
				if err = tx.StorageRetirementEntry.Create().SetRetirementID(id).SetEntryKey(fmt.Sprintf("space:%d", sp.SpaceID)).SetKind("space").SetSpaceID(sp.SpaceID).Exec(ctx); err != nil {
					return err
				}
			}
			plans := map[int]RetirementProjectPlan{}
			for _, p := range in.Plan {
				if _, ok := plans[p.ProjectID]; ok {
					return ErrInvalidInput
				}
				plans[p.ProjectID] = p
			}
			for _, p := range inv.Projects {
				entry, err := tx.StorageRetirementEntry.Create().SetRetirementID(id).SetEntryKey(fmt.Sprintf("project:%d", p.ID)).SetKind("project").SetProjectID(p.ID).Save(ctx)
				if err != nil {
					return err
				}
				if plan, ok := plans[p.ID]; ok {
					if err = s.acceptProject(ctx, tx, actor, r, target, entry, plan); err != nil {
						return err
					}
					delete(plans, p.ID)
				}
			}
			if len(plans) > 0 {
				return ErrInvalidInput
			}
			return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actor, Action: "storage.retirement.accept", ResourceType: "storage_retirement", ResourceID: id})
		})
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageRetirement.Get(ctx, id)
}

func (s *StorageRetirementService) lockScope(ctx context.Context, c *ent.Client, t RetirementTarget) error {
	conns := map[int]bool{}
	if t.TargetKind == "connection" {
		conns[t.TargetID] = true
	}
	for _, sp := range t.Spaces {
		conns[sp.ConnectionID] = true
	}
	ids := []int{}
	for id := range conns {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if err := storageguard.LockConnection(ctx, c, id); err != nil {
			return err
		}
	}
	for _, sp := range t.Spaces {
		row, err := c.StorageSpace.Get(ctx, sp.SpaceID)
		if err != nil {
			return err
		}
		if row.Bucket != "" {
			if err = storageguard.LockNamespace(ctx, c, row.Bucket); err != nil {
				return err
			}
		}
		if _, err = c.ExecContext(ctx, fmt.Sprintf("UPDATE storage_spaces SET management_generation=management_generation WHERE id=%d", sp.SpaceID)); err != nil {
			return err
		}
	}
	return nil
}
func (s *StorageRetirementService) scopeAvailable(ctx context.Context, c *ent.Client, t RetirementTarget) error {
	// An incoming migration past its cancellation boundary must finish before
	// its destination can retire; otherwise the new-use barrier would strand
	// an already partially published project.
	cutover, err := c.StorageTask.Query().Where(storagetask.KindEQ("migration"), storagetask.TargetSpaceIDIn(retirementSpaceIDs(t)...), storagetask.PhaseIn("cutover", "cleanup"), storagetask.StatusNEQ(storagetask.StatusCompleted)).Exist(ctx)
	if err != nil {
		return err
	}
	if cutover {
		return ErrStorageInProgress
	}
	if t.TargetKind == "connection" {
		conn, err := c.StorageConnection.Get(ctx, t.TargetID)
		if err != nil {
			return err
		}
		if conn.Lifecycle == storageconnection.LifecycleRemoved {
			return ErrStorageRemoved
		}
		if conn.Lifecycle != storageconnection.LifecycleAvailable || conn.RetirementID != nil {
			return ErrStorageRetirementConflict
		}
	}
	for _, sp := range t.Spaces {
		row, err := c.StorageSpace.Get(ctx, sp.SpaceID)
		if err != nil {
			return err
		}
		conn, err := c.StorageConnection.Get(ctx, row.ConnectionID)
		if err != nil {
			return err
		}
		if row.Lifecycle == storagespace.LifecycleRemoved || conn.Lifecycle == storageconnection.LifecycleRemoved {
			return ErrStorageRemoved
		}
		if row.RetirementID != nil || conn.RetirementID != nil || row.Lifecycle != storagespace.LifecycleAvailable || conn.Lifecycle != storageconnection.LifecycleAvailable {
			return ErrStorageRetirementConflict
		}
	}
	return nil
}
func (s *StorageRetirementService) checkDefaults(ctx context.Context, c *ent.Client, t RetirementTarget) error {
	for _, sp := range t.Spaces {
		if sp.SpaceID == s.storage.defaultSpaceID {
			return ErrStorageRetirementDependenciesRemaining
		}
	}
	if t.BackendID != nil && *t.BackendID == s.cfg.DefaultSiteSpace {
		return ErrStorageRetirementDependenciesRemaining
	}
	return nil
}

// Child acceptance, authorization and the parent's association commit together.
// The worker may only execute these durable accepted inputs, never impersonate
// the parent's original actor to start a new project migration later.
func (s *StorageRetirementService) acceptProject(ctx context.Context, tx *ent.Client, actor int, r *ent.StorageRetirement, scope RetirementTarget, entry *ent.StorageRetirementEntry, plan RetirementProjectPlan) error {
	if entry.AcceptedAt != nil {
		return ErrStorageRetirementConflict
	}
	for _, source := range scope.Spaces {
		if plan.TargetSpaceID == source.SpaceID {
			return ErrStoragePolicy
		}
	}
	projects := &ProjectService{client: tx, users: s.storage.projects.users, storage: s.storage}
	p, err := projects.requireProjectAccess(ctx, actor, plan.ProjectID, true)
	if err != nil {
		return err
	}
	needsMove, err := retirementProjectNeedsMove(ctx, tx, p.ID, retirementSpaceIDs(scope))
	if err != nil {
		return err
	}
	if !needsMove {
		return ErrStorageRetirementConflict
	}
	if p.StorageGeneration != plan.ExpectedStorageGeneration || p.StorageState != "active" {
		return ErrStorageConflict
	}
	if !storageCanIncrement(p.StorageGeneration) || p.StorageGeneration >= MaxStorageInteger-1 {
		return ErrInvalidInput
	}
	if err = storageProjectGate(ctx, tx, p.ID, p.StorageGeneration); err != nil {
		return err
	}
	if err = storageguard.Gate(ctx, tx, plan.TargetSpaceID, storageguard.NewUse); err != nil {
		return err
	}
	sp, err := tx.StorageSpace.Get(ctx, plan.TargetSpaceID)
	if err != nil {
		return err
	}
	if sp.ManagementGeneration != plan.ExpectedTargetManagementGeneration || sp.ExecutionGeneration != plan.ExpectedTargetExecutionGeneration {
		return ErrStorageConflict
	}
	size, err := storageMigrationSize(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if err = s.storage.validateStorageTarget(ctx, tx, p, sp.ID, size, false); err != nil {
		return err
	}
	update := tx.StorageRetirementEntry.UpdateOneID(entry.ID).SetTargetSpaceID(sp.ID).SetExpectedStorageGeneration(p.StorageGeneration).SetExpectedTargetManagementGeneration(sp.ManagementGeneration).SetExpectedTargetExecutionGeneration(sp.ExecutionGeneration).SetAcceptedAt(time.Now().UTC()).SetAcceptedBy(actor)
	emptyErr := storageEmptyProject(ctx, tx, p.ID)
	if emptyErr != nil && !errors.Is(emptyErr, ErrStorageConflict) {
		return emptyErr
	}
	if emptyErr == nil {
		if err = tx.Project.UpdateOneID(p.ID).SetStorageSpaceID(sp.ID).AddStorageGeneration(1).Exec(ctx); err != nil {
			return err
		}
		return update.SetStatus(storageretiremententry.StatusCompleted).Exec(ctx)
	}
	key := fmt.Sprintf("retirement:%d:project:%d", r.ID, p.ID)
	input := map[string]any{"retirement_id": r.ID, "target_space_id": sp.ID}
	task, err := tx.StorageTask.Create().SetOperationID(generateUniqueID()).SetContractVersion(1).SetIdempotencyKey(key).SetRequestHash(retirementHash(plan)).SetActorID(actor).SetProjectID(p.ID).SetKind("migration").SetTargetSpaceID(sp.ID).SetExpectedStorageGeneration(p.StorageGeneration).SetInput(input).SetDeadline(time.Now().UTC().Add(s.cfg.IntentTTL)).Save(ctx)
	if err != nil {
		return err
	}
	if err = s.storage.prepareMigration(ctx, tx, p, task, sp.ID, p.StorageGeneration); err != nil {
		return err
	}
	return update.SetMigrationTaskID(task.ID).SetStatus(storageretiremententry.StatusRunning).Exec(ctx)
}

func (s *StorageRetirementService) UpdatePlan(ctx context.Context, actor, id int, in RetirementPlanInput) (*ent.StorageRetirement, error) {
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		r, err := s.authorized(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if r.Generation != in.ExpectedGeneration {
			return ErrStorageRetirementConflict
		}
		if r.CancelRequested || r.Phase == "finalizing" || r.Phase == "waiting_configuration" || r.Status == storageretirement.StatusCompleted || r.Status == storageretirement.StatusCancelled {
			return ErrStorageRetirementConflict
		}
		if s.cfg.Maintenance {
			return ErrStorageMaintenance
		}
		old, err := retirementScope(r)
		if err != nil {
			return err
		}
		current, err := s.normalizeTarget(ctx, tx, actor, string(old.TargetKind), old.TargetID)
		if err != nil {
			return err
		}
		if !retirementSameIdentity(old, current) || retirementHash(current) != retirementHash(in.ConfirmedScope) {
			return ErrStorageRetirementPreviewStale
		}
		inv, err := s.inventory(ctx, tx, current)
		if err != nil {
			return err
		}
		if err = s.verifyPreview(in.PreviewRevision, actor, retirementPreviewFingerprint(inv.Fingerprint, in.Plan)); err != nil {
			return err
		}
		seen := map[int]bool{}
		for _, plan := range in.Plan {
			if seen[plan.ProjectID] {
				return ErrInvalidInput
			}
			seen[plan.ProjectID] = true
			entry, err := tx.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(id), storageretiremententry.KindEQ("project"), storageretiremententry.ProjectIDEQ(plan.ProjectID)).Only(ctx)
			if err != nil {
				return err
			}
			if err = s.acceptProject(ctx, tx, actor, r, old, entry, plan); err != nil {
				return err
			}
		}
		n, err := tx.StorageRetirement.Update().Where(storageretirement.IDEQ(id), storageretirement.GenerationEQ(in.ExpectedGeneration)).AddGeneration(1).SetStatus(storageretirement.StatusPending).ClearNextCheckAt().SetErrorCode("").Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageRetirementConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageRetirement.Get(ctx, id)
}

func (s *StorageRetirementService) Resume(ctx context.Context, actor, id int, generation int64) (*ent.StorageRetirement, error) {
	return s.action(ctx, actor, id, generation, false)
}
func (s *StorageRetirementService) Cancel(ctx context.Context, actor, id int, generation int64) (*ent.StorageRetirement, error) {
	return s.action(ctx, actor, id, generation, true)
}
func (s *StorageRetirementService) action(ctx context.Context, actor, id int, generation int64, cancel bool) (*ent.StorageRetirement, error) {
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		r, err := s.authorized(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if cancel && (r.CancelRequested || r.Status == storageretirement.StatusCancelled) {
			return nil
		}
		if r.Status == storageretirement.StatusCompleted {
			if cancel {
				return ErrStorageRetirementConflict
			}
			return nil
		}
		if r.Generation != generation || !storageCanIncrement(generation) {
			return ErrStorageRetirementConflict
		}
		if s.cfg.Maintenance {
			return ErrStorageMaintenance
		}
		if r.Status == storageretirement.StatusCancelled {
			return ErrStorageRetirementConflict
		}
		if cancel && (r.Phase == "finalizing" || r.Phase == "waiting_configuration") {
			return ErrStorageRetirementConflict
		}
		u := tx.StorageRetirement.Update().Where(storageretirement.IDEQ(id), storageretirement.GenerationEQ(generation)).AddGeneration(1).ClearNextCheckAt().SetErrorCode("").SetStatus(storageretirement.StatusPending)
		if cancel {
			u.SetCancelRequested(true)
		} else {
			u.SetAttempts(0).ClearRetryStartedAt()
		}
		n, err := u.Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageRetirementConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageRetirement.Get(ctx, id)
}

// Process runs bounded parent work. Pure retention waits use next_check_at and
// never consume the network-failure retry budget.
func (s *StorageRetirementService) Process(ctx context.Context) error {
	if s.cfg.Maintenance {
		return nil
	}
	now := time.Now().UTC()
	limit := s.cfg.ReconcileBatchSize
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.client.StorageRetirement.Query().Where(storageretirement.Or(storageretirement.StatusIn(storageretirement.StatusPending, storageretirement.StatusRunning, storageretirement.StatusWaitingRetry), storageretirement.And(storageretirement.StatusEQ(storageretirement.StatusNeedsAction), storageretirement.ErrorCodeEQ("storage_retirement_dependencies_remaining"), storageretirement.NextCheckAtNotNil())), storageretirement.Or(storageretirement.NextCheckAtIsNil(), storageretirement.NextCheckAtLTE(now))).Order(ent.Asc(storageretirement.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.ProcessOne(ctx, r.ID); err != nil && !errors.Is(err, storageguard.ErrBusy) {
			return err
		}
	}
	return nil
}

func (s *StorageRetirementService) ProcessOne(ctx context.Context, id int) error {
	s.runMu.Lock()
	if s.executing[id] {
		s.runMu.Unlock()
		return nil
	}
	s.executing[id] = true
	s.runMu.Unlock()
	defer func() { s.runMu.Lock(); delete(s.executing, id); s.runMu.Unlock() }()
	if s.cfg.Maintenance {
		return ErrStorageMaintenance
	}
	token := generateUniqueID()
	now := time.Now().UTC()
	ttl := s.cfg.TransferTimeout
	if ttl <= 0 {
		ttl = time.Minute
	}
	n, err := s.client.StorageRetirement.Update().Where(storageretirement.IDEQ(id), storageretirement.StatusNotIn(storageretirement.StatusCompleted, storageretirement.StatusCancelled, storageretirement.StatusFailed), storageretirement.Or(storageretirement.LeaseUntilIsNil(), storageretirement.LeaseUntilLTE(now))).SetLeaseToken(token).SetLeaseUntil(now.Add(ttl)).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.client.StorageRetirement.Update().Where(storageretirement.IDEQ(id), storageretirement.LeaseTokenEQ(token)).ClearLeaseUntil().SetLeaseToken("").Save(releaseCtx)
	}()
	r, err := s.client.StorageRetirement.Get(ctx, id)
	if err != nil {
		return err
	}
	if r.StartedAt == nil {
		if err = s.client.StorageRetirement.UpdateOneID(id).SetStartedAt(now).Exec(ctx); err != nil {
			return err
		}
	}
	if r.CancelRequested {
		err = s.cancelAccepted(ctx, r)
	} else if r.Phase == "finalizing" || r.Phase == "waiting_configuration" {
		err = s.Finalize(ctx, id)
	} else {
		err = s.advance(ctx, r)
	}
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	code := storageCode(err)
	state := storageretirement.StatusNeedsAction
	u := s.client.StorageRetirement.Update().Where(storageretirement.IDEQ(id), storageretirement.GenerationEQ(r.Generation), storageretirement.LeaseTokenEQ(token), storageretirement.StatusNotIn(storageretirement.StatusCompleted, storageretirement.StatusCancelled)).SetErrorCode(code).ClearNextCheckAt()
	if errors.Is(err, storageguard.ErrBusy) {
		state = storageretirement.StatusWaitingRetry
		u.SetNextCheckAt(now.Add(time.Second))
	} else if errors.Is(err, storage.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		u.AddAttempts(1)
		maxAttempts := s.cfg.RetryMaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		state = storageretirement.StatusFailed
		if r.Attempts+1 < maxAttempts {
			state = storageretirement.StatusWaitingRetry
			u.SetNextCheckAt(now.Add(time.Second << min(r.Attempts, 6)))
		}
	}
	_, e := u.SetStatus(state).Save(ctx)
	return e
}

func (s *StorageRetirementService) advance(ctx context.Context, r *ent.StorageRetirement) error {
	target, err := retirementScope(r)
	if err != nil {
		return err
	}
	ids := retirementSpaceIDs(target)
	// Accepted writes that have not published lose the right to publish. Their
	// original intentions retain responsibility for exact reconciliation.
	if len(ids) > 0 {
		// Accepted intentions may not have a StorageWrite yet. They still lose
		// publication rights and must not leave the parent waiting forever.
		if _, err := s.client.StorageTask.Update().Where(storagetask.TargetSpaceIDIn(ids...), storagetask.KindNotIn("migration", "storage_probe", "storage_marker"), storagetask.PhaseNEQ("committed"), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled, storagetask.StatusFailed)).SetStatus(storagetask.StatusCancelled).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx); err != nil {
			return err
		}
		incoming, err := s.client.StorageTask.Query().Where(storagetask.KindEQ("migration"), storagetask.TargetSpaceIDIn(ids...), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled)).All(ctx)
		if err != nil {
			return err
		}
		for _, task := range incoming {
			if err = s.storage.cancelMigration(ctx, task); err != nil {
				return err
			}
		}
		writes, err := s.client.StorageWrite.Query().Where(storagewrite.SpaceIDIn(ids...), storagewrite.PhaseNotIn("committed", "cleaned")).All(ctx)
		if err != nil {
			return err
		}
		for _, w := range writes {
			task, err := s.client.StorageTask.Get(ctx, w.TaskID)
			if err != nil {
				return err
			}
			if task.Kind == "storage_probe" || task.Kind == "storage_marker" {
				continue
			}
			if _, err = s.client.StorageTask.Update().Where(storagetask.IDEQ(task.ID), storagetask.PhaseNEQ("committed"), storagetask.StatusNEQ(storagetask.StatusCompleted)).SetStatus(storagetask.StatusCancelled).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx); err != nil {
				return err
			}
		}
	}
	entries, err := s.client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(r.ID), storageretiremententry.KindEQ("project")).All(ctx)
	if err != nil {
		return err
	}
	migrating := false
	for _, entry := range entries {
		if entry.MigrationTaskID == nil {
			continue
		}
		task, err := s.client.StorageTask.Get(ctx, *entry.MigrationTaskID)
		if err != nil {
			return err
		}
		state := storageretiremententry.Status(task.Status)
		if err = s.client.StorageRetirementEntry.UpdateOneID(entry.ID).SetStatus(state).SetErrorCode(task.ErrorCode).Exec(ctx); err != nil {
			return err
		}
		if task.Status == storagetask.StatusNeedsAction || task.Status == storagetask.StatusFailed || task.Status == storagetask.StatusCancelled {
			needs, err := retirementProjectNeedsMove(ctx, s.client, entry.ProjectID, ids)
			if err != nil {
				return err
			}
			if needs {
				return retirementChildError(task.ErrorCode)
			}
			if err = s.client.StorageRetirementEntry.UpdateOneID(entry.ID).SetStatus(storageretiremententry.StatusCompleted).SetErrorCode("").Exec(ctx); err != nil {
				return err
			}
			continue
		}
		if task.Status != storagetask.StatusCompleted {
			migrating = true
		}
	}
	inv, err := s.inventory(ctx, s.client, target)
	if err != nil {
		return err
	}
	if inv.Blocking || migrating {
		phase := storageretirement.PhaseWaitingCleanup
		if migrating {
			phase = storageretirement.PhaseMigrating
		}
		state := storageretirement.StatusWaitingRetry
		next := time.Now().UTC().Add(max(s.cfg.ReconcileInterval, time.Second))
		if inv.NextCheckAt != nil && inv.NextCheckAt.After(time.Now()) {
			next = *inv.NextCheckAt
		}
		// Missing plans require an explicit project manager action.
		for _, entry := range entries {
			if entry.AcceptedAt == nil {
				needs, err := retirementProjectNeedsMove(ctx, s.client, entry.ProjectID, ids)
				if err != nil {
					return err
				}
				if needs {
					state = storageretirement.StatusNeedsAction
				}
			}
		}
		u := s.client.StorageRetirement.Update().Where(storageretirement.IDEQ(r.ID), storageretirement.GenerationEQ(r.Generation), storageretirement.CancelRequestedEQ(false)).SetPhase(phase).SetStatus(state).SetNextCheckAt(next)
		if state == storageretirement.StatusNeedsAction {
			u.SetErrorCode("storage_retirement_dependencies_remaining")
		}
		_, err := u.Save(ctx)
		return err
	}
	return s.storage.ioGuard.Exclusive(func() error {
		if s.storage.ioGuard.BusyLocked(ids) {
			return storageguard.ErrBusy
		}
		return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			if err := s.lockScope(ctx, tx, target); err != nil {
				return err
			}
			current, err := tx.StorageRetirement.Get(ctx, r.ID)
			if err != nil {
				return err
			}
			if current.CancelRequested || current.Generation != r.Generation {
				return ErrStorageRetirementConflict
			}
			inv, err := s.inventory(ctx, tx, target)
			if err != nil {
				return err
			}
			if inv.Blocking {
				return ErrStorageRetirementDependenciesRemaining
			}
			if err = s.checkDefaults(ctx, tx, target); err != nil {
				return err
			}
			for _, sp := range target.Spaces {
				if err = tx.StorageSpace.UpdateOneID(sp.SpaceID).AddExecutionGeneration(1).Exec(ctx); err != nil {
					return err
				}
			}
			return tx.StorageRetirement.UpdateOneID(r.ID).SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusPending).AddGeneration(1).ClearNextCheckAt().SetErrorCode("").Exec(ctx)
		})
	})
}

func retirementProjectNeedsMove(ctx context.Context, c *ent.Client, id int, spaces []int) (bool, error) {
	p, err := c.Project.Get(ctx, id)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, sp := range spaces {
		if p.StorageSpaceID != nil && *p.StorageSpaceID == sp {
			return true, nil
		}
	}
	objects, err := c.Blob.Query().Where(blob.ProjectIDEQ(id), blob.ActiveLocationIDNotNil(), blob.StatusEQ(blob.StatusReady)).Select(blob.FieldActiveLocationID).All(ctx)
	if err != nil {
		return false, err
	}
	ids := []int{}
	for _, b := range objects {
		ids = append(ids, *b.ActiveLocationID)
	}
	if len(ids) == 0 || len(spaces) == 0 {
		return false, nil
	}
	return c.BlobLocation.Query().Where(bloblocation.IDIn(ids...), bloblocation.SpaceIDIn(spaces...)).Exist(ctx)
}
func retirementChildError(code string) error {
	switch code {
	case "source_missing":
		return storage.ErrNotFound
	case "source_corrupt":
		return storage.ErrCorrupt
	case "storage_auth_required":
		return storage.ErrAuthRequired
	case "storage_permission_denied":
		return storage.ErrPermission
	case "storage_quota_exceeded":
		return storage.ErrLimit
	case "storage_disk_insufficient":
		return storage.ErrDiskSpaceInsufficient
	case "storage_disk_probe_failed":
		return storage.ErrDiskSpaceUnknown
	case "storage_maintenance":
		return ErrStorageMaintenance
	case "storage_deployment_disabled":
		return ErrStorageDeploymentDisabled
	case "storage_policy_violation":
		return ErrStoragePolicy
	case "storage_timeout":
		return context.DeadlineExceeded
	case "storage_unavailable":
		return storage.ErrUnavailable
	}
	return ErrStorageRetirementDependenciesRemaining
}

func (s *StorageRetirementService) cancelAccepted(ctx context.Context, r *ent.StorageRetirement) error {
	if r.Phase == "finalizing" || r.Phase == "waiting_configuration" {
		return ErrStorageRetirementConflict
	}
	entries, err := s.client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(r.ID), storageretiremententry.MigrationTaskIDNotNil()).All(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		task, err := s.client.StorageTask.Get(ctx, *entry.MigrationTaskID)
		if err != nil {
			return err
		}
		if task.Status == storagetask.StatusCompleted {
			continue
		}
		if task.Phase == "cutover" || task.Phase == "cleanup" {
			return storageguard.ErrBusy
		}
		if err = s.storage.cancelMigration(ctx, task); err != nil {
			// The child can cross into cutover after the read above. Treat that
			// race as the same durable wait, rather than requiring manual resume.
			if errors.Is(err, ErrStorageConflict) {
				current, readErr := s.client.StorageTask.Get(ctx, task.ID)
				if readErr != nil {
					return readErr
				}
				if current.Status == storagetask.StatusCompleted {
					continue
				}
				if current.Phase == "cutover" || current.Phase == "cleanup" {
					return storageguard.ErrBusy
				}
			}
			return err
		}
	}
	target, err := retirementScope(r)
	if err != nil {
		return err
	}
	return s.storage.ioGuard.Exclusive(func() error {
		if s.storage.ioGuard.BusyLocked(retirementSpaceIDs(target)) {
			return storageguard.ErrBusy
		}
		return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			if err := s.lockScope(ctx, tx, target); err != nil {
				return err
			}
			for _, sp := range target.Spaces {
				n, err := tx.StorageSpace.Update().Where(storagespace.IDEQ(sp.SpaceID), storagespace.RetirementIDEQ(r.ID), storagespace.LifecycleEQ(storagespace.LifecycleRetiring)).SetLifecycle(storagespace.LifecycleAvailable).ClearRetirementID().AddManagementGeneration(1).AddExecutionGeneration(1).Save(ctx)
				if err != nil {
					return err
				}
				if n != 1 {
					return ErrStorageRetirementConflict
				}
			}
			if target.TargetKind == "connection" {
				if _, err := tx.StorageConnection.Update().Where(storageconnection.IDEQ(target.TargetID), storageconnection.RetirementIDEQ(r.ID)).SetLifecycle(storageconnection.LifecycleAvailable).ClearRetirementID().AddManagementGeneration(1).AddExecutionGeneration(1).Save(ctx); err != nil {
					return err
				}
			}
			return tx.StorageRetirement.UpdateOneID(r.ID).SetStatus(storageretirement.StatusCancelled).SetFinishedAt(time.Now().UTC()).ClearNextCheckAt().SetErrorCode("").AddGeneration(1).Exec(ctx)
		})
	})
}

// complete is called only after the finalizer has recorded all marker outcomes
// and, for deployment identities, observed removal from the loaded config.
func (s *StorageRetirementService) complete(ctx context.Context, id int) error {
	return s.storage.ioGuard.Exclusive(func() error {
		return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			r, err := tx.StorageRetirement.Get(ctx, id)
			if err != nil {
				return err
			}
			if r.Status == storageretirement.StatusCompleted {
				return nil
			}
			if r.Phase != "finalizing" && r.Phase != "waiting_configuration" {
				return ErrStorageRetirementConflict
			}
			target, err := retirementScope(r)
			if err != nil {
				return err
			}
			if s.storage.ioGuard.BusyLocked(retirementSpaceIDs(target)) {
				return storageguard.ErrBusy
			}
			if err = s.lockScope(ctx, tx, target); err != nil {
				return err
			}
			inv, err := s.inventory(ctx, tx, target)
			if err != nil {
				return err
			}
			if inv.Blocking {
				return ErrStorageRetirementDependenciesRemaining
			}
			if err = s.checkDefaults(ctx, tx, target); err != nil {
				return err
			}
			if target.BackendID != nil {
				for _, b := range s.cfg.Backends {
					if b.ID == *target.BackendID {
						return ErrStorageRetirementConfigurationPending
					}
				}
			}
			for _, sp := range target.Spaces {
				e, err := tx.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(id), storageretiremententry.EntryKeyEQ(fmt.Sprintf("marker:%d", sp.SpaceID)), storageretiremententry.StatusEQ(storageretiremententry.StatusCompleted)).Only(ctx)
				if err != nil || e == nil {
					return ErrStorageRetirementDependenciesRemaining
				}
				if _, err = tx.StorageSpace.Update().Where(storagespace.IDEQ(sp.SpaceID), storagespace.RetirementIDEQ(id)).SetLifecycle(storagespace.LifecycleRemoved).SetRemovedAt(time.Now().UTC()).SetRemovedBy(r.ActorID).AddManagementGeneration(1).AddExecutionGeneration(1).Save(ctx); err != nil {
					return err
				}
				if _, err = tx.StoragePathClaim.Delete().Where(storagepathclaim.SpaceIDEQ(sp.SpaceID), storagepathclaim.IdentityEQ(sp.SpaceIdentity)).Exec(ctx); err != nil {
					return err
				}
			}
			if err = s.purgeRetirementCredentials(ctx, tx, r); err != nil {
				return err
			}
			if target.TargetKind == "connection" {
				if err = tx.StorageConnection.UpdateOneID(target.TargetID).SetLifecycle(storageconnection.LifecycleRemoved).SetActiveAuthGeneration(0).SetRemovedAt(time.Now().UTC()).SetRemovedBy(r.ActorID).AddManagementGeneration(1).AddExecutionGeneration(1).Exec(ctx); err != nil {
					return err
				}
			}
			if err = tx.StorageRetirement.UpdateOneID(id).SetStatus(storageretirement.StatusCompleted).SetPhase(storageretirement.PhaseCompleted).SetFinishedAt(time.Now().UTC()).ClearNextCheckAt().SetErrorCode("").AddGeneration(1).Exec(ctx); err != nil {
				return err
			}
			return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: r.ActorID, Action: "storage.retirement.complete", ResourceType: "storage_retirement", ResourceID: id})
		})
	})
}
