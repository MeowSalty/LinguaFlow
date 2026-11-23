// Package storageguard coordinates lifecycle gates shared by file services and
// backup capture. Database gates protect commits; Coordinator protects actual
// requests in the supported single execution instance.
package storageguard

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

var (
	ErrRetiring = errors.New("storage_retiring")
	ErrRemoved  = errors.New("storage_removed")
	ErrConflict = errors.New("storage_retirement_conflict")
	ErrBusy     = errors.New("storage_operation_in_progress")
)

type Mode int

const (
	Read Mode = iota
	NewUse
	Cleanup
)

type Identity struct{ Connection, Space int64 }

// Check performs no writes. A retirement permit is limited to the exact source
// space while that parent is finalizing, and is never valid after removal.
func Check(ctx context.Context, c *ent.Client, spaceID int, mode Mode, retirementID int) (*ent.StorageConnection, *ent.StorageSpace, error) {
	sp, err := c.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return nil, nil, err
	}
	conn, err := c.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return nil, nil, err
	}
	if err := checkLoaded(ctx, c, conn, sp, mode, retirementID); err != nil {
		return nil, nil, err
	}
	return conn, sp, nil
}

func checkLoaded(ctx context.Context, c *ent.Client, conn *ent.StorageConnection, sp *ent.StorageSpace, mode Mode, retirementID int) error {
	if conn.Lifecycle == storageconnection.LifecycleRemoved || sp.Lifecycle == storagespace.LifecycleRemoved {
		return ErrRemoved
	}
	if conn.Status != storageconnection.StatusEnabled || sp.Status == storagespace.StatusDisabled {
		return storage.ErrPermission
	}
	if mode == NewUse && (conn.Lifecycle != storageconnection.LifecycleAvailable || sp.Lifecycle != storagespace.LifecycleAvailable) {
		return ErrRetiring
	}
	parent := sp.RetirementID
	if parent == nil {
		parent = conn.RetirementID
	}
	if parent != nil {
		r, e := c.StorageRetirement.Get(ctx, *parent)
		if e != nil {
			return e
		}
		if r.Phase == "waiting_configuration" || r.Phase == "completed" {
			return ErrRetiring
		}
		if r.Phase == "finalizing" && (mode != Cleanup || retirementID != r.ID) {
			return ErrRetiring
		}
		if retirementID != 0 && retirementID != r.ID {
			return ErrConflict
		}
	} else if retirementID != 0 {
		return ErrConflict
	}
	return nil
}

// LockConnection must run inside the caller's transaction before inspecting
// children. SQL no-op updates avoid changing timestamps or business generations.
func LockConnection(ctx context.Context, c *ent.Client, id int) error {
	r, e := c.ExecContext(ctx, fmt.Sprintf("UPDATE storage_connections SET management_generation=management_generation WHERE id=%d", id))
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// Gate participates in the transaction that creates a reference or publishes
// bytes. Callers with several spaces acquire connections/spaces in ID order.
func Gate(ctx context.Context, c *ent.Client, spaceID int, mode Mode) error {
	sp, e := c.StorageSpace.Get(ctx, spaceID)
	if e != nil {
		return e
	}
	if e = LockConnection(ctx, c, sp.ConnectionID); e != nil {
		return e
	}
	_, e = c.ExecContext(ctx, fmt.Sprintf("UPDATE storage_spaces SET management_generation=management_generation WHERE id=%d", spaceID))
	if e != nil {
		return e
	}
	_, _, e = Check(ctx, c, spaceID, mode, 0)
	return e
}

// GatedSpace contains facts reloaded after the parent and child locks.
type GatedSpace struct {
	Connection *ent.StorageConnection
	Space      *ent.StorageSpace
}

// GateMany participates in the caller's transaction. It locks each unique
// connection, then each unique space, in ascending ID order. Discovery before
// locking is advisory; a changed parent is rejected after the locks. Queries
// use fixed ID pages, and no-op lock writes preserve business generations.
func GateMany(ctx context.Context, c *ent.Client, spaceIDs []int, mode Mode) (map[int]GatedSpace, error) {
	const pageSize = 256
	unique := make(map[int]bool, len(spaceIDs))
	for _, id := range spaceIDs {
		if id < 1 {
			return nil, ErrConflict
		}
		unique[id] = true
	}
	ids := make([]int, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	discovered := make(map[int]int, len(ids))
	parents := map[int]bool{}
	for start := 0; start < len(ids); start += pageSize {
		page := ids[start:min(start+pageSize, len(ids))]
		rows, err := c.StorageSpace.Query().Where(storagespace.IDIn(page...)).Select(storagespace.FieldID, storagespace.FieldConnectionID).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(page) {
			return nil, ErrConflict
		}
		for _, row := range rows {
			discovered[row.ID] = row.ConnectionID
			parents[row.ConnectionID] = true
		}
	}
	connections := make([]int, 0, len(parents))
	for id := range parents {
		connections = append(connections, id)
	}
	sort.Ints(connections)
	for _, id := range connections {
		if err := LockConnection(ctx, c, id); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		result, err := c.ExecContext(ctx, fmt.Sprintf("UPDATE storage_spaces SET management_generation=management_generation WHERE id=%d", id))
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, ErrConflict
		}
	}
	byConnection := make(map[int]*ent.StorageConnection, len(connections))
	for start := 0; start < len(connections); start += pageSize {
		page := connections[start:min(start+pageSize, len(connections))]
		rows, err := c.StorageConnection.Query().Where(storageconnection.IDIn(page...)).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(page) {
			return nil, ErrConflict
		}
		for _, row := range rows {
			byConnection[row.ID] = row
		}
	}
	result := make(map[int]GatedSpace, len(ids))
	for start := 0; start < len(ids); start += pageSize {
		page := ids[start:min(start+pageSize, len(ids))]
		rows, err := c.StorageSpace.Query().Where(storagespace.IDIn(page...)).All(ctx)
		if err != nil {
			return nil, err
		}
		if len(rows) != len(page) {
			return nil, ErrConflict
		}
		for _, row := range rows {
			if row.ConnectionID != discovered[row.ID] {
				return nil, ErrConflict
			}
			conn := byConnection[row.ConnectionID]
			if err := checkLoaded(ctx, c, conn, row, mode, 0); err != nil {
				return nil, err
			}
			result[row.ID] = GatedSpace{Connection: conn, Space: row}
		}
	}
	return result, nil
}

// LockNamespace serializes prefix allocation even when a bucket has no claims.
// Both SQLite and PostgreSQL support this conflict form. Quoting is deliberately
// local to this one literal; endpoints do not partition the namespace.
func LockNamespace(ctx context.Context, c *ent.Client, bucket string) error {
	if bucket == "" {
		return ErrConflict
	}
	q := "INSERT INTO storage_namespaces (bucket) VALUES ('" + strings.ReplaceAll(bucket, "'", "''") + "') ON CONFLICT (bucket) DO UPDATE SET bucket=excluded.bucket"
	_, e := c.ExecContext(ctx, q)
	return e
}

type Coordinator struct {
	mu     sync.Mutex
	active map[int]int
}

func New() *Coordinator { return &Coordinator{active: map[int]int{}} }

// Exclusive excludes new request admission, but never assumes that existing
// remote calls ended merely because their durable leases expired.
func (g *Coordinator) Exclusive(fn func() error) error { g.mu.Lock(); defer g.mu.Unlock(); return fn() }
func (g *Coordinator) BusyLocked(ids []int) bool {
	for _, id := range ids {
		if g.active[id] > 0 {
			return true
		}
	}
	return false
}
func (g *Coordinator) Busy(ids []int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.BusyLocked(ids)
}

func (g *Coordinator) Acquire(ctx context.Context, c *ent.Client, spaceID int, mode Mode, parent int, expected *Identity) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	conn, sp, e := Check(ctx, c, spaceID, mode, parent)
	if e != nil {
		return nil, e
	}
	if expected != nil && (expected.Connection != conn.ExecutionGeneration || expected.Space != sp.ExecutionGeneration) {
		return nil, ErrRetiring
	}
	if conn.Driver == storageconnection.DriverS3 {
		// Durable evidence precedes even read-only provider calls. Unknown old
		// identities must never be reclassified as having no remote attempts.
		if sp.RemoteUseState != "attempted" {
			if e = c.StorageSpace.UpdateOneID(sp.ID).SetRemoteUseState(storagespace.RemoteUseStateAttempted).Exec(ctx); e != nil {
				return nil, e
			}
		}
		if conn.RemoteUseState != "attempted" {
			if e = c.StorageConnection.UpdateOneID(conn.ID).SetRemoteUseState(storageconnection.RemoteUseStateAttempted).Exec(ctx); e != nil {
				return nil, e
			}
		}
	}
	g.active[spaceID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.active[spaceID]--
			if g.active[spaceID] == 0 {
				delete(g.active, spaceID)
			}
		})
	}, nil
}
