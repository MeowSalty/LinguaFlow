package workstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type storageBudgetKey struct{}
type storageConfirmationKey struct{}

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
	changes map[storageRowKey]storageRowChange
	// A committed write can outlive its fact query. Preserve its exact delta
	// until authority returns; a duplicate ACK must not replace that baseline.
	pending    bool
	pendingErr error
	// An unknown member write keeps only its scalar identity proof. Physical
	// lengths cannot authorize a refund before the canonical result is verified.
	pendingConfirmation func(context.Context) (bool, error)
	closed              bool
}
type storageRowKey struct {
	table string
	id    int
}
type storageRowChange struct{ before int64 }

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
			case *ent.WorkRequestInputMemberMutation:
				tx = m.Client()
			case *ent.WorkResponseMemberMutation:
				tx = m.Client()
			default:
				return next.Mutate(ctx, m)
			}
			return b.hook(tx)(next).Mutate(ctx, m)
		})
	}
	client.WorkRequest.Use(h)
	client.WorkCandidate.Use(h)
	client.WorkRequestInputMember.Use(h)
	client.WorkResponseMember.Use(h)
}
func (b *StorageBudget) Close() error {
	if b.InventoryLock != nil {
		b.InventoryLock.Lock()
		defer b.InventoryLock.Unlock()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if b.pending {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := b.settlePendingLocked(ctx); err != nil {
			return err
		}
	}
	if _, ok := b.Root.Usage(b.Scope, StorageIdentity(b.Scope.JobID)); ok {
		if err := b.Root.Detach(b.Scope, StorageIdentity(b.Scope.JobID)); err != nil {
			return err
		}
	}
	b.closed = true
	b.moved, b.touched, b.changes = nil, nil, nil
	return nil
}

// Settle reconciles a joined writer's outstanding storage facts before its
// admission metadata is retired. It neither starts another write nor detaches
// the durable identity, and follows the same lock order as begin and Close.
func (b *StorageBudget) Settle(ctx context.Context) error {
	if b.InventoryLock != nil {
		b.InventoryLock.Lock()
		defer b.InventoryLock.Unlock()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settlePendingLocked(ctx)
}

func (b *StorageBudget) begin(ctx context.Context) error {
	if b.InventoryLock != nil {
		b.InventoryLock.Lock()
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		if b.InventoryLock != nil {
			b.InventoryLock.Unlock()
		}
		return backend.AnnotateRejection(resourcebudget.ErrIdentity, backend.RejectionEvidence{Branch: "storage_budget_closed", Operation: "storage_begin", StoreKind: b.Source.Driver, ScopeKnown: true, Scope: b.Scope})
	}
	if err := b.settlePendingLocked(ctx); err != nil {
		b.mu.Unlock()
		if b.InventoryLock != nil {
			b.InventoryLock.Unlock()
		}
		return err
	}
	b.moved = make(map[string]int64)
	b.touched = make(map[string]struct{})
	b.changes = make(map[storageRowKey]storageRowChange)
	if b.Source.Columns == nil {
		fields, err := b.Source.requestStorageFields(ctx)
		if err != nil {
			b.mu.Unlock()
			if b.InventoryLock != nil {
				b.InventoryLock.Unlock()
			}
			return err
		}
		b.Source.Columns = make(map[string]bool, len(fields))
		for _, field := range fields {
			b.Source.Columns[field] = true
		}
	}

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

var storageFields = map[string]bool{"freeze_payload": true, "response_payload": true, "corrected_targets": true, "corrected_target_index": true, "glossary_receipt": true, "promotion_members": true, "promotion_status": true, "members": true, "debits": true, "segment_ids": true, "payload": true, "pipeline_header": true, "baseline_target": true, "capture_payload": true, "response_glossary": true, "corrected_payload": true, "prompt_id": true, "work_id": true, "candidate_id": true, "baseline_digest": true, "baseline_status": true, "payload_digest": true, "corrected_digest": true, "failure": true, "candidate_digest": true}

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
			table := mutationStorageTable(m)
			if table == "" {
				return next.Mutate(ctx, m)
			}
			beforeKey, err := b.recordStorageBefore(ctx, client, m)
			if err != nil {
				return nil, err
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
				var n int64
				var err error
				if (table == "work_request_input_members" || table == "work_response_members") && (field == "payload" || field == "corrected_payload") {
					n, err = storageSize(v)
				} else {
					n, err = b.fieldSize(ctx, client, field, v)
				}
				if err != nil {
					return nil, err
				}
				if err = addInventory(&size, n); err != nil {
					return nil, err
				}
				fields = append(fields, b.Source.storageLength(table, field))
			}
			if !m.Op().Is(ent.OpCreate) {
				if id, ok := m.(interface{ ID() (int, bool) }); ok {
					if value, yes := id.ID(); yes {
						identity := "identity"
						if table == "work_candidates" {
							identity = "parent_request_id"
						} else if table == "work_request_input_members" || table == "work_response_members" {
							identity = "(SELECT identity FROM work_requests WHERE id=request_id)"
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
					for _, field := range []string{"capture_payload", "response_glossary", "corrected_payload"} {
						if _, reliable := m.Field(field); reliable {
							return nil, fmt.Errorf("%w: missing pre-dispatch save credit", resourcebudget.ErrCapacity)
						}
					}
					if _, payload := m.Field("payload"); payload && table != "work_request_input_members" {
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
			value, writeErr := next.Mutate(ctx, m)
			if id, ok := mutationRowID(m, value); ok {
				key := storageRowKey{table: table, id: id}
				if _, exists := b.changes[key]; !exists {
					b.changes[key] = storageRowChange{}
				}
			} else {
				_ = beforeKey
			}
			return value, writeErr
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
	b.pending, b.pendingErr = true, transactionErr
	if unknownCommit(transactionErr) {
		b.pendingConfirmation, _ = ctx.Value(storageConfirmationKey{}).(func(context.Context) (bool, error))
	}
	return b.settlePendingLocked(ctx)
}

// settlePendingLocked runs with the inventory and invocation locks held. It
// consumes the original transaction facts before a new attempt can begin.
func (b *StorageBudget) settlePendingLocked(ctx context.Context) error {
	if !b.pending {
		return nil
	}
	if b.pendingConfirmation != nil {
		confirmed, err := b.pendingConfirmation(ctx)
		if err != nil {
			return fmt.Errorf("pending canonical confirmation: %w", err)
		}
		if !confirmed && !storageRollbackKnown(b.pendingErr) {
			b.pendingErr = &NotCommittedError{Cause: b.pendingErr}
		}
	}
	if err := b.settleLocked(ctx, b.pendingErr); err != nil {
		return fmt.Errorf("pending storage settlement: %w", err)
	}
	b.pending, b.pendingErr = false, nil
	b.pendingConfirmation = nil
	return nil
}

func (b *StorageBudget) settleLocked(ctx context.Context, transactionErr error) error {
	var actual int64
	var err error
	if b.pendingConfirmation != nil && unknownCommit(transactionErr) {
		// Another proved writer may have advanced the same durable response
		// while this ACK was unresolved. Reconcile current SQL byte totals;
		// the original touched-row delta cannot describe that later writer.
		_, actual, err = b.Source.Job(ctx, b.Scope.JobID)
	} else {
		actual, err = b.actualChangedStorage(ctx)
	}
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
			// A retired metadata source is consumed exactly once. If a later
			// receipt or Root adjustment fails, retry only the unsettled facts.
			delete(b.touched, id)
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
		if storageRollbackKnown(transactionErr) {
			for id, moved := range b.moved {
				refund := min(extra, moved)
				if refund == 0 {
					continue
				}
				var err error
				if b.Credit != nil {
					if _, live := b.Credit.StorageCredit(id); !live {
						// The source has ended. Proven unused durable charge can
						// be released, but its retired receipt cannot be recreated.
						delete(b.moved, id)
						continue
					}
					err = b.Credit.RefundStorage(id, refund)
				} else {
					err = b.Root.Move(b.Scope, StorageIdentity(b.Scope.JobID), RuntimeIdentity(b.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: refund})
				}
				if err != nil {
					return err
				}
				b.moved[id] -= refund
				extra -= refund
			}
		}
		if extra > 0 {
			return b.Root.Release(b.Scope, StorageIdentity(b.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: extra})
		}
	}
	return nil
}

func storageRollbackKnown(err error) bool {
	// Equal byte totals (or equal row lengths) do not establish non-commit:
	// a completed replacement can retire precisely as many bytes as it writes.
	var absent *NotCommittedError
	if errors.As(err, &absent) {
		return true
	}
	var tx *TransactionError
	return errors.As(err, &tx) && tx.Outcome == "not_committed"
}

func addInventoryValue(values map[string]int64, id string, n int64) error {
	value := values[id]
	if err := addInventory(&value, n); err != nil {
		return err
	}
	values[id] = value
	return nil
}
