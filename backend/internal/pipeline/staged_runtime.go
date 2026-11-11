package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type candidateCapacityError struct{}

func (candidateCapacityError) Error() string   { return "candidate capacity exceeded" }
func (candidateCapacityError) Permanent() bool { return true }

var ErrCandidateCapacity error = candidateCapacityError{}

type CandidateLimits struct {
	Segments         int
	Bytes, ItemBytes int64
}

func DefaultCandidateLimits() CandidateLimits {
	return CandidateLimits{Segments: 256, Bytes: 16 << 20, ItemBytes: 1 << 20}
}

type windowEntry struct {
	resourceID  int
	roundID     int
	consumer    *WindowConsumer
	count       int
	bytes       int64
	reservation bool
	durable     bool
	replaced    map[string]windowEntry
}

// CandidateWindow includes reservations, pending drafts and ready results.
// Network response buffers are bounded separately by completion slots and the
// decompressed response limit. Existing drafts may exceed a lowered window;
// production stays stopped until consumers make room.
type CandidateWindow struct {
	mu        sync.Mutex
	limits    CandidateLimits
	entries   map[string]windowEntry
	ledger    *backend.RequestAdmission
	changed   chan struct{}
	consumers map[*WindowConsumer]struct{}
}

func NewCandidateWindow(l CandidateLimits) *CandidateWindow {
	if l.Segments <= 0 || l.Bytes <= 0 || l.ItemBytes <= 0 {
		l = DefaultCandidateLimits()
	}
	a, _ := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	_ = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: l.Segments, Candidates: l.Bytes, Item: l.ItemBytes, Responses: backend.DefaultMaxResponseBytes, Handoff: 64 << 20})
	return newWindowView(a, l)
}
func newWindowView(a *backend.RequestAdmission, l CandidateLimits) *CandidateWindow {
	return &CandidateWindow{ledger: a, limits: l, entries: map[string]windowEntry{}, changed: make(chan struct{}), consumers: map[*WindowConsumer]struct{}{}}
}
func (w *CandidateWindow) signal() {
	close(w.changed)
	w.changed = make(chan struct{})
	w.ledger.SignalViewChange()
}
func (w *CandidateWindow) Changed() <-chan struct{} { return w.ledger.Changed() }

// mainBatchLimit is the single request member boundary. Concurrency does not
// participate in public planning.
func (w *CandidateWindow) mainBatchLimit(concurrency int) int {
	return w.limits.Segments
}

func (w *CandidateWindow) TryReserve(key string, n int) bool {
	result, _ := w.TryReserveOwned(key, n, 0, nil)
	return result == WindowGranted
}

func (w *CandidateWindow) TryPrepareOwned(key string, n, resourceID int, consumer *WindowConsumer, replacements []string) (WindowReservationResult, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if consumer == nil || consumer.window != w || resourceID > 0 && !consumer.active {
		return WindowTooLarge, w.ledger.Changed()
	}
	if n < 1 || n > w.limits.Segments {
		return WindowTooLarge, w.ledger.Changed()
	}
	stock := w.ledger.PipelineSnapshot().Segments
	for _, id := range replacements {
		if e, ok := w.entries[id]; ok {
			stock -= e.count
		}
	}
	if n > w.limits.Segments-1-stock {
		if n > w.limits.Segments-stock {
			w.ledger.WaitMainTurn(resourceID, consumer.executionRound)
			return WindowBusy, w.ledger.Changed()
		}
		for _, e := range w.entries {
			if !w.fixedPin(e, consumer) {
				w.ledger.WaitMainTurn(resourceID, consumer.executionRound)
				return WindowBusy, w.ledger.Changed()
			}
		}
	}
	ok, err := w.ledger.PrepareMembers(key, n, resourceID, consumer.roundID, replacements)
	if err != nil {
		return WindowTooLarge, w.ledger.Changed()
	}
	if !ok {
		return WindowBusy, w.ledger.Changed()
	}
	w.entries[key] = windowEntry{resourceID: resourceID, roundID: consumer.roundID, consumer: consumer, count: n, reservation: true}
	return WindowGranted, w.ledger.Changed()
}

type WindowReservationResult uint8

const (
	WindowGranted WindowReservationResult = iota
	WindowTooLarge
	WindowBusy
	WindowPinnedLimit
)

// TryReserveOwned distinguishes permanent batch limits from current occupancy.
// The failure wakeup is captured under the occupancy lock.
func (w *CandidateWindow) TryReserveOwned(key string, n, resourceID int, consumer *WindowConsumer, replacements ...[]string) (WindowReservationResult, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if consumer != nil && (consumer.window != w || consumer.resourceID != resourceID || resourceID > 0 && !consumer.active) {
		return WindowTooLarge, w.ledger.Changed()
	}
	if existing, ok := w.entries[key]; ok {
		if existing.reservation && existing.count == n && existing.consumer == consumer {
			return WindowGranted, w.ledger.Changed()
		}
		return WindowTooLarge, w.ledger.Changed()
	}
	round := 0
	if consumer != nil {
		round = consumer.roundID
	}
	progress := w.limits.Segments == 1 || w.limits.Bytes < w.limits.ItemBytes || w.ledger.RuntimeState().ProgressID == key
	var replace []string
	if len(replacements) > 0 {
		replace = replacements[0]
	}
	previous := map[string]windowEntry{}
	for _, id := range replace {
		if e, ok := w.entries[id]; ok {
			previous[id] = e
		}
	}
	ok, err := w.ledger.ReserveMembersReplacing(key, n, resourceID, round, progress, replace)
	if err != nil {
		return WindowTooLarge, w.ledger.Changed()
	}
	if !ok {
		var pinned int
		for _, entry := range w.entries {
			if w.fixedPin(entry, consumer) {
				pinned += entry.count
			}
		}
		if pinned > 0 && n > max(0, w.limits.Segments-1-pinned) {
			return WindowPinnedLimit, w.ledger.Changed()
		}
		return WindowBusy, w.ledger.Changed()
	}
	for id := range previous {
		delete(w.entries, id)
	}
	w.entries[key] = windowEntry{resourceID: resourceID, roundID: round, consumer: consumer, count: n, reservation: true, replaced: previous}
	return WindowGranted, w.ledger.Changed()
}

// WindowConsumer is one live staged coordinator, not merely a resource ID.
// A later round or re-entry gets a new instance and cannot revive old claims.
type WindowConsumer struct {
	window         *CandidateWindow
	resourceID     int
	roundID        int
	active         bool
	recovered      bool
	executionRound int
}

func (w *CandidateWindow) freshBatchLimit(round int) (int, error) {
	return w.limits.Segments, nil
}

func (w *CandidateWindow) EnterScope(resourceID, roundID int, executionRound ...int) *WindowConsumer {
	consumer := &WindowConsumer{window: w, resourceID: resourceID, roundID: roundID, active: resourceID > 0}
	if len(executionRound) > 0 {
		consumer.executionRound = executionRound[0]
	}
	w.mu.Lock()
	if consumer.active {
		w.consumers[consumer] = struct{}{}
	}
	w.signal()
	w.mu.Unlock()
	return consumer
}

// RecoveryComplete follows a successful, exhaustive current-scope page load.
// Failed or cancelled pagination cannot prove that unclaimed drafts are foreign.
func (c *WindowConsumer) RecoveryComplete() {
	w := c.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if c.active && !c.recovered {
		c.recovered = true
		for _, entry := range w.entries {
			if c.resourceID > 0 && c.roundID > 0 && entry.resourceID == c.resourceID && entry.roundID == c.roundID && entry.consumer != c {
				// An exact-scope page loader ceased to be a potential consumer
				// without claiming this entry. Wake other waiting coordinators.
				w.signal()
				break
			}
		}
	}
}
func (c *WindowConsumer) Leave() {
	c.window.ledger.YieldMainPriority(c.resourceID, c.executionRound)
	w := c.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if c.active {
		var local []string
		for id, entry := range w.entries {
			if entry.consumer == c {
				local = append(local, id)
			}
		}
		w.ledger.ReleaseLocalGrowth(local)
		c.active = false
		delete(w.consumers, c)
		w.signal()
	}
}

func (w *CandidateWindow) fixedPin(entry windowEntry, consumer *WindowConsumer) bool {
	if consumer == nil || consumer.window != w || !consumer.active || !consumer.recovered ||
		consumer.resourceID <= 0 || consumer.roundID <= 0 || !entry.durable ||
		entry.resourceID <= 0 || entry.roundID <= 0 ||
		entry.resourceID == consumer.resourceID && entry.roundID == consumer.roundID ||
		entry.consumer != nil && entry.consumer.active {
		return false
	}
	// Header accounting has no entry-level claim yet. An active coordinator
	// loading that scope's page still makes this temporary occupancy, not a pin.
	for active := range w.consumers {
		if active.active && active.resourceID == entry.resourceID && (active.roundID == 0 || active.roundID == entry.roundID) {
			return false
		}
	}
	return true
}

// CanDrainFor excludes only proven fixed drafts. With no actual live consumer
// left, a fully pinned window still yields instead of waiting forever.
func (w *CandidateWindow) CanDrainFor(consumer *WindowConsumer) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	draining := false
	budgets := w.ledger.BudgetEntries()
	for id, entry := range w.entries {
		if e, ok := budgets[id]; ok && e.PlannedCount > 0 {
			continue
		}
		if w.fixedPin(entry, consumer) {
			continue
		}
		if entry.resourceID <= 0 || entry.consumer == nil || !entry.consumer.active {
			loading := false
			for active := range w.consumers {
				if entry.durable && !entry.reservation && active != consumer && active.active && !active.recovered && active.resourceID > 0 && active.roundID > 0 &&
					active.resourceID == entry.resourceID && active.roundID == entry.roundID {
					loading = true
					break
				}
			}
			if !loading {
				return false
			}
		}
		draining = true
	}
	return draining || w.ledger.PipelineSnapshot().Segments == 0
}
func (w *CandidateWindow) Transfer(from, to string, size int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[from]
	if !ok || !e.reservation || e.count < 1 {
		return ErrCandidateCapacity
	}
	if err := w.ledger.ApplyPromotion(from, to, size); err != nil {
		return err
	}
	e.count--
	if e.count == 0 {
		delete(w.entries, from)
	} else {
		w.entries[from] = e
	}
	w.entries[to] = windowEntry{resourceID: e.resourceID, roundID: e.roundID, consumer: e.consumer, count: 1, bytes: size, durable: true}
	w.signal()
	return nil
}

func (w *CandidateWindow) RenameReservation(from, to string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[from]
	if !ok || !e.reservation {
		return ErrCandidateCapacity
	}
	if err := w.ledger.RenameMembers(from, to); err != nil {
		return err
	}
	delete(w.entries, from)
	e.durable = true
	e.replaced = nil
	w.entries[to] = e
	w.signal()
	return nil
}

func (w *CandidateWindow) RestoreRaw(id string, count, resource, round int, consumer *WindowConsumer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.entries[id]; ok {
		return nil
	}
	if err := w.ledger.RestoreBudgetEntry(id, backend.PipelineBudgetEntry{ResourceID: resource, RoundID: round, Count: count, Reservation: true, Durable: true}); err != nil {
		return err
	}
	w.entries[id] = windowEntry{resourceID: resource, roundID: round, consumer: consumer, count: count, reservation: true, durable: true}
	w.signal()
	return nil
}

func (w *CandidateWindow) DropRawMember(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[id]
	if !ok || !e.reservation {
		return ErrCandidateCapacity
	}
	if err := w.ledger.DropBudgetMember(id); err != nil {
		return err
	}
	e.count--
	if e.count == 0 {
		delete(w.entries, id)
	} else {
		w.entries[id] = e
	}
	w.signal()
	return nil
}
func (w *CandidateWindow) Resize(key string, bytes int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.resizeLocked(key, bytes, true)
}

// ReserveResize reserves growth before saving, retaining the old bytes until
// persistence succeeds. Call Resize afterwards to release a confirmed shrink.
func (w *CandidateWindow) ReserveResize(key string, bytes int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.resizeLocked(key, bytes, false)
}

func (w *CandidateWindow) resizeLocked(key string, size int64, shrink bool) error {
	e, ok := w.entries[key]
	if !ok || e.reservation {
		return ErrCandidateCapacity
	}
	if !shrink && size < e.bytes {
		return nil
	}
	if err := w.ledger.ResizeBudgetEntry(key, size, shrink); err != nil {
		return err
	}
	e.bytes = size
	w.entries[key] = e
	w.signal()
	return nil
}
func (w *CandidateWindow) Restore(key string, bytes int64) error {
	return w.RestoreScoped(key, bytes, 0, 0)
}

// RestoreScoped accounts for a durable header without claiming its consumer.
func (w *CandidateWindow) RestoreScoped(key string, bytes int64, resourceID, roundID int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restoreLocked(key, bytes, resourceID, roundID, nil)
}

// ClaimRecovered is called only after the current Scope's page loads the DTO.
// Header-only accounting retains capacity without claiming a future round.
func (w *CandidateWindow) ClaimRecovered(key string, bytes int64, consumer *WindowConsumer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if consumer == nil || consumer.window != w || consumer.resourceID > 0 && !consumer.active {
		return fmt.Errorf("candidate consumer is not active")
	}
	return w.restoreLocked(key, bytes, consumer.resourceID, consumer.roundID, consumer)
}
func (w *CandidateWindow) restoreLocked(key string, bytes int64, resourceID, roundID int, consumer *WindowConsumer) error {
	if key == "" || bytes > w.limits.ItemBytes || bytes < 0 {
		return fmt.Errorf("saved candidate exceeds current recovery hard limit: %w", ErrCandidateCapacity)
	}
	if old, ok := w.entries[key]; ok {
		if old.reservation || old.bytes != bytes {
			return fmt.Errorf("saved candidate identity already has different capacity")
		}
		if old.resourceID != 0 && resourceID != 0 && old.resourceID != resourceID {
			return fmt.Errorf("saved candidate has a different resource owner")
		}
		if old.roundID != 0 && roundID != 0 && old.roundID != roundID {
			return fmt.Errorf("saved candidate has a different round owner")
		}
		if consumer != nil && old.consumer != nil && old.consumer.active && old.consumer != consumer {
			return fmt.Errorf("candidate already claimed by another coordinator")
		}
		changed := false
		if old.resourceID == 0 && resourceID > 0 {
			old.resourceID = resourceID
			changed = true
		}
		if old.roundID == 0 && roundID > 0 {
			old.roundID = roundID
			changed = true
		}
		if !old.durable {
			old.durable = true
			changed = true
		}
		if consumer != nil && old.consumer != consumer {
			old.consumer = consumer
			changed = true
		}
		if changed {
			w.entries[key] = old
			w.signal()
		}
		return nil
	}
	if err := w.ledger.RestoreBudgetEntry(key, backend.PipelineBudgetEntry{ResourceID: resourceID, RoundID: roundID, Count: 1, Bytes: bytes, Durable: true}); err != nil {
		return err
	}
	w.entries[key] = windowEntry{resourceID: resourceID, roundID: roundID, consumer: consumer, count: 1, bytes: bytes, durable: true}
	w.signal()
	return nil
}
func (w *CandidateWindow) Release(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.entries[key]; ok && e.reservation && !e.durable {
		for id, previous := range e.replaced {
			w.entries[id] = previous
		}
	}
	w.ledger.ReleaseBudgetEntry(key)
	delete(w.entries, key)
	w.signal()
}

type CandidateWindowSnapshot struct {
	Segments int
	Bytes    int64
}

func (w *CandidateWindow) Snapshot() CandidateWindowSnapshot {
	s := w.ledger.PipelineSnapshot()
	return CandidateWindowSnapshot{Segments: s.Segments, Bytes: s.CandidateBytes + s.GrowthBytes}
}

// ExecutionRuntime is owned by one Job (all resources) or one synchronous call.
type ExecutionRuntime struct {
	Admission                   *backend.RequestAdmission
	Window                      *CandidateWindow
	Gate                        *PauseGate
	ResourceID                  int
	JobID                       int
	parse                       chan struct{}
	Preparation                 *Preparation
	Policy                      RuntimePolicy
	Lifetime                    context.Context
	saveOnce                    sync.Once
	Saver                       *persistence.Scheduler
	Observer                    persistence.Observer
	dispatchMu                  sync.Mutex
	stopped                     bool
	ownerMu                     sync.Mutex
	mainOwners                  map[string][]*backend.RequestPermit
	ResponseBytes, HandoffBytes int64
	EpisodeStore                RuntimeEpisodeStore
	episodeMu                   sync.Mutex
}

type RuntimePolicy struct {
	PreparePending               int
	DispatchLease, HandoffBudget time.Duration
	Save                         persistence.Policy
}

func DefaultRuntimePolicy() RuntimePolicy {
	return RuntimePolicy{16, 30 * time.Second, 120 * time.Second, persistence.DefaultPolicy()}
}

// Configure runs before the runtime is shared. Existing owners are never resized.
func (r *ExecutionRuntime) Configure(policy RuntimePolicy) {
	d := DefaultRuntimePolicy()
	if policy.PreparePending <= 0 {
		policy.PreparePending = d.PreparePending
	}
	if policy.DispatchLease <= 0 {
		policy.DispatchLease = d.DispatchLease
	}
	if policy.HandoffBudget <= 0 {
		policy.HandoffBudget = d.HandoffBudget
	}
	if policy.Save.ExecutionTimeout <= 0 {
		policy.Save = d.Save
	}
	r.Policy = policy
	r.Preparation = NewPreparation(r.Admission, policy.PreparePending)
}

func NewExecutionRuntime(admission *backend.RequestAdmission, limits CandidateLimits, gate *PauseGate) *ExecutionRuntime {
	if gate == nil {
		gate = NewPauseGate()
	}
	policy := DefaultRuntimePolicy()
	if limits.Segments <= 0 || limits.Bytes <= 0 || limits.ItemBytes <= 0 {
		limits = DefaultCandidateLimits()
	}
	_ = admission.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: limits.Segments, Candidates: limits.Bytes, Item: limits.ItemBytes, Responses: backend.DefaultMaxResponseBytes, Handoff: 64 << 20})
	return &ExecutionRuntime{Admission: admission, Window: newWindowView(admission, limits), ResponseBytes: backend.DefaultMaxResponseBytes, HandoffBytes: 64 << 20, Gate: gate, parse: make(chan struct{}, 2), Policy: policy, Preparation: NewPreparation(admission, policy.PreparePending), Observer: &persistence.Collector{}}
}

func (r *ExecutionRuntime) SetResponseLimits(response, handoff int64) error {
	if response <= 0 {
		response = backend.DefaultMaxResponseBytes
	}
	if handoff <= 0 {
		handoff = 64 << 20
	}
	if err := r.Admission.SetResponseLimits(response, handoff); err != nil {
		return err
	}
	r.ResponseBytes, r.HandoffBytes = response, handoff
	return nil
}

// Close is called by the Job/call owner after every resource has joined.
func (r *ExecutionRuntime) Close() {
	if r != nil {
		r.stopDispatch()
	}
	if r != nil && r.Preparation != nil {
		r.Preparation.Close()
	}
	if r != nil && r.Admission != nil {
		r.Admission.Close()
	}
	if r != nil && r.Saver != nil {
		r.Saver.Close()
	}
}

func (r *ExecutionRuntime) saveScheduler() *persistence.Scheduler {
	r.saveOnce.Do(func() {
		r.Saver = persistence.New(r.Policy.Save, slog.Default())
	})
	return r.Saver
}
func (r *ExecutionRuntime) PersistenceSaver() persistence.Saver {
	if r == nil {
		return nil
	}
	// Initialize before producers start so snapshots can safely read Saver.
	r.saveScheduler()
	return runtimeSaver{runtime: r}
}

type runtimeSaver struct{ runtime *ExecutionRuntime }

func (s runtimeSaver) Save(ctx context.Context, meta persistence.Metadata, fn func(context.Context) error) error {
	return s.runtime.SaveNamed(ctx, meta, fn)
}
func (r *ExecutionRuntime) CompletionContext(ctx context.Context) context.Context {
	if r.Lifetime != nil {
		return completionContext{Context: r.Lifetime, values: ctx}
	}
	return context.WithoutCancel(ctx)
}

type completionContext struct {
	context.Context
	values context.Context
}

func (c completionContext) Value(key any) any {
	if value := c.values.Value(key); value != nil {
		return value
	}
	return c.Context.Value(key)
}
func (r *ExecutionRuntime) stopDispatch() {
	r.dispatchMu.Lock()
	r.stopped = true
	r.dispatchMu.Unlock()
	if r.Preparation != nil {
		r.Preparation.Close()
	}
	if r.Admission != nil {
		r.Admission.Close()
	}
}
func (r *ExecutionRuntime) beginDispatch(ctx context.Context) error {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.stopped {
		return backend.ErrAdmissionClosed
	}
	return nil
}

type executionRuntimeKey struct{}

func WithExecutionRuntime(ctx context.Context, r *ExecutionRuntime) context.Context {
	if r != nil {
		ctx = backend.WithOwnedObserver(ctx, r.Admission)
	}
	if r != nil {
		ctx = persistence.WithExecutionTimeout(ctx, r.Policy.Save.ExecutionTimeout)
	}
	return context.WithValue(ctx, executionRuntimeKey{}, r)
}
func ExecutionRuntimeFromContext(ctx context.Context) *ExecutionRuntime {
	r, _ := ctx.Value(executionRuntimeKey{}).(*ExecutionRuntime)
	return r
}
func (r *ExecutionRuntime) Save(ctx context.Context, fn func(context.Context) error) error {
	meta := persistence.Metadata{Operation: "handoff", Class: persistence.Reliable, ResourceID: r.ResourceID}
	if s, _ := ctx.Value(requestSessionKey{}).(*requestSession); s != nil {
		meta.RequestID = s.lastID
		meta.ResourceID = s.intent.ResourceID
		s.handoffMu.Lock()
		deadline := s.handoffDeadline
		s.handoffMu.Unlock()
		if !deadline.IsZero() {
			ctx = persistence.WithDeadline(ctx, deadline)
		}
	}
	return r.SaveNamed(ctx, meta, fn)
}
func (r *ExecutionRuntime) SaveNamed(ctx context.Context, meta persistence.Metadata, fn func(context.Context) error) error {
	if s, _ := ctx.Value(requestSessionKey{}).(*requestSession); s != nil {
		if meta.RequestID == "" {
			meta.RequestID = s.lastID
		}
		if meta.ResourceID == 0 {
			meta.ResourceID = s.intent.ResourceID
		}
		s.handoffMu.Lock()
		deadline := s.handoffDeadline
		s.handoffMu.Unlock()
		if !deadline.IsZero() {
			ctx = persistence.WithDeadline(ctx, deadline)
		}
	}
	if meta.JobID == 0 {
		meta.JobID = r.JobID
	}
	ctx = persistence.WithLifetime(ctx, r.CompletionContext(ctx))
	if r.Observer != nil {
		ctx = persistence.WithObserver(ctx, persistence.Fanout(persistence.ObserverFromContext(ctx), r.Observer))
	}
	if err := r.saveScheduler().Save(ctx, meta, fn); err != nil {
		return &StorageError{Err: err}
	}
	return nil
}

// StorageError is permanent with respect to model retries. Save already retried
// transient local failures; returning this error must abort the execution run.
type StorageError struct{ Err error }

func (e *StorageError) Error() string { return fmt.Sprintf("candidate persistence failed: %v", e.Err) }
func (e *StorageError) Unwrap() error { return e.Err }
func (*StorageError) Permanent() bool { return true }
func storageFailure(err error) bool   { var s *StorageError; return errors.As(err, &s) }

type requestSession struct {
	mode              string
	deferred          *WorkDeferred
	runtime           *ExecutionRuntime
	store             RoundStore
	reporter          progress.Reporter
	intent            RequestIntent
	permits           []*backend.RequestPermit
	preWire, preInput int64
	preTicket         *PreparationTicket
	lastID            string
	calls             int
	err               error
	rawResponse       *MainResponse
	parsing           bool
	pending           []string
	completionCtx     context.Context
	handoffDeadline   time.Time
	handoffMu         sync.Mutex
	callCtx           context.Context
	workCancel        context.CancelFunc
	handoffTimer      *time.Timer
	handoffExpired    atomic.Bool
}
type requestSessionKey struct{}

func (s *requestSession) finish() error {
	if s.preTicket != nil && s.calls == 0 {
		if abort, ok := s.store.(RequestAborter); ok {
			if err := s.runtime.SaveNamed(s.accountingContext(context.Background()), persistence.Metadata{Operation: "abort", Class: persistence.Reliable, RequestID: s.intent.ID, ResourceID: s.intent.ResourceID}, func(ctx context.Context) error { return abort.AbortRequest(ctx, s.intent.ID) }); err != nil {
				s.fail(err)
			}
		}
		if s.preTicket != nil {
			s.preTicket.Release()
		}
	}
	defer func() {
		if s.handoffTimer != nil {
			s.handoffTimer.Stop()
		}
		if s.workCancel != nil {
			s.workCancel()
		}
	}()
	defer s.releaseResponse()
	if s.handoffExpired.Load() {
		return s.fail(&StorageError{Err: context.DeadlineExceeded})
	}
	if s.err != nil {
		return s.err
	}
	ctx := s.completionCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for len(s.pending) > 0 {
		if err := s.record(ctx, s.pending[0], RequestRecord{State: "completed"}); err != nil {
			return s.fail(err)
		}
		s.pending = s.pending[1:]
	}
	s.completionCtx = nil
	return nil
}
func (s *requestSession) Context(ctx context.Context) context.Context {
	ctx = backend.WithOwnedObserver(ctx, s.runtime.Admission)
	workCtx, cancel := context.WithCancel(ctx)
	s.workCancel = cancel
	s.callCtx = context.WithValue(workCtx, requestSessionKey{}, s)
	return s.callCtx
}
func (s *requestSession) startHandoff(deadline time.Time) {
	s.handoffMu.Lock()
	defer s.handoffMu.Unlock()
	if s.handoffDeadline.IsZero() || deadline.Before(s.handoffDeadline) {
		s.handoffDeadline = deadline
		if s.handoffTimer != nil {
			s.handoffTimer.Stop()
		}
		if s.workCancel != nil {
			s.handoffTimer = time.AfterFunc(max(time.Until(deadline), time.Nanosecond), func() { s.handoffExpired.Store(true); s.workCancel() })
		}
	}
}

func (s *requestSession) record(ctx context.Context, id string, record RequestRecord) error {
	err := s.runtime.SaveNamed(ctx, persistence.Metadata{Operation: record.State, Class: persistence.Reliable, ResourceID: s.intent.ResourceID, RequestID: id}, func(saveCtx context.Context) error { return s.store.Record(saveCtx, id, record) })
	if err == nil {
		progress.NotifyWorkState(s.reporter)
	}
	return err
}

// Later calls retain the first response's handoff deadline. Necessary facts use
// worker lifetime cancellation, while their budget cannot restart after a retry.
func (s *requestSession) accountingContext(ctx context.Context) context.Context {
	deadline := time.Now().Add(s.runtime.Policy.HandoffBudget)
	if !s.handoffDeadline.IsZero() && s.handoffDeadline.Before(deadline) {
		deadline = s.handoffDeadline
	}
	if parent := persistence.Deadline(ctx); !parent.IsZero() && parent.Before(deadline) {
		deadline = parent
	}
	return persistence.WithDeadline(s.runtime.CompletionContext(ctx), deadline)
}

func (s *requestSession) releaseResponse() {
	if s.parsing {
		<-s.runtime.parse
		s.parsing = false
	}
	for _, p := range s.permits {
		p.Release()
	}
	s.permits = nil
}

func (s *requestSession) acquireParse(ctx context.Context) error {
	if s.parsing {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.runtime.parse <- struct{}{}:
		s.parsing = true
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *requestSession) fail(err error) error {
	if s.handoffExpired.Load() {
		err = &StorageError{Err: errors.Join(err, context.DeadlineExceeded)}
	}
	if workDeferred(err) != nil {
		return err
	}
	if s.err == nil {
		s.err = err
		var storage *StorageError
		if errors.As(err, &storage) {
			s.runtime.stopDispatch()
		}
	}
	return err
}

// requestBackend is deliberately outside policy/timeout wrappers: admission
// waiting is not charged to a provider call timeout.
type requestBackend struct {
	inner     backend.Backend
	id, round int
	runtime   *ExecutionRuntime
}

func BindRequestBackend(b backend.Backend, id, round int, runtime *ExecutionRuntime) backend.Backend {
	if b == nil || runtime == nil || runtime.Admission == nil {
		return b
	}
	return &requestBackend{inner: b, id: id, round: round, runtime: runtime}
}
func (b *requestBackend) Name() string             { return b.inner.Name() }
func (b *requestBackend) Close() error             { return b.inner.Close() }
func (b *requestBackend) Backend() backend.Backend { return b.inner }

type directRequestIDKey struct{}

func (b *requestBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	if session, _ := ctx.Value(requestSessionKey{}).(*requestSession); session == nil {
		// A direct call owns one finite prepared input while waiting for RPM.
		// Its descriptor must keep this identity until the call finishes.
		id := NewWorkID()
		ctx = context.WithValue(ctx, directRequestIDKey{}, id)
		defer b.runtime.Admission.ReleasePrepared(id)
		defer b.forgetPreparation(ctx)
	}
	for {
		resp, err := b.translate(ctx, req)
		d := workDeferred(err)
		s, _ := ctx.Value(requestSessionKey{}).(*requestSession)
		if s != nil && s.handoffExpired.Load() {
			return nil, s.fail(&StorageError{Err: context.DeadlineExceeded})
		}
		canDefer := coordinatorPreparation(ctx) && (s == nil || s.calls == 0)
		if d == nil || canDefer {
			if d != nil && s != nil {
				s.deferred = d
			}
			if s != nil && (errors.Is(err, backend.ErrPipelineCapacity) || errors.Is(err, backend.ErrMainBatchTooLarge) || errors.Is(err, backend.ErrAdmissionClosed)) {
				s.fail(err)
			}
			return resp, err
		}
		if err = waitPreparation(ctx, d.Wait); err != nil {
			return nil, err
		}
	}
}
func (b *requestBackend) translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	if err := requestBackendPolicy(ctx, b.inner); err != nil {
		b.forgetPreparation(ctx)
		return nil, err
	}
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	if session != nil {
		if session.err != nil {
			return nil, session.err
		}
		// A subsequent call (prompt upgrade) follows parsing and request-fact
		// storage of the preceding result. Release its slots before re-admission.
		session.releaseResponse()
	}
	intent := RequestIntent{ID: NewWorkID(), Stage: backend.RequestStageMain, BackendID: b.id, RoundIndex: b.round}
	if id, _ := ctx.Value(directRequestIDKey{}).(string); session == nil && id != "" {
		intent.ID = id
	}
	if session != nil {
		intent = session.intent
		if intent.ID == "" || session.calls > 0 {
			intent.ID = NewWorkID()
		}
		intent.BackendID = b.id
		if session.calls > 0 && intent.Stage == backend.RequestStageMain {
			intent.Phase = "prompt_upgrade"
		}
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, req.System)
	_, _ = digest.Write([]byte{0})
	_, _ = io.WriteString(digest, req.User)
	if session == nil || session.preTicket == nil {
		intent.InputDigest = hex.EncodeToString(digest.Sum(nil))
	}
	wireBytes, wireErr := backend.MeasureRequest(b.inner, req)
	if wireErr != nil {
		return nil, wireErr
	}
	ctx = backend.WithRequestCredits(ctx, wireBytes, b.runtime.responseLimit())
	ctx = backend.WithBuildWorkers(ctx, b.runtime)
	if b.runtime.Gate != nil && b.runtime.Gate.Paused() {
		return nil, backend.ErrDispatchPaused
	}
	admissionIntent := backend.RequestAdmissionIntent{RequestID: intent.ID, RoundIndex: intent.RoundIndex, ResourceID: intent.ResourceID, BackendID: b.id, Stage: intent.Stage}
	inputBytes, inputErr := boundedSum(wireBytes, int64(len(req.System)), int64(len(req.User)))
	if inputErr != nil {
		return nil, inputErr
	}
	if intent.Stage == backend.RequestStageMain {
		ok, prepareErr := b.runtime.Admission.PrepareMain(intent.ID, intent.RoundIndex, intent.ResourceID, int64(len(intent.FreezePayload)), inputBytes, intent.BusinessBound, b.runtime.Window.limits.Segments == 1 || b.runtime.Window.limits.Bytes < b.runtime.Window.limits.ItemBytes || b.runtime.Admission.RuntimeState().ProgressID == intent.ID, len(intent.Indices))
		if prepareErr != nil {
			return nil, fmt.Errorf("prepare main %s freeze%d/input%d: %w", intent.ID, len(intent.FreezePayload), inputBytes, prepareErr)
		}
		if !ok {
			return nil, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "prepared_bytes", CapacityChanged: b.runtime.Admission.Changed()}}
		}
		if session != nil {
			defer b.runtime.Admission.ReleasePrepared(intent.ID)
		}
		if b.runtime.Window.limits.Segments == 1 || b.runtime.Window.limits.Bytes < b.runtime.Window.limits.ItemBytes {
			if err := b.runtime.EnsureProgress(ctx, intent.ID, intent.ID, intent.ResourceID, intent.RoundIndex); err != nil {
				if errors.Is(err, backend.ErrProgressRequired) {
					return nil, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "progress_owner", CapacityChanged: b.runtime.Admission.Changed()}}
				}
				return nil, err
			}
		}
	} else {
		admissionIntent.InputBytes = inputBytes
		ok, e := b.runtime.Admission.PrepareAux(intent.ID, inputBytes, len(intent.Members))
		if e != nil {
			return nil, e
		}
		if !ok {
			return nil, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "aux_input", CapacityChanged: b.runtime.Admission.Changed()}}
		}
		defer b.runtime.Admission.ReleasePrepared(intent.ID)
		for _, member := range intent.Members {
			admissionIntent.CandidateIDs = append(admissionIntent.CandidateIDs, member.CandidateID)

		}
	}
	var permit *backend.RequestPermit
	var err error
	ticket := preparationTicket(ctx)
	if session != nil && session.preTicket != nil {
		if session.preWire != wireBytes || session.preInput != inputBytes {
			return nil, backend.ErrBuildCapacity
		}
		ticket = session.preTicket
	}
	if !ticket.Active() {
		var err error
		ticket, err = b.runtime.Preparation.Try(ctx, admissionIntent, b.runtime.Gate)
		if err != nil {
			// An unfrozen direct-save retry gets a fresh request identity. Its
			// released prepared K must not leave an old identity in this group.
			if session != nil && len(intent.FreezePayload) == 0 && workDeferred(err) != nil {
				b.forgetPreparation(ctx)
			}
			return nil, fmt.Errorf("prepare dispatch %s: %w", intent.ID, err)
		}
	}
	if session != nil && session.store != nil && session.preTicket == nil {
		if err := session.runtime.SaveNamed(ctx, persistence.Metadata{Operation: "reserve", Class: persistence.Normal, ResourceID: intent.ResourceID, RequestID: intent.ID}, func(saveCtx context.Context) error { return session.store.Reserve(saveCtx, intent) }); err != nil {
			if errors.Is(err, backend.ErrDispatchPaused) {
				// The durable pause intent may win before the in-memory callback.
				// No reservation succeeded, so this is a dispatch stop, not a failed
				// response save or a debit eligible for refund.
				if b.runtime.Gate != nil {
					b.runtime.Gate.Pause()
				}
				ticket.Release()
				return nil, backend.ErrDispatchPaused
			}
			return nil, session.fail(err)
		}
	}
	ticket.MarkReserved()
	permit, err = b.acquire(ctx, admissionIntent, ticket)
	if errors.Is(err, backend.ErrProgressRequired) && len(admissionIntent.CandidateIDs) == 1 {
		if progressErr := b.runtime.EnsureProgress(ctx, admissionIntent.CandidateIDs[0], "", intent.ResourceID, intent.RoundIndex); progressErr == nil {
			permit, err = b.acquire(ctx, admissionIntent, ticket)
		} else {
			err = progressErr
		}
	}

	if err == nil {
		err = b.runtime.beginDispatch(ctx)
		if err == nil {
			err = permit.StartDispatch(ctx)
		}
		if err != nil && permit != nil {
			permit.Release()
			permit = nil
		}
	}
	if err != nil {
		// Acquire joins any racing grant before returning an error; this caller
		// never dispatched. Refund even when pause/cancellation ended the wait.
		if session != nil && session.store != nil {
			if aborter, ok := session.store.(RequestAborter); ok {
				abortCtx := session.accountingContext(ctx)
				if abortErr := session.runtime.SaveNamed(abortCtx, persistence.Metadata{Operation: "abort", Class: persistence.Reliable, ResourceID: intent.ResourceID, RequestID: intent.ID}, func(saveCtx context.Context) error {
					return aborter.AbortRequest(saveCtx, intent.ID)
				}); abortErr != nil {
					return nil, session.fail(errors.Join(err, abortErr))
				}
				progress.NotifyWorkState(session.reporter)
				ticket.Release()
			}
		} else {
			ticket.Release()
		}
		if errors.Is(err, backend.ErrStalePermit) {
			return nil, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "policy_changed", CapacityChanged: b.runtime.Admission.Changed(), PolicyChanged: b.runtime.Admission.PolicyChanged()}}
		}
		if workDeferred(err) != nil {
			return nil, err
		}
		if policyErr := requestBackendPolicy(ctx, b.inner); policyErr != nil {
			return nil, policyErr
		}
		return nil, fmt.Errorf("acquire request %s: %w", intent.ID, err)
	}
	ticket.Release()
	slog.Default().Debug("request phase", "request_id", intent.ID, "resource_id", intent.ResourceID, "request_phase", "dispatch_started")
	if session == nil {
		defer permit.Release()
	} else {
		session.permits = append(session.permits, permit)
		session.calls++
		session.lastID = intent.ID
	}
	start := time.Now()
	var sentDone chan error
	if session != nil && session.store != nil {
		// The completion reservation bounds these writers. A granted network
		// permit must start its request immediately, never wait on the save queue.
		sentDone = make(chan error, 1)
		sentCtx := session.accountingContext(ctx)
		go func() {
			err := session.record(sentCtx, intent.ID, RequestRecord{State: "sent"})
			var uncertain interface{ Uncertain() bool }
			if errors.As(err, &uncertain) && uncertain.Uncertain() {
				b.runtime.stopDispatch()
			}
			sentDone <- err
		}()
	}
	resp, callErr := b.inner.Translate(backend.WithAdmittedRequest(ctx), req)
	duration := time.Since(start)
	permit.RequestDone()
	if session != nil && session.store != nil {
		deadline := time.Now().Add(b.runtime.Policy.HandoffBudget)
		session.startHandoff(deadline)
		handoffCtx := persistence.WithDeadline(b.runtime.CompletionContext(ctx), session.handoffDeadline)
		phase := "dispatch_returned"
		if resp != nil {
			phase = "response_received"
		}
		slog.Default().Debug("request phase", "request_id", intent.ID, "request_phase", phase, "provider_ms", duration.Milliseconds())
		// Join the intermediate fact first. Received records usage but retains a
		// saving state until reliable handoff/confirmation calls session.finish.
		sentErr := <-sentDone
		if sentErr != nil {
			slog.Default().Warn("sent fact persistence failed", "request_id", intent.ID, "request_phase", "persistence_pending", "error", sentErr)
		}
		record := RequestRecord{State: "received", Duration: duration, UsageKnown: resp != nil}
		if resp != nil {
			record.Usage = resp.Usage
		}
		if callErr != nil {
			record.State = "failed"
			record.Error = callErr.Error()
		}
		if callErr == nil && resp != nil && int64(len(resp.Text)) > session.runtime.responseLimit() {
			callErr = &backend.ResponseLimitError{Limit: session.runtime.responseLimit()}
			record.State = "failed"
			record.Error = callErr.Error()
		}
		if len(record.Error) > 2048 {
			record.Error = record.Error[:2048]
		}
		if callErr == nil && len(intent.FreezePayload) > 0 && resp != nil {
			response := MainResponse{MainResponseHeader: MainResponseHeader{ID: intent.ID, ResourceID: intent.ResourceID, SnapshotDigest: intent.SnapshotDigest, BusinessBound: intent.BusinessBound, Truncated: resp.Truncated}, FreezePayload: intent.FreezePayload, Raw: []byte(resp.Text), Members: intent.PromotionMembers, Record: record}
			session.runtime.Admission.ObserveOwned("raw", intent.ID, int64(cap(response.Raw)))
			resp.Text = ""
			resp.Raw = nil
			if err := session.runtime.SaveNamed(handoffCtx, persistence.Metadata{Operation: "response_handoff", Class: persistence.Handoff, ResourceID: intent.ResourceID, RequestID: intent.ID}, func(saveCtx context.Context) error { return session.store.HandoffResponse(saveCtx, response) }); err != nil {
				return nil, session.fail(errors.Join(sentErr, err))
			}
			response.FreezeBytes, response.ResponseBytes = int64(len(response.FreezePayload)), int64(len(response.Raw))
			response.FreezeDigest, response.ResponseDigest = responseDigest(response.FreezePayload), responseDigest(response.Raw)
			response.MemberCount, response.Phase = len(response.Members), "response_pending"
			if err := session.runtime.Admission.ApplyResponseHandoff(intent.ID, response.ResponseBytes); err != nil {
				return nil, session.fail(err)
			}
			session.rawResponse = &response
			session.runtime.retainMainOwner(intent.ID, session.permits)
			session.permits = nil
			if err := emitRawBatchEvent(handoffCtx, session, req, response, b.inner.Name()); err != nil {
				return nil, session.fail(err)
			}
			progress.NotifyWorkState(session.reporter)
		} else if err := session.record(handoffCtx, intent.ID, record); err != nil {
			return nil, session.fail(errors.Join(sentErr, err))
		}
		var uncertain interface{ Uncertain() bool }
		if errors.As(sentErr, &uncertain) && uncertain.Uncertain() {
			return nil, session.fail(sentErr)
		}
		if callErr == nil && session.rawResponse == nil {
			session.pending = append(session.pending, intent.ID)
			session.completionCtx = handoffCtx
		}
	}
	if session != nil {
		if callErr != nil {
			// Legacy handlers can sleep before returning a retry. A failed call
			// has no parse buffer once its terminal accounting is durable.
			session.releaseResponse()

		}
	}
	return resp, callErr
}

func (b *requestBackend) forgetPreparation(ctx context.Context) {
	i := backend.RequestAdmissionIntent{RoundIndex: b.round, BackendID: b.id, Stage: backend.RequestStageMain}
	if s, _ := ctx.Value(requestSessionKey{}).(*requestSession); s != nil {
		i.ResourceID = s.intent.ResourceID
		i.RoundIndex = s.intent.RoundIndex
		i.Stage = s.intent.Stage
	}
	b.runtime.Preparation.Forget(i)
}

func requestBackendPolicy(ctx context.Context, b backend.Backend) error {
	for b != nil {
		if checked, ok := b.(interface{ Check(context.Context) error }); ok {
			return checked.Check(ctx)
		}
		wrapped, ok := b.(interface{ Backend() backend.Backend })
		if !ok {
			return nil
		}
		b = wrapped.Backend()
	}
	return nil
}

// The waiter belongs to a bounded preparation owner, never the dispatcher.
func (b *requestBackend) acquire(ctx context.Context, intent backend.RequestAdmissionIntent, ticket *PreparationTicket) (*backend.RequestPermit, error) {
	if coordinatorPreparation(ctx) {
		hint, err := b.runtime.Admission.ReadyHint(ctx, intent, b.runtime.Gate)
		if err != nil {
			return nil, err
		}
		if hint.Reason != "" && hint.Reason != "backend_rpm" {
			return nil, &WorkDeferred{Wait: hint}
		}
		// ReadyHint previews local roots only. The immediate joint probe must
		// also check actor/instance payload and reliable storage before queueing.
		permit, wait, err := b.runtime.Admission.TryAdmit(ctx, intent, b.runtime.Gate)
		if err != nil || permit != nil {
			return permit, err
		}
		if wait.Reason != "backend_rpm" {
			return nil, &WorkDeferred{Wait: wait}
		}
	}
	type result struct {
		permit *backend.RequestPermit
		err    error
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan result, 1)
	go func() { p, e := b.runtime.Admission.Acquire(waitCtx, intent, b.runtime.Gate); done <- result{p, e} }()
	lease := b.runtime.Policy.DispatchLease
	if lease <= 0 {
		lease = 30 * time.Second
	}
	timer := time.NewTimer(lease)
	defer timer.Stop()
	expired := false
	for {
		changed := b.runtime.Preparation.Changed()
		admissionChanged := b.runtime.Admission.Changed()
		policyChanged := b.runtime.Admission.PolicyChanged()
		if expired && ticket.Contested(ctx, b.runtime.Gate) {
			cancel()
			r := <-done
			if r.permit != nil {
				return r.permit, r.err
			}
			return nil, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "dispatch_lease", CapacityChanged: b.runtime.Preparation.Changed()}}
		}
		select {
		case r := <-done:
			return r.permit, r.err
		case <-timer.C:
			expired = true
		case <-changed:
		case <-admissionChanged:
		case <-policyChanged:
		case <-ctx.Done():
			cancel()
			r := <-done
			return r.permit, r.err
		}
	}
}

// RestoreGrowth reconstructs a dispatched member's durable Q credit before any payload decode.
func (w *CandidateWindow) RestoreGrowth(id string) error { return w.ledger.RestoreGrowth(id) }

// TransferAllowed uses the completed header page and actual live scope claims.
// A later round in this resource cannot consume an earlier-round progress lane.
func (w *CandidateWindow) TransferAllowed(id string, resource, executionRound int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	entry, ok := w.entries[id]
	if !ok {
		return false
	}
	for consumer := range w.consumers {
		if consumer.active && consumer.recovered && consumer.resourceID == resource && consumer.executionRound == executionRound {
			return w.fixedPin(entry, consumer)
		}
	}
	return false
}

func (w *CandidateWindow) PinnedRawIDs(resource, round int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ids []string
	for consumer := range w.consumers {
		if consumer.active && consumer.recovered && consumer.resourceID == resource && consumer.executionRound == round {
			for id, e := range w.entries {
				if e.reservation && w.fixedPin(e, consumer) {
					ids = append(ids, id)
				}
			}
			break
		}
	}
	return ids
}

func (w *CandidateWindow) ShrinkReservation(id string, count int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[id]
	if !ok || !e.reservation || e.durable || count < 1 || count > e.count {
		return ErrCandidateCapacity
	}
	if err := w.ledger.ShrinkMembers(id, count); err != nil {
		return err
	}
	e.count = count
	w.entries[id] = e
	w.signal()
	return nil
}

func (w *CandidateWindow) SplitOwnedMembers(from string, ids []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[from]
	if !ok || !e.reservation || len(ids) > e.count {
		return ErrCandidateCapacity
	}
	if err := w.ledger.SplitOwnedMembers(from, ids); err != nil {
		return err
	}
	delete(w.entries, from)
	for _, id := range ids {
		w.entries[id] = windowEntry{resourceID: e.resourceID, roundID: e.roundID, consumer: e.consumer, count: 1, reservation: true, durable: true}
	}
	w.signal()
	return nil
}

func (w *CandidateWindow) ReplacesProgress(from, to string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[to]
	if !ok || !e.reservation || e.durable || e.count != 1 {
		return false
	}
	old, ok := e.replaced[from]
	return ok && old.reservation && old.durable && old.count == 1
}

func (w *CandidateWindow) RetireToMember(from, to string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[from]
	if !ok || e.reservation || e.count != 1 {
		return ErrCandidateCapacity
	}
	if err := w.ledger.RetireToMember(from, to); err != nil {
		return err
	}
	delete(w.entries, from)
	w.entries[to] = windowEntry{resourceID: e.resourceID, roundID: e.roundID, consumer: e.consumer, count: 1, reservation: true, durable: true}
	w.signal()
	return nil
}
