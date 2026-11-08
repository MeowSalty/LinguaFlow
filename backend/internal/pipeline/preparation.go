package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// WorkDeferred is scheduler control flow, never a model/storage failure.
type WorkDeferred struct{ Wait backend.AdmissionWait }

func (e *WorkDeferred) Error() string      { return "work deferred: " + e.Wait.Reason }
func (*WorkDeferred) Permanent() bool      { return true }
func workDeferred(err error) *WorkDeferred { var d *WorkDeferred; errors.As(err, &d); return d }
func waitPreparation(ctx context.Context, w backend.AdmissionWait) error {
	var timer *time.Timer
	var at <-chan time.Time
	if !w.AvailableAt.IsZero() {
		timer = time.NewTimer(max(time.Until(w.AvailableAt), time.Nanosecond))
		defer timer.Stop()
		at = timer.C
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.PauseRequested:
		return backend.ErrDispatchPaused
	case <-w.CapacityChanged:
	case <-w.PolicyChanged:
	case <-at:
	}
	return nil
}

type preparationKey struct {
	resource, round, backend int
	stage                    backend.RequestStage
}

func preparationIdentity(i backend.RequestAdmissionIntent) preparationKey {
	return preparationKey{i.ResourceID, i.RoundIndex, i.BackendID, i.Stage}
}

type queuedPreparation struct{ stop func() bool }
type Preparation struct {
	mu                      sync.Mutex
	admission               *backend.RequestAdmission
	capacity                int
	pending                 map[preparationKey]*PreparationTicket
	queue                   []backend.RequestAdmissionIntent
	queued                  map[preparationKey]*queuedPreparation
	changed                 chan struct{}
	closed                  bool
	peakPending, peakQueued int
}
type PreparationTicket struct {
	owner      *Preparation
	key        preparationKey
	once       sync.Once
	competitor *preparationKey
}

func NewPreparation(a *backend.RequestAdmission, capacity int) *Preparation {
	if capacity <= 0 {
		capacity = 16
	}
	return &Preparation{admission: a, capacity: capacity, pending: map[preparationKey]*PreparationTicket{}, queued: map[preparationKey]*queuedPreparation{}, changed: make(chan struct{})}
}
func (p *Preparation) signal() { close(p.changed); p.changed = make(chan struct{}) }
func samePreparationGroup(a, b preparationKey) bool {
	return a.resource == b.resource && a.stage == b.stage
}
func (p *Preparation) blocked(k preparationKey) bool {
	for occupied := range p.pending {
		if samePreparationGroup(k, occupied) {
			return true
		}
	}
	return len(p.pending) >= p.capacity
}
func (p *Preparation) Try(ctx context.Context, i backend.RequestAdmissionIntent, gate backend.DispatchGate) (*PreparationTicket, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, backend.ErrAdmissionClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	k := preparationIdentity(i)
	found := false
	for _, q := range p.queue {
		if preparationIdentity(q) == k {
			found = true
			break
		}
	}
	// A ready competitor must be visible even when cold, unprepared hints filled
	// the descriptor queue. The displaced batch remains in its coordinator.
	if !found && len(p.queue) >= p.capacity && p.admission != nil {
		hint, err := p.admission.ReadyHint(ctx, i, gate)
		if err == nil && hint.Reason == "" {
			for _, q := range p.queue {
				old, oldErr := p.admission.ReadyHint(ctx, q, gate)
				if oldErr != nil || old.Reason != "" {
					p.forgetLocked(preparationIdentity(q))
					break
				}
			}
		}
	}
	if !found && len(p.queue) < p.capacity {
		p.queue = append(p.queue, i)
		entry := &queuedPreparation{}
		p.queued[k] = entry
		entry.stop = context.AfterFunc(ctx, func() {
			p.mu.Lock()
			if p.queued[k] == entry {
				p.forgetLocked(k)
			}
			p.mu.Unlock()
		})
		p.peakQueued = max(p.peakQueued, len(p.queue))
		p.signal()
	}
	w := backend.AdmissionWait{Reason: "prepare_capacity", CapacityChanged: p.changed}
	if gate != nil {
		w.PauseRequested = gate.Done()
	}
	for n, q := range p.queue {
		key := preparationIdentity(q)
		if p.blocked(key) {
			continue
		}
		var hint backend.AdmissionWait
		var err error
		if p.admission != nil {
			hint, err = p.admission.ReadyHint(ctx, q, gate)
		}
		if err != nil {
			if key == k {
				return nil, err
			}
			continue
		}
		if hint.Reason != "" {
			if key == k {
				w = hint
			}
			continue
		}
		if key != k {
			return nil, &WorkDeferred{Wait: w}
		}
		t := &PreparationTicket{owner: p, key: k}
		p.pending[k] = t
		p.peakPending = max(p.peakPending, len(p.pending))
		p.queue = append(p.queue[:n], p.queue[n+1:]...)
		if entry := p.queued[k]; entry != nil {
			entry.stop()
			delete(p.queued, k)
		}
		p.signal()
		slog.Default().Debug("preparation granted", "resource_id", i.ResourceID, "backend_id", i.BackendID, "stage", i.Stage, "pending", len(p.pending), "queued", len(p.queue))
		return t, nil
	}
	return nil, &WorkDeferred{Wait: w}
}
func (t *PreparationTicket) Release() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		p := t.owner
		p.mu.Lock()
		if p.pending[t.key] == t {
			delete(p.pending, t.key)
		}
		if t.competitor != nil {
			var first, rest, tail []backend.RequestAdmissionIntent
			for _, q := range p.queue {
				k := preparationIdentity(q)
				if k == *t.competitor {
					first = append(first, q)
				} else if k == t.key {
					tail = append(tail, q)
				} else {
					rest = append(rest, q)
				}
			}
			p.queue = append(append(first, rest...), tail...)
		}
		p.signal()
		slog.Default().Debug("preparation released", "resource_id", t.key.resource, "stage", t.key.stage, "pending", len(p.pending), "queued", len(p.queue))
		p.mu.Unlock()
	})
}
func (t *PreparationTicket) MarkReserved() {
	t.owner.mu.Lock()
	t.owner.signal()
	t.owner.mu.Unlock()
}
func (t *PreparationTicket) Active() bool {
	if t == nil {
		return false
	}
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	return t.owner.pending[t.key] == t
}
func (t *PreparationTicket) Contested(ctx context.Context, gate backend.DispatchGate) bool {
	p := t.owner
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, q := range p.queue {
		k := preparationIdentity(q)
		if !samePreparationGroup(k, t.key) && len(p.pending) < p.capacity {
			continue
		}
		hint, err := p.admission.ReadyHint(ctx, q, gate)
		if err == nil && hint.Reason == "" {
			t.competitor = &k
			return true
		}
	}
	return false
}
func (p *Preparation) Changed() <-chan struct{} { p.mu.Lock(); defer p.mu.Unlock(); return p.changed }
func (p *Preparation) Snapshot() (pending, queued int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending), len(p.queue)
}
func (p *Preparation) Peaks() (pending, queued int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peakPending, p.peakQueued
}

// Resize only affects new tickets. Occupied tickets retain their owner.
func (p *Preparation) Resize(capacity int) {
	if capacity <= 0 {
		return
	}
	p.mu.Lock()
	p.capacity = capacity
	p.signal()
	p.mu.Unlock()
}
func (p *Preparation) forgetLocked(k preparationKey) {
	for n, q := range p.queue {
		if preparationIdentity(q) == k {
			p.queue = append(p.queue[:n], p.queue[n+1:]...)
			break
		}
	}
	if e := p.queued[k]; e != nil {
		e.stop()
		delete(p.queued, k)
	}
	p.signal()
}
func (p *Preparation) Forget(i backend.RequestAdmissionIntent) {
	p.mu.Lock()
	p.forgetLocked(preparationIdentity(i))
	p.mu.Unlock()
}
func (p *Preparation) Close() {
	p.mu.Lock()
	p.closed = true
	p.queue = nil
	for k, e := range p.queued {
		e.stop()
		delete(p.queued, k)
	}
	p.signal()
	p.mu.Unlock()
}

type preparationTicketKey struct{}
type coordinatorPreparationKey struct{}

func withCoordinatorPreparation(ctx context.Context) context.Context {
	return context.WithValue(ctx, coordinatorPreparationKey{}, true)
}
func coordinatorPreparation(ctx context.Context) bool {
	v, _ := ctx.Value(coordinatorPreparationKey{}).(bool)
	return v
}
func withPreparationTicket(ctx context.Context, t *PreparationTicket) context.Context {
	return context.WithValue(ctx, preparationTicketKey{}, t)
}
func preparationTicket(ctx context.Context) *PreparationTicket {
	t, _ := ctx.Value(preparationTicketKey{}).(*PreparationTicket)
	return t
}
func prepareIntent(handler RoundHandler, store RoundStore, stage backend.RequestStage) backend.RequestAdmissionIntent {
	bs, _, _, _, round := alignmentSettings(handler)
	id := 0
	if len(bs) > 0 {
		if b, ok := bs[0].(*requestBackend); ok {
			id = b.id
		}
	}
	if stage == backend.RequestStageMain {
		round = handlerRoundIndex(handler)
		switch h := handler.(type) {
		case *TranslateHandler:
			id = requestBackendID(h.Backend)
		case *ReviseHandler:
			id = requestBackendID(h.Backend)
		case *ExtractHandler:
			if len(h.Backends) > 0 {
				id = requestBackendID(h.Backends[0])
			}
		case *SemanticQAHandler:
			id = requestBackendID(h.Backend)
		case *AdjudicateHandler:
			id = requestBackendID(h.Backend)
		}
	}
	return backend.RequestAdmissionIntent{ResourceID: storeResourceID(store), RoundIndex: round, BackendID: id, Stage: stage}
}
func requestBackendID(b backend.Backend) int {
	if r, ok := b.(*requestBackend); ok {
		return r.id
	}
	return 0
}
