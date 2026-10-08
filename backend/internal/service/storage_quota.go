package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// MaxStorageInteger is the largest integer exactly representable by API clients.
const MaxStorageInteger int64 = 1<<53 - 1

func validStorageQuota(limit *int64) bool {
	return limit == nil || (*limit > 0 && *limit <= MaxStorageInteger)
}

func storageLedgerTotal(sp *ent.StorageSpace) (int64, error) {
	var total int64
	for _, n := range []int64{sp.ReservedBytes, sp.CandidateBytes, sp.LiveBytes, sp.PendingDeleteBytes} {
		if n < 0 || n > MaxStorageInteger-total {
			return 0, ErrInvalidInput
		}
		total += n
	}
	return total, nil
}

// A nil balance denotes an unlimited business quota, never an unknown ledger.
func storageAvailableBytes(sp *ent.StorageSpace) (*int64, error) {
	total, err := storageLedgerTotal(sp)
	if err != nil || !validStorageQuota(sp.CapacityBytes) {
		return nil, ErrInvalidInput
	}
	if sp.CapacityBytes == nil {
		return nil, nil
	}
	remaining := *sp.CapacityBytes - total
	if remaining < 0 {
		remaining = 0
	}
	return &remaining, nil
}

func storageQuotaAdmission(sp *ent.StorageSpace, size int64) error {
	total, err := storageLedgerTotal(sp)
	if err != nil || size < 0 || size > MaxStorageInteger-total {
		return ErrInvalidInput
	}
	available, err := storageAvailableBytes(sp)
	if err != nil {
		return err
	}
	if available != nil && size > *available {
		return storage.ErrLimit
	}
	return nil
}

// EnsureStoragePolicy writes initialization choices exactly once. Existing policy
// always wins over deployment inputs, including when the execution mode changes.
func (s *StorageService) EnsureStoragePolicy(ctx context.Context, local bool, capacitySet bool, capacity *int64, logicalSet bool, logical *int64) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		return ensureStoragePolicy(ctx, tx, local, capacitySet, capacity, logicalSet, logical)
	})
}

func ensureStoragePolicy(ctx context.Context, tx *ent.Client, local, capacitySet bool, capacity *int64, logicalSet bool, logical *int64) error {
	row, err := tx.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	fields := map[string]json.RawMessage{}
	if row != nil {
		if err = json.Unmarshal([]byte(row.Value), &fields); err != nil {
			return err
		}
	}
	_, hasCapacity := fields["default_space_capacity_bytes"]
	_, hasLogical := fields["logical_limit_bytes"]
	if !local && ((!hasCapacity && !capacitySet) || (!hasLogical && !logicalSet)) {
		return fmt.Errorf("%w: explicitly configure missing storage initialization quotas for serve", ErrStoragePolicy)
	}
	if !capacitySet {
		capacity = nil
	}
	if !logicalSet {
		logical = nil
	}
	if !hasCapacity {
		if !validStorageQuota(capacity) {
			return ErrInvalidInput
		}
		fields["default_space_capacity_bytes"], err = json.Marshal(capacity)
		if err != nil {
			return err
		}
	}
	if !hasLogical {
		if !validStorageQuota(logical) {
			return ErrInvalidInput
		}
		fields["logical_limit_bytes"], err = json.Marshal(logical)
		if err != nil {
			return err
		}
	}
	if row == nil {
		fields["mode"] = json.RawMessage(`"site_only"`)
		fields["default_choice"] = json.RawMessage(`"site"`)
		fields["generation"] = json.RawMessage(`0`)
	}
	if row == nil || !hasCapacity || !hasLogical {
		data, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if row == nil {
			err = tx.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(string(data)).Exec(ctx)
		} else {
			var n int
			n, err = tx.SystemSetting.Update().Where(systemsetting.IDEQ(row.ID), systemsetting.ValueEQ(row.Value)).SetValue(string(data)).Save(ctx)
			if err == nil && n != 1 {
				return ErrStorageConflict
			}
		}
		if err != nil {
			return err
		}
	}
	_, err = storagePolicy(ctx, tx)
	return err
}

func (s *StorageConnectionService) SetSpaceQuota(ctx context.Context, actor, spaceID int, capacity *int64, expectedGeneration int64) (*StorageSpaceRecord, error) {
	if !validStorageQuota(capacity) || expectedGeneration < 0 || expectedGeneration >= MaxStorageInteger {
		return nil, ErrInvalidInput
	}
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		sp, err := tx.StorageSpace.Get(ctx, spaceID)
		if err != nil {
			return err
		}
		if _, err = s.authorized(ctx, tx, actor, sp.ConnectionID); err != nil {
			return err
		}
		if sp.ManagementGeneration != expectedGeneration {
			return ErrStorageConflict
		}
		update := tx.StorageSpace.Update().Where(storagespace.IDEQ(spaceID), storagespace.ManagementGenerationEQ(expectedGeneration)).AddManagementGeneration(1)
		if capacity == nil {
			update.ClearCapacityBytes()
		} else {
			update.SetCapacityBytes(*capacity)
		}
		n, err := update.Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		event := AuditEvent{ActorUserID: actor, Action: "storage.quota.update", ResourceType: "storage_space", ResourceID: spaceID, VisibilityScope: "personal", Message: "Storage space quota updated"}
		if sp.OwnerKind == storagespace.OwnerKindOrg {
			event.OrgID = &sp.OwnerID
			event.VisibilityScope = "organization"
		}
		if sp.OwnerKind == storagespace.OwnerKindSite {
			event.Action = "admin.storage.quota.update"
			event.VisibilityScope = "unknown"
		}
		return recordAuditEvent(ctx, tx, event)
	})
	if err != nil {
		return nil, err
	}
	sp, err := s.client.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	return s.spaceRecord(sp)
}

func storageCanIncrement(n int64) bool { return n >= 0 && n < MaxStorageInteger }
