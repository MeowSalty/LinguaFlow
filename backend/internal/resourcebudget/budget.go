// Package resourcebudget accounts execution roots shared by one Server.
// It accounts logical ownership, not heap size or RSS. Storage and runtime
// payload are independent roots; releasing a worker never releases storage.
package resourcebudget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

var (
	ErrInvalid      = errors.New("invalid resource budget")
	ErrCapacity     = errors.New("resource budget capacity unavailable")
	ErrIdentity     = errors.New("resource budget identity conflict")
	ErrBusy         = errors.New("resource budget must be drained before rebuild")
	ErrActorMissing = errors.New("resource budget actor inventory must be prepared")
)

// CapacityError records consumer evidence at the exact failed atomic probe.
// A later consumer release cannot retroactively turn that failure into no-drain.
type CapacityError struct {
	DrainOutside bool
	cause        error
}

func (e *CapacityError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return ErrCapacity.Error()
}
func (e *CapacityError) Unwrap() error { return ErrCapacity }
func CapacityHasDrain(err error) bool {
	var e *CapacityError
	return errors.As(err, &e) && e.DrainOutside
}

type Scope struct {
	ActorID int
	JobID   int
	CallID  string
}

// Vector contains independent nonnegative roots. A decrease in one dimension
// cannot pay for an increase in another dimension, including during overage.
type Vector struct {
	UnconfirmedMembers  int64
	PayloadBytes        int64
	PrepareOwners       int64
	MainResponseOwners  int64
	AuxResponseOwners   int64
	BuildWorkers        int64
	QueuedDescriptors   int64
	LogicalStorageBytes int64
}

// Limits applies each positive value independently; zero leaves that root unlimited.
type Limits struct {
	MaxUnconfirmedMembers  int64 `yaml:"max_unconfirmed_members"`
	MaxPayloadBytes        int64 `yaml:"max_payload_bytes"`
	MaxPrepareOwners       int64 `yaml:"max_prepare_owners"`
	MaxMainResponseOwners  int64 `yaml:"max_main_response_owners"`
	MaxAuxResponseOwners   int64 `yaml:"max_aux_response_owners"`
	MaxBuildWorkers        int64 `yaml:"max_build_workers"`
	MaxQueuedDescriptors   int64 `yaml:"max_queued_descriptors"`
	MaxLogicalStorageBytes int64 `yaml:"max_logical_storage_bytes"`
}

type Policy struct {
	Actor    Limits `yaml:"actor"`
	Instance Limits `yaml:"instance"`
}

var dimensions = [...]string{"unconfirmed_members", "payload_bytes", "prepare_owners", "main_response_owners", "aux_response_owners", "build_workers", "queued_descriptors", "logical_storage_bytes"}

func (v Vector) values() [8]int64 {
	return [8]int64{v.UnconfirmedMembers, v.PayloadBytes, v.PrepareOwners, v.MainResponseOwners, v.AuxResponseOwners, v.BuildWorkers, v.QueuedDescriptors, v.LogicalStorageBytes}
}
func vector(v [8]int64) Vector { return Vector{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]} }
func (l Limits) vector() Vector {
	return Vector{l.MaxUnconfirmedMembers, l.MaxPayloadBytes, l.MaxPrepareOwners, l.MaxMainResponseOwners, l.MaxAuxResponseOwners, l.MaxBuildWorkers, l.MaxQueuedDescriptors, l.MaxLogicalStorageBytes}
}
func (p Policy) Validate() error {
	for _, group := range []struct {
		name   string
		limits Limits
	}{{"actor", p.Actor}, {"instance", p.Instance}} {
		for i, n := range group.limits.vector().values() {
			if n < 0 {
				return fmt.Errorf("%w: %s.max_%s must be nonnegative", ErrInvalid, group.name, dimensions[i])
			}
		}
	}
	return nil
}
func (v Vector) Validate() error {
	for i, n := range v.values() {
		if n < 0 {
			return fmt.Errorf("%w: negative %s", ErrInvalid, dimensions[i])
		}
	}
	return nil
}
func (v Vector) empty() bool { return v == (Vector{}) }

type entry struct {
	scope Scope
	usage Vector
}
type balance struct{ total, unattached Vector }

type RootBudget struct {
	mu         sync.Mutex
	policy     Policy
	instance   balance
	orphan     Vector
	actors     map[int]balance
	active     map[string]entry
	changed    chan struct{}
	rebuilding bool
	rebuilt    bool
	preparing  map[int]bool
	pins       map[int]int64
	pinCount   int64
}

func New(p Policy) (*RootBudget, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &RootBudget{policy: p, actors: make(map[int]balance), active: make(map[string]entry), preparing: make(map[int]bool), pins: make(map[int]int64), changed: make(chan struct{})}, nil
}

// PinActor protects a prepared actor between inventory loading and its first
// claim. Each token has exactly one idempotent release; callers cannot release
// another caller's pin. Positive descriptor caps also limit these counts.
func (b *RootBudget) PinActor(actorID int) (func(), error) {
	b.mu.Lock()
	if actorID <= 0 {
		b.mu.Unlock()
		return nil, ErrInvalid
	}
	if b.rebuilding || b.preparing[actorID] {
		b.mu.Unlock()
		return nil, ErrBusy
	}
	if _, ok := b.actors[actorID]; !ok {
		b.mu.Unlock()
		return nil, ErrActorMissing
	}
	if cap := b.policy.Instance.MaxQueuedDescriptors; cap > 0 && b.pinCount >= cap {
		b.mu.Unlock()
		return nil, ErrCapacity
	}
	if cap := b.policy.Actor.MaxQueuedDescriptors; cap > 0 && b.pins[actorID] >= cap {
		b.mu.Unlock()
		return nil, ErrCapacity
	}
	if b.pinCount == math.MaxInt64 || b.pins[actorID] == math.MaxInt64 {
		b.mu.Unlock()
		return nil, fmt.Errorf("%w: actor pin count overflow", ErrInvalid)
	}
	b.pinCount++
	b.pins[actorID]++
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.pinCount--
			b.pins[actorID]--
			if b.pins[actorID] == 0 {
				delete(b.pins, actorID)
			}
			b.notifyLocked()
			b.mu.Unlock()
		})
	}, nil
}

// CanDrainOutside reports an observed consumer in another execution scope.
// Cold payload/storage inventory and queued descriptors do not establish a
// future release. A positive instance descriptor cap bounds this scan.
func (r *RootBudget) CanDrainOutside(scope Scope) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canDrainOutsideLocked(scope)
}
func (r *RootBudget) canDrainOutsideLocked(scope Scope) bool {
	for _, e := range r.active {
		if scope.JobID > 0 && scope.JobID == e.scope.JobID || scope.JobID == 0 && e.scope.JobID == 0 && scope.CallID == e.scope.CallID {
			continue
		}
		v := e.usage
		if v.PrepareOwners > 0 || v.MainResponseOwners > 0 || v.AuxResponseOwners > 0 || v.BuildWorkers > 0 {
			return true
		}
	}
	return false
}
func (r *RootBudget) capacityLocked(scope Scope, err error) error {
	if errors.Is(err, ErrCapacity) {
		return &CapacityError{DrainOutside: r.canDrainOutsideLocked(scope), cause: err}
	}
	return err
}

func validateIdentity(s Scope, id string, historical bool) error {
	if s.ActorID < 0 || !historical && s.ActorID == 0 || s.JobID < 0 || (s.JobID == 0) == (s.CallID == "") || id == "" {
		return fmt.Errorf("%w: actor and exactly one job/call identity are required", ErrIdentity)
	}
	// Stable IDs are descriptors, not a place to retain request bodies. Together
	// with a positive descriptor count cap this bounds active map key storage.
	if len(id) > 4096 || len(s.CallID) > 4096 {
		return fmt.Errorf("%w: descriptor identity exceeds 4096 bytes", ErrIdentity)
	}
	return nil
}

// change computes independent signed deltas without allowing negative balances
// or arithmetic overflow. Positive limits apply only to positive deltas.
func change(total, old, next Vector, limit *Limits) (Vector, error) {
	if err := old.Validate(); err != nil {
		return Vector{}, err
	}
	if err := next.Validate(); err != nil {
		return Vector{}, err
	}
	t, o, n := total.values(), old.values(), next.values()
	var caps [8]int64
	if limit != nil {
		caps = limit.vector().values()
	}
	for i := range t {
		if n[i] >= o[i] {
			d := n[i] - o[i]
			if d > math.MaxInt64-t[i] {
				return Vector{}, fmt.Errorf("%w: %s overflow", ErrInvalid, dimensions[i])
			}
			t[i] += d
			if limit != nil && caps[i] > 0 && d > 0 && t[i] > caps[i] {
				return Vector{}, fmt.Errorf("%w: %s", ErrCapacity, dimensions[i])
			}
		} else {
			d := o[i] - n[i]
			if d > t[i] {
				return Vector{}, fmt.Errorf("%w: %s underflow", ErrIdentity, dimensions[i])
			}
			t[i] -= d
		}
	}
	return vector(t), nil
}

func (b *RootBudget) notifyLocked() { close(b.changed); b.changed = make(chan struct{}) }

func (b *RootBudget) actorCacheFullLocked() bool {
	n, limit := int64(len(b.actors)), b.policy.Instance.MaxQueuedDescriptors
	return limit > 0 && (n >= limit || int64(len(b.preparing)) >= limit-n)
}

// Claim funds a new active identity. Repeating the same claim is idempotent;
// changing an existing identity requires Transfer.
func (b *RootBudget) Claim(s Scope, id string, v Vector) error {
	if err := validateIdentity(s, id, false); err != nil {
		return err
	}
	if err := v.Validate(); err != nil {
		return err
	}
	if v.empty() {
		return fmt.Errorf("%w: empty claim", ErrInvalid)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	if b.preparing[s.ActorID] {
		return ErrBusy
	}
	if e, ok := b.active[id]; ok {
		if e.scope == s && e.usage == v {
			return nil
		}
		return ErrIdentity
	}
	if cap := b.policy.Instance.MaxQueuedDescriptors; cap > 0 && int64(len(b.active)) >= cap {
		return b.capacityLocked(s, ErrCapacity)
	}
	return b.transferLocked(s, id, Vector{}, v)
}

func (b *RootBudget) transferLocked(s Scope, id string, old, next Vector) error {
	a, ok := b.actors[s.ActorID]
	if !ok && b.rebuilt {
		return ErrActorMissing
	}
	if !ok && b.actorCacheFullLocked() {
		return b.capacityLocked(s, ErrCapacity)
	}
	at, err := change(a.total, old, next, &b.policy.Actor)
	if err != nil {
		return b.capacityLocked(s, fmt.Errorf("actor %d: %w", s.ActorID, err))
	}
	it, err := change(b.instance.total, old, next, &b.policy.Instance)
	if err != nil {
		return b.capacityLocked(s, fmt.Errorf("instance: %w", err))
	}
	a.total, b.instance.total = at, it
	b.actors[s.ActorID] = a
	if next.empty() {
		delete(b.active, id)
	} else {
		b.active[id] = entry{s, next}
	}
	b.notifyLocked()
	return nil
}

// Transfer atomically replaces one identity's complete root ownership.
// Actor attribution is immutable while that identity is attached.
func (b *RootBudget) Transfer(s Scope, id string, next Vector) error {
	if err := next.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	e, ok := b.active[id]
	if !ok || e.scope != s {
		return ErrIdentity
	}
	return b.transferLocked(s, id, e.usage, next)
}

// TransferRuntime replaces runtime roots while adjusting storage reservation
// relative to its current value under the same lock. Durable consumers may
// concurrently Move storage to their own identity without the runtime writer
// reviving an old reservation read before that handoff.
func (b *RootBudget) TransferRuntime(s Scope, id string, next Vector, storageDelta int64) error {
	if err := next.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	e, ok := b.active[id]
	if !ok || e.scope != s {
		return ErrIdentity
	}
	current := e.usage.LogicalStorageBytes
	if storageDelta > 0 && storageDelta > math.MaxInt64-current {
		return ErrInvalid
	}
	if storageDelta < -current {
		return ErrIdentity
	}
	next.LogicalStorageBytes = current + storageDelta
	return b.transferLocked(s, id, e.usage, next)
}

// Release subtracts exactly the supplied roots. Pass runtime roots only when
// unloading a paused/failed invocation; its durable storage remains charged.
func (b *RootBudget) Release(s Scope, id string, amount Vector) error {
	if err := amount.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	e, ok := b.active[id]
	if !ok || e.scope != s {
		return ErrIdentity
	}
	next, err := change(e.usage, amount, Vector{}, nil)
	if err != nil {
		return err
	}
	return b.transferLocked(s, id, e.usage, next)
}

// Attach adopts a projected durable identity already included by Rebuild.
// The caller is responsible for validating the identity against its store.
// Unattached scalar balances prevent invented credit or duplicate adoption.
func (b *RootBudget) Attach(s Scope, id string, v Vector) error {
	if err := validateIdentity(s, id, false); err != nil {
		return err
	}
	if err := validateStored(v); err != nil {
		return err
	}
	if v.empty() {
		return ErrInvalid
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	if e, ok := b.active[id]; ok {
		if e.scope == s && e.usage == v {
			return nil
		}
		return ErrIdentity
	}
	if cap := b.policy.Instance.MaxQueuedDescriptors; cap > 0 && int64(len(b.active)) >= cap {
		return ErrCapacity
	}
	a, ready := b.actors[s.ActorID]
	if !ready {
		return ErrActorMissing
	}
	au, err := change(a.unattached, v, Vector{}, nil)
	if err != nil {
		return err
	}
	iu, err := change(b.instance.unattached, v, Vector{}, nil)
	if err != nil {
		return err
	}
	a.unattached, b.instance.unattached = au, iu
	b.actors[s.ActorID], b.active[id] = a, entry{s, v}
	b.notifyLocked()
	return nil
}

// Detach discards the active descriptor but preserves every remaining durable
// root in the scalar inventory. Runtime workers must have been released first.
func (b *RootBudget) Detach(s Scope, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	e, ok := b.active[id]
	if !ok || e.scope != s {
		return ErrIdentity
	}
	if err := validateStored(e.usage); err != nil {
		return err
	}
	a := b.actors[s.ActorID]
	au, err := change(a.unattached, Vector{}, e.usage, nil)
	if err != nil {
		return err
	}
	iu, err := change(b.instance.unattached, Vector{}, e.usage, nil)
	if err != nil {
		return err
	}
	a.unattached, b.instance.unattached = au, iu
	b.actors[s.ActorID] = a
	delete(b.active, id)
	b.notifyLocked()
	return nil
}

type Snapshot struct {
	Policy           Policy
	Actor, Instance  Vector
	ActiveIdentities int
	Rebuilding       bool
	Changed          <-chan struct{}
}

func (b *RootBudget) Snapshot(actorID int) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Snapshot{b.policy, b.actors[actorID].total, b.instance.total, len(b.active), b.rebuilding, b.changed}
}

// Usage reads attached ownership only. Inactive durable identities must be
// projected from the store and adopted with Attach, never guessed from totals.
func (b *RootBudget) Usage(s Scope, id string) (Vector, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.active[id]
	if !ok || e.scope != s {
		return Vector{}, false
	}
	return e.usage, true
}

// Changed is an event hint only. A wakeup always requires a fresh Claim or
// Transfer; it does not authorize work using an earlier admission decision.
func (b *RootBudget) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.changed
}

// Move transfers ownership between two identities atomically without changing
// any actor or instance root. It cannot borrow across dimensions or actors.
func (b *RootBudget) Move(s Scope, from, to string, amount Vector) error {
	if err := validateIdentity(s, to, false); err != nil {
		return err
	}
	if err := amount.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding {
		return ErrBusy
	}
	f, ok := b.active[from]
	if !ok || f.scope != s {
		return ErrIdentity
	}
	if from == to {
		return nil
	}
	t, ok := b.active[to]
	if ok && t.scope != s {
		return ErrIdentity
	}
	left, err := change(f.usage, amount, Vector{}, nil)
	if err != nil {
		return err
	}
	if cap := b.policy.Instance.MaxQueuedDescriptors; cap > 0 && !ok && !left.empty() && !amount.empty() && int64(len(b.active)) >= cap {
		return ErrCapacity
	}
	right, err := change(t.usage, Vector{}, amount, nil)
	if err != nil {
		return err
	}
	if left.empty() {
		delete(b.active, from)
	} else {
		b.active[from] = entry{s, left}
	}
	if !right.empty() {
		b.active[to] = entry{s, right}
	}
	b.notifyLocked()
	return nil
}

// Record is a validated header projection, never a body. ActorID zero is
// accepted only during rebuild so unavailable actors cannot hide server stock.
type Record struct {
	Scope    Scope
	Identity string
	Usage    Vector
}
type Source interface {
	Scan(context.Context, func(Record) error) error
}
type SourceFunc func(context.Context, func(Record) error) error

func (f SourceFunc) Scan(ctx context.Context, emit func(Record) error) error { return f(ctx, emit) }

func validateStored(v Vector) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if v.PrepareOwners != 0 || v.MainResponseOwners != 0 || v.AuxResponseOwners != 0 || v.BuildWorkers != 0 || v.QueuedDescriptors != 0 {
		return fmt.Errorf("%w: live workers cannot be reconstructed from storage", ErrInvalid)
	}
	return nil
}

// Rebuild replaces only drained inventory. It records stock above current
// limits without increasing those limits, allowing same-identity reductions.
// Scan must emit each stable stored identity once, in bounded pages from a
// writer-quiescent store. No map of historical request identities is retained.
func (b *RootBudget) Rebuild(ctx context.Context, src Source) error {
	if src == nil {
		return ErrInvalid
	}
	b.mu.Lock()
	if b.rebuilding || len(b.active) != 0 || len(b.preparing) != 0 || b.pinCount != 0 {
		b.mu.Unlock()
		return ErrBusy
	}
	b.rebuilding = true
	b.notifyLocked()
	b.mu.Unlock()
	var instance, orphan Vector
	err := src.Scan(ctx, func(r Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateIdentity(r.Scope, r.Identity, true); err != nil {
			return err
		}
		if err := validateStored(r.Usage); err != nil {
			return err
		}
		it, err := change(instance, Vector{}, r.Usage, nil)
		if err != nil {
			return err
		}
		if r.Scope.ActorID == 0 {
			next, err := change(orphan, Vector{}, r.Usage, nil)
			if err != nil {
				return err
			}
			orphan = next
		}
		instance = it
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rebuilding = false
	if err == nil {
		b.actors, b.instance, b.rebuilt = make(map[int]balance), balance{instance, instance}, true
		b.orphan = orphan
	}
	b.notifyLocked()
	return err
}

// RebuildActor prepares one actor outside dispatch locks using an actor-filtered
// projected source. Already prepared actors are left intact. A positive instance
// descriptor limit bounds the cache. ForgetActor evicts inactive entries;
// historical actor totals are never retained by Rebuild.
func (b *RootBudget) RebuildActor(ctx context.Context, actorID int, src Source) error {
	if actorID <= 0 || src == nil {
		return ErrInvalid
	}
	b.mu.Lock()
	if b.rebuilding || b.preparing[actorID] {
		b.mu.Unlock()
		return ErrBusy
	}
	if _, ok := b.actors[actorID]; ok {
		b.mu.Unlock()
		return nil
	}
	if b.actorCacheFullLocked() {
		b.mu.Unlock()
		return ErrCapacity
	}
	b.preparing[actorID] = true
	b.mu.Unlock()
	var total Vector
	err := src.Scan(ctx, func(r Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.Scope.ActorID != actorID {
			return ErrIdentity
		}
		if err := validateIdentity(r.Scope, r.Identity, false); err != nil {
			return err
		}
		if err := validateStored(r.Usage); err != nil {
			return err
		}
		next, err := change(total, Vector{}, r.Usage, nil)
		if err != nil {
			return err
		}
		total = next
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.preparing, actorID)
	if err == nil {
		// A source inconsistent with the instance rebuild cannot create credit.
		if _, checkErr := change(b.instance.total, total, Vector{}, nil); checkErr != nil {
			err = checkErr
		} else {
			b.actors[actorID] = balance{total, total}
		}
	}
	b.notifyLocked()
	return err
}

// ForgetActor evicts only inactive scalar metadata. Its persisted ownership
// remains in the instance total and is reloaded before that actor executes.
func (b *RootBudget) ForgetActor(actorID int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding || b.preparing[actorID] || b.pins[actorID] > 0 {
		return ErrBusy
	}
	for _, e := range b.active {
		if e.scope.ActorID == actorID {
			return ErrBusy
		}
	}
	delete(b.actors, actorID)
	b.notifyLocked()
	return nil
}

// CachedActorIDs includes only the live cache, never historical actors.
// A positive instance descriptor limit bounds its size.
// Inventory callers hold their database barrier while projecting these IDs.
func (b *RootBudget) CachedActorIDs() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]int, 0, len(b.actors))
	for id := range b.actors {
		ids = append(ids, id)
	}
	return ids
}

// ReleaseInventoryTotals retires a transaction-verified bulk inventory delta
// without loading every historical actor. Absent actor cache entries are left
// absent; later RebuildActor sees the committed database. Attached ownership
// cannot be refunded. All scalar updates succeed or fail together.
func (b *RootBudget) ReleaseInventoryTotals(total, orphan Vector, actors map[int]Vector) error {
	if err := validateStored(total); err != nil {
		return err
	}
	if err := validateStored(orphan); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding || len(b.preparing) > 0 {
		return ErrBusy
	}
	if cap := b.policy.Instance.MaxQueuedDescriptors; cap > 0 && int64(len(actors)) > cap {
		return ErrCapacity
	}
	iu, err := change(b.instance.unattached, total, Vector{}, nil)
	if err != nil {
		return err
	}
	it, err := change(b.instance.total, total, Vector{}, nil)
	if err != nil {
		return err
	}
	on, err := change(b.orphan, orphan, Vector{}, nil)
	if err != nil {
		return err
	}
	sum := orphan
	next := make(map[int]balance, len(actors))
	for id, delta := range actors {
		if id <= 0 {
			return ErrInvalid
		}
		if err := validateStored(delta); err != nil {
			return err
		}
		sum, err = change(sum, Vector{}, delta, nil)
		if err != nil {
			return err
		}
		a, ok := b.actors[id]
		if !ok {
			continue
		}
		a.unattached, err = change(a.unattached, delta, Vector{}, nil)
		if err != nil {
			return err
		}
		a.total, err = change(a.total, delta, Vector{}, nil)
		if err != nil {
			return err
		}
		next[id] = a
	}
	if _, err = change(total, sum, Vector{}, nil); err != nil {
		return err
	}
	b.instance = balance{it, iu}
	b.orphan = on
	for id, a := range next {
		b.actors[id] = a
	}
	b.notifyLocked()
	return nil
}

// ReleaseInventory retires inactive durable stock only after its transaction
// has been verified by the caller. It consumes unattached balances, so it
// cannot release credit still owned by an active runtime identity. Actor zero
// is reserved for cleanup of unavailable-actor history and never for execution.
func (b *RootBudget) ReleaseInventory(s Scope, amount Vector) error {
	if err := validateIdentity(s, "inventory", true); err != nil {
		return err
	}
	if err := validateStored(amount); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rebuilding || b.preparing[s.ActorID] {
		return ErrBusy
	}
	iu, err := change(b.instance.unattached, amount, Vector{}, nil)
	if err != nil {
		return err
	}
	it, err := change(b.instance.total, amount, Vector{}, nil)
	if err != nil {
		return err
	}
	if s.ActorID == 0 {
		o, err := change(b.orphan, amount, Vector{}, nil)
		if err != nil {
			return err
		}
		b.orphan = o
	} else {
		a, ok := b.actors[s.ActorID]
		if !ok {
			return ErrActorMissing
		}
		au, err := change(a.unattached, amount, Vector{}, nil)
		if err != nil {
			return err
		}
		at, err := change(a.total, amount, Vector{}, nil)
		if err != nil {
			return err
		}
		b.actors[s.ActorID] = balance{at, au}
	}
	b.instance = balance{it, iu}
	b.notifyLocked()
	return nil
}
