package workstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type storageBudgetKey struct{}

// StorageBudget owns only the active invocation's storage attachment. Its
// scalar backing remains charged when that invocation unloads.
type StorageBudget struct {
	mu            sync.Mutex
	Root          *resourcebudget.RootBudget
	Source        InventorySource
	Scope         resourcebudget.Scope
	InventoryLock sync.Locker
	Credit        interface {
		StorageCredit(string) (int64, bool)
		ConsumeStorage(string, int64) error
		RefundStorage(string, int64) error
	}
	before  int64
	moved   map[string]int64
	touched map[string]struct{}
}

func WithStorageBudget(ctx context.Context, b *StorageBudget) context.Context {
	return context.WithValue(ctx, storageBudgetKey{}, b)
}

// InstallStorageHooks is called once during Server construction. Ent shares
// hook registries with transactional clients, so hooks must never be appended
// per transaction.
func InstallStorageHooks(client *ent.Client) {
	h := func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			b, _ := ctx.Value(storageBudgetKey{}).(*StorageBudget)
			if b == nil {
				return next.Mutate(ctx, m)
			}
			var tx *ent.Client
			switch m := m.(type) {
			case *ent.WorkRequestMutation:
				tx = m.Client()
			case *ent.WorkCandidateMutation:
				tx = m.Client()
			default:
				return next.Mutate(ctx, m)
			}
			return b.hook(tx)(next).Mutate(ctx, m)
		})
	}
	client.WorkRequest.Use(h)
	client.WorkCandidate.Use(h)
}
func (b *StorageBudget) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.Root.Usage(b.Scope, StorageIdentity(b.Scope.JobID)); ok {
		return b.Root.Detach(b.Scope, StorageIdentity(b.Scope.JobID))
	}
	return nil
}

func (b *StorageBudget) begin(ctx context.Context) error {
	if b.InventoryLock != nil {
		b.InventoryLock.Lock()
	}
	b.mu.Lock()
	b.moved = make(map[string]int64)
	b.touched = make(map[string]struct{})

	if usage, ok := b.Root.Usage(b.Scope, StorageIdentity(b.Scope.JobID)); ok {
		b.before = usage.LogicalStorageBytes
		return nil
	}
	_, n, err := b.Source.Job(ctx, b.Scope.JobID)
	b.before = n
	if err == nil && n > 0 {
		err = b.Root.Attach(b.Scope, StorageIdentity(b.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: n})
	}
	if err != nil {
		b.mu.Unlock()
		if b.InventoryLock != nil {
			b.InventoryLock.Unlock()
		}
	}
	return err
}

var storageFields = map[string]bool{"freeze_payload": true, "response_payload": true, "corrected_targets": true, "corrected_target_index": true, "glossary_receipt": true, "promotion_members": true, "promotion_status": true, "members": true, "debits": true, "segment_ids": true, "payload": true, "pipeline_header": true, "baseline_target": true}

func storageSize(v any) (int64, error) {
	switch v := v.(type) {
	case []byte:
		return int64(len(v)), nil
	case json.RawMessage:
		return int64(len(v)), nil
	case string:
		return int64(len(v)), nil
	default:
		data, err := json.Marshal(v)
		return int64(len(data)), err
	}
}

func (b *StorageBudget) fieldSize(ctx context.Context, client *ent.Client, field string, v any) (int64, error) {
	if b.Source.Driver != "postgres" {
		return storageSize(v)
	}
	// PostgreSQL stores JSONB in normalized form. A compact encoder length
	// cannot reserve its actual UTF-8 text projection (notably punctuation).
	switch field {
	case "glossary_receipt", "promotion_members", "members", "debits", "segment_ids", "payload":
		data, err := json.Marshal(v)
		if err != nil {
			return 0, err
		}
		rows, err := client.QueryContext(ctx, "SELECT OCTET_LENGTH(CAST(CAST($1 AS JSONB) AS TEXT))", string(data))
		if err != nil {
			return 0, err
		}
		var n int64
		err = inventoryRow{rows: rows}.Scan(&n)
		return n, err
	default:
		return storageSize(v)
	}
}

// hook consumes pre-dispatch maximum save credit before a SQL write. It never
// waits for quota after a provider response has returned.
func (b *StorageBudget) hook(client *ent.Client) ent.Hook {
	return func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			var table string
			switch m.(type) {
			case *ent.WorkRequestMutation:
				table = "work_requests"
			case *ent.WorkCandidateMutation:
				table = "work_candidates"
			default:
				return next.Mutate(ctx, m)
			}
			var size, old int64
			requestID := persistence.MetadataFromContext(ctx).RequestID
			if requestID == "" {
				if id, ok := m.Field("identity"); ok && table == "work_requests" {
					requestID, _ = id.(string)
				}
				if id, ok := m.Field("parent_request_id"); ok && table == "work_candidates" {
					requestID, _ = id.(string)
				}
			}
			var fields []string
			for _, field := range m.Fields() {
				if !storageFields[field] {
					continue
				}
				v, ok := m.Field(field)
				if !ok {
					continue
				}
				n, err := b.fieldSize(ctx, client, field, v)
				if err != nil {
					return nil, err
				}
				if err = addInventory(&size, n); err != nil {
					return nil, err
				}
				fields = append(fields, b.Source.length(field))
			}
			if !m.Op().Is(ent.OpCreate) {
				if id, ok := m.(interface{ ID() (int, bool) }); ok {
					if value, yes := id.ID(); yes {
						identity := "identity"
						if table == "work_candidates" {
							identity = "parent_request_id"
						}
						lengths := joinLengths(fields)
						if lengths == "" {
							lengths = "0"
						}
						query := "SELECT " + lengths + "," + identity + " FROM " + table + " WHERE id=?"
						rows, err := client.QueryContext(ctx, b.Source.bind(query), value)
						if err != nil {
							return nil, err
						}
						if rows.Next() {
							var stableID string
							err = rows.Scan(&old, &stableID)
							if requestID == "" {
								requestID = stableID
							}
						}
						err = errors.Join(err, rows.Err(), rows.Close())
						if err != nil {
							return nil, err
						}
					}
				}
			}
			if requestID != "" && table == "work_requests" {
				b.touched[requestID] = struct{}{}
				if table == "work_requests" {
					if metadata, ok := b.Credit.(interface{ MarkRequestMetadataUncertain(string) }); ok {
						metadata.MarkRequestMetadataUncertain(requestID)
					}
				}
			}
			if size > old {
				amount := resourcebudget.Vector{LogicalStorageBytes: size - old}
				runtime, live := b.Root.Usage(b.Scope, RuntimeIdentity(b.Scope.JobID))
				creditAvailable := runtime.LogicalStorageBytes
				if b.Credit != nil {
					creditAvailable, live = b.Credit.StorageCredit(requestID)
				}
				if live && creditAvailable >= amount.LogicalStorageBytes {
					var err error
					if b.Credit != nil {
						err = b.Credit.ConsumeStorage(requestID, amount.LogicalStorageBytes)
					} else {
						err = b.Root.Move(b.Scope, RuntimeIdentity(b.Scope.JobID), StorageIdentity(b.Scope.JobID), amount)
					}
					if err != nil {
						return nil, err
					}
					if err = addInventoryValue(b.moved, requestID, amount.LogicalStorageBytes); err != nil {
						return nil, err
					}
				} else {
					// Frozen request metadata is written before dispatch. Reliable
					// response/candidate bodies must consume the dispatch reservation.
					if _, response := m.Field("response_payload"); response {
						return nil, fmt.Errorf("%w: missing pre-dispatch save credit", resourcebudget.ErrCapacity)
					}
					if _, candidate := m.Field("payload"); candidate {
						return nil, fmt.Errorf("%w: missing candidate save credit", resourcebudget.ErrCapacity)
					}
					owned, ok := b.Root.Usage(b.Scope, StorageIdentity(b.Scope.JobID))
					if err := addInventory(&owned.LogicalStorageBytes, amount.LogicalStorageBytes); err != nil {
						return nil, err
					}
					var err error
					if ok {
						err = b.Root.Transfer(b.Scope, StorageIdentity(b.Scope.JobID), owned)
					} else {
						err = b.Root.Claim(b.Scope, StorageIdentity(b.Scope.JobID), owned)
					}
					if err != nil {
						return nil, err
					}
				}
			}
			return next.Mutate(ctx, m)
		})
	}
}

func joinLengths(fields []string) string {
	q := ""
	for _, f := range fields {
		if q != "" {
			q += "+"
		}
		q += f
	}
	return q
}

// finish uses database facts after ACK or ACK loss. Query failure leaves every
// reservation charged; no guessed rollback can release reliable backing.
func (b *StorageBudget) finish(ctx context.Context, transactionErr error) error {
	defer b.mu.Unlock()
	if b.InventoryLock != nil {
		defer b.InventoryLock.Unlock()
	}
	_, actual, err := b.Source.Job(ctx, b.Scope.JobID)
	if err != nil {
		return err
	}
	if metadata, ok := b.Credit.(interface {
		SetRequestMetadataBytes(string, int, int64) error
	}); ok {
		for id := range b.touched {
			members, n, err := b.Source.RequestMetadataFacts(ctx, id)
			if errors.Is(err, sql.ErrNoRows) {
				members, n, err = 0, 0, nil
			}
			if err != nil {
				return err
			}
			if err = metadata.SetRequestMetadataBytes(id, members, n); err != nil {
				return err
			}
		}
	}
	owned, ok := b.Root.Usage(b.Scope, StorageIdentity(b.Scope.JobID))
	if !ok {
		if actual == 0 {
			return nil
		}
		return resourcebudget.ErrIdentity
	}
	if actual > owned.LogicalStorageBytes {
		return fmt.Errorf("%w: unreserved storage write", resourcebudget.ErrIdentity)
	}
	if extra := owned.LogicalStorageBytes - actual; extra > 0 {
		if transactionErr != nil && (!unknownCommit(transactionErr) || actual == b.before) {
			for id, moved := range b.moved {
				refund := min(extra, moved)
				if refund == 0 {
					continue
				}
				var err error
				if b.Credit != nil {
					err = b.Credit.RefundStorage(id, refund)
				} else {
					err = b.Root.Move(b.Scope, StorageIdentity(b.Scope.JobID), RuntimeIdentity(b.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: refund})
				}
				if err != nil {
					return err
				}
				extra -= refund
			}
		}
		if extra > 0 {
			return b.Root.Release(b.Scope, StorageIdentity(b.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: extra})
		}
	}
	return nil
}

func addInventoryValue(values map[string]int64, id string, n int64) error {
	value := values[id]
	if err := addInventory(&value, n); err != nil {
		return err
	}
	values[id] = value
	return nil
}
