package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
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
}

// CandidateWindow includes reservations, pending drafts and ready results.
// Network response buffers are bounded separately by completion slots and the
// decompressed response limit. Existing drafts may exceed a lowered window;
// production stays stopped until consumers make room.
type CandidateWindow struct {
	mu        sync.Mutex
	limits    CandidateLimits
	entries   map[string]windowEntry
	count     int
	bytes     int64
	changed   chan struct{}
	consumers map[*WindowConsumer]struct{}
}

func NewCandidateWindow(l CandidateLimits) *CandidateWindow {
	if l.Segments <= 0 || l.Bytes <= 0 || l.ItemBytes <= 0 {
		l = DefaultCandidateLimits()
	}
	return &CandidateWindow{limits: l, entries: map[string]windowEntry{}, changed: make(chan struct{}), consumers: map[*WindowConsumer]struct{}{}}
}
func (w *CandidateWindow) signal() { close(w.changed); w.changed = make(chan struct{}) }
func (w *CandidateWindow) Changed() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changed
}
func (w *CandidateWindow) TryReserve(key string, n int) bool {
	result, _ := w.TryReserveOwned(key, n, 0, nil)
	return result == WindowGranted
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
func (w *CandidateWindow) TryReserveOwned(key string, n, resourceID int, consumer *WindowConsumer) (WindowReservationResult, <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if consumer != nil && (consumer.window != w || consumer.resourceID != resourceID || resourceID > 0 && !consumer.active) {
		return WindowTooLarge, w.changed
	}
	if existing, exists := w.entries[key]; exists {
		if existing.reservation && existing.count == n && existing.resourceID == resourceID && existing.consumer == consumer {
			return WindowGranted, w.changed
		}
		return WindowTooLarge, w.changed
	}
	if key == "" || n < 1 || n > w.limits.Segments || int64(n) > math.MaxInt64/w.limits.ItemBytes {
		return WindowTooLarge, w.changed
	}
	bytes := int64(n) * w.limits.ItemBytes
	// Preserve the original empty-window singleton progress exception when a
	// lowered byte limit is smaller than the per-item recovery hard limit.
	if bytes > w.limits.Bytes && n != 1 {
		return WindowTooLarge, w.changed
	}
	// Proven inactive recovery drafts outside this scope cannot drain in this
	// coordinator. Preserve HEAD's prefix batching against that fixed occupancy
	// only; active reservations never reduce this ceiling.
	var pinnedCount int
	var pinnedBytes int64
	for _, entry := range w.entries {
		if w.fixedPin(entry, consumer) {
			pinnedCount += entry.count
			pinnedBytes += entry.bytes
		}
	}
	if pinnedCount > 0 && (n > w.limits.Segments-pinnedCount || bytes > w.limits.Bytes-pinnedBytes) {
		if n == 1 {
			return WindowBusy, w.changed
		}
		return WindowPinnedLimit, w.changed
	}
	if n > w.limits.Segments-w.count || bytes > w.limits.Bytes-w.bytes {
		if w.count != 0 || n != 1 {
			return WindowBusy, w.changed
		}
	}
	roundID := 0
	if consumer != nil {
		roundID = consumer.roundID
	}
	w.entries[key] = windowEntry{resourceID: resourceID, roundID: roundID, consumer: consumer, count: n, bytes: bytes, reservation: true}
	w.count += n
	w.bytes += bytes
	return WindowGranted, w.changed
}

// WindowConsumer is one live staged coordinator, not merely a resource ID.
// A later round or re-entry gets a new instance and cannot revive old claims.
type WindowConsumer struct {
	window     *CandidateWindow
	resourceID int
	roundID    int
	active     bool
	recovered  bool
}

func (w *CandidateWindow) EnterScope(resourceID, roundID int) *WindowConsumer {
	consumer := &WindowConsumer{window: w, resourceID: resourceID, roundID: roundID, active: resourceID > 0}
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
	w := c.window
	w.mu.Lock()
	defer w.mu.Unlock()
	if c.active {
		c.active = false
		delete(w.consumers, c)
		w.signal()
	}
}

func (w *CandidateWindow) fixedPin(entry windowEntry, consumer *WindowConsumer) bool {
	if consumer == nil || consumer.window != w || !consumer.active || !consumer.recovered ||
		consumer.resourceID <= 0 || consumer.roundID <= 0 || !entry.durable || entry.reservation ||
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
	for _, entry := range w.entries {
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
	return draining || len(w.entries) == 0
}
func (w *CandidateWindow) Transfer(from, to string, bytes int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if to == "" || bytes < 0 || bytes > w.limits.ItemBytes {
		return ErrCandidateCapacity
	}
	e, ok := w.entries[from]
	if !ok || e.count < 1 || !e.reservation {
		return fmt.Errorf("missing candidate reservation")
	}
	if _, ok := w.entries[to]; ok {
		return fmt.Errorf("duplicate candidate identity")
	}
	e.count--
	e.bytes -= w.limits.ItemBytes
	if e.count == 0 {
		delete(w.entries, from)
	} else {
		w.entries[from] = e
	}
	w.entries[to] = windowEntry{resourceID: e.resourceID, roundID: e.roundID, consumer: e.consumer, count: 1, bytes: bytes}
	w.bytes += bytes - w.limits.ItemBytes
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

func (w *CandidateWindow) resizeLocked(key string, bytes int64, shrink bool) error {
	old, ok := w.entries[key]
	if !ok {
		return fmt.Errorf("candidate reservation missing")
	}
	if old.reservation || bytes > w.limits.ItemBytes || bytes < 0 {
		return ErrCandidateCapacity
	}
	delta := bytes - old.bytes
	if delta < 0 && !shrink {
		return nil
	}
	if delta > 0 && delta > w.limits.Bytes-w.bytes && w.count > 1 {
		return ErrCandidateCapacity
	}
	w.bytes += delta
	old.bytes = bytes
	w.entries[key] = old
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
	if bytes > math.MaxInt64-w.bytes || w.count == math.MaxInt {
		return ErrCandidateCapacity
	}
	w.entries[key] = windowEntry{resourceID: resourceID, roundID: roundID, consumer: consumer, count: 1, bytes: bytes, durable: true}
	w.count++
	w.bytes += bytes
	w.signal()
	return nil
}
func (w *CandidateWindow) Release(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.entries[key]; ok {
		w.count -= e.count
		w.bytes -= e.bytes
		delete(w.entries, key)
		w.signal()
	}
}

type CandidateWindowSnapshot struct {
	Segments int
	Bytes    int64
}

func (w *CandidateWindow) Snapshot() CandidateWindowSnapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return CandidateWindowSnapshot{Segments: w.count, Bytes: w.bytes}
}

// ExecutionRuntime is owned by one Job (all resources) or one synchronous call.
type ExecutionRuntime struct {
	Admission   *backend.RequestAdmission
	Window      *CandidateWindow
	Gate        *PauseGate
	ResourceID  int
	JobID       int
	parse       chan struct{}
	Preparation *Preparation
	Policy      RuntimePolicy
	Lifetime    context.Context
	saveOnce    sync.Once
	Saver       *persistence.Scheduler
	Observer    persistence.Observer
	dispatchMu  sync.Mutex
	stopped     bool
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
	return &ExecutionRuntime{Admission: admission, Window: NewCandidateWindow(limits), Gate: gate, parse: make(chan struct{}, 2), Policy: policy, Preparation: NewPreparation(admission, policy.PreparePending), Observer: &persistence.Collector{}}
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
	runtime         *ExecutionRuntime
	store           RoundStore
	reporter        progress.Reporter
	intent          RequestIntent
	permits         []*backend.RequestPermit
	lastID          string
	calls           int
	err             error
	parsing         bool
	pending         []string
	completionCtx   context.Context
	handoffDeadline time.Time
	handoffMu       sync.Mutex
	callCtx         context.Context
	workCancel      context.CancelFunc
	handoffTimer    *time.Timer
	handoffExpired  atomic.Bool
}
type requestSessionKey struct{}

func (s *requestSession) finish() error {
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
func (b *requestBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	for {
		resp, err := b.translate(ctx, req)
		d := workDeferred(err)
		s, _ := ctx.Value(requestSessionKey{}).(*requestSession)
		if s != nil && s.handoffExpired.Load() {
			return nil, s.fail(&StorageError{Err: context.DeadlineExceeded})
		}
		canDefer := coordinatorPreparation(ctx) && (s == nil || s.calls == 0)
		if d == nil || canDefer {
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
	if session != nil {
		intent = session.intent
		intent.ID = NewWorkID()
		intent.BackendID = b.id
		if session.calls > 0 && intent.Stage == backend.RequestStageMain {
			intent.Phase = "prompt_upgrade"
		}
	}
	digest := sha256.Sum256([]byte(req.System + "\x00" + req.User))
	intent.InputDigest = hex.EncodeToString(digest[:])
	if b.runtime.Gate != nil && b.runtime.Gate.Paused() {
		return nil, backend.ErrDispatchPaused
	}
	admissionIntent := backend.RequestAdmissionIntent{RoundIndex: intent.RoundIndex, ResourceID: intent.ResourceID, BackendID: b.id, Stage: intent.Stage}
	ticket := preparationTicket(ctx)
	if !ticket.Active() {
		var err error
		ticket, err = b.runtime.Preparation.Try(ctx, admissionIntent, b.runtime.Gate)
		if err != nil {
			return nil, err
		}
	}
	if session != nil && session.store != nil {
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
	permit, err := b.acquire(ctx, admissionIntent, ticket)
	if err == nil {
		err = b.runtime.beginDispatch(ctx)
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
		if workDeferred(err) != nil {
			return nil, err
		}
		if policyErr := requestBackendPolicy(ctx, b.inner); policyErr != nil {
			return nil, policyErr
		}
		return nil, err
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
		if err := session.record(handoffCtx, intent.ID, record); err != nil {
			return nil, session.fail(errors.Join(sentErr, err))
		}
		var uncertain interface{ Uncertain() bool }
		if errors.As(sentErr, &uncertain) && uncertain.Uncertain() {
			return nil, session.fail(sentErr)
		}
		if callErr == nil {
			session.pending = append(session.pending, intent.ID)
			session.completionCtx = handoffCtx
		}
	}
	if session != nil {
		if callErr != nil {
			// Legacy handlers can sleep before returning a retry. A failed call
			// has no parse buffer once its terminal accounting is durable.
			session.releaseResponse()
		} else {
			if err := session.acquireParse(ctx); err != nil {
				return nil, session.fail(err)
			}
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
