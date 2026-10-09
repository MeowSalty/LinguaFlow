package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
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
	count       int
	bytes       int64
	reservation bool
}

// CandidateWindow includes reservations, pending drafts and ready results.
// Network response buffers are bounded separately by completion slots and the
// decompressed response limit. Existing drafts may exceed a lowered window;
// production stays stopped until consumers make room.
type CandidateWindow struct {
	mu      sync.Mutex
	limits  CandidateLimits
	entries map[string]windowEntry
	count   int
	bytes   int64
	changed chan struct{}
}

func NewCandidateWindow(l CandidateLimits) *CandidateWindow {
	if l.Segments <= 0 || l.Bytes <= 0 || l.ItemBytes <= 0 {
		l = DefaultCandidateLimits()
	}
	return &CandidateWindow{limits: l, entries: map[string]windowEntry{}, changed: make(chan struct{})}
}
func (w *CandidateWindow) signal() { close(w.changed); w.changed = make(chan struct{}) }
func (w *CandidateWindow) Changed() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.changed
}
func (w *CandidateWindow) TryReserve(key string, n int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if existing, exists := w.entries[key]; exists {
		return existing.reservation && existing.count == n
	}
	if key == "" || n < 1 || n > w.limits.Segments || int64(n) > math.MaxInt64/w.limits.ItemBytes {
		return false
	}
	bytes := int64(n) * w.limits.ItemBytes
	if n > w.limits.Segments-w.count || bytes > w.limits.Bytes-w.bytes {
		if w.count != 0 || n != 1 {
			return false
		}
	}
	w.entries[key] = windowEntry{count: n, bytes: bytes, reservation: true}
	w.count += n
	w.bytes += bytes
	return true
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
	w.entries[to] = windowEntry{count: 1, bytes: bytes}
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
	w.mu.Lock()
	defer w.mu.Unlock()
	if key == "" || bytes > w.limits.ItemBytes || bytes < 0 {
		return fmt.Errorf("saved candidate exceeds current recovery hard limit: %w", ErrCandidateCapacity)
	}
	if old, ok := w.entries[key]; ok {
		if old.reservation || old.bytes != bytes {
			return fmt.Errorf("saved candidate identity already has different capacity")
		}
		return nil
	}
	if bytes > math.MaxInt64-w.bytes || w.count == math.MaxInt {
		return ErrCandidateCapacity
	}
	w.entries[key] = windowEntry{count: 1, bytes: bytes}
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
	Admission      *backend.RequestAdmission
	Window         *CandidateWindow
	Gate           *PauseGate
	ResourceID     int
	parse          chan struct{}
	save           chan struct{}
	saveTimeout    time.Duration
	saveRetryDelay time.Duration
}

func NewExecutionRuntime(admission *backend.RequestAdmission, limits CandidateLimits, gate *PauseGate) *ExecutionRuntime {
	if gate == nil {
		gate = NewPauseGate()
	}
	return &ExecutionRuntime{Admission: admission, Window: NewCandidateWindow(limits), Gate: gate, parse: make(chan struct{}, 2), save: make(chan struct{}, 1), saveTimeout: 30 * time.Second, saveRetryDelay: 100 * time.Millisecond}
}

// Close is called by the Job/call owner after every resource has joined.
func (r *ExecutionRuntime) Close() {
	if r != nil && r.Admission != nil {
		r.Admission.Close()
	}
}

type executionRuntimeKey struct{}

func WithExecutionRuntime(ctx context.Context, r *ExecutionRuntime) context.Context {
	return context.WithValue(ctx, executionRuntimeKey{}, r)
}
func ExecutionRuntimeFromContext(ctx context.Context) *ExecutionRuntime {
	r, _ := ctx.Value(executionRuntimeKey{}).(*ExecutionRuntime)
	return r
}
func (r *ExecutionRuntime) Save(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, r.saveTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return &StorageError{Err: err}
	}
	select {
	case r.save <- struct{}{}:
	case <-ctx.Done():
		return &StorageError{Err: ctx.Err()}
	}
	defer func() { <-r.save }()
	// Storage errors stay on the local path; they never re-enter model retry.
	for {
		if err := ctx.Err(); err != nil {
			return &StorageError{Err: err}
		}
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if permanentStorageError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &StorageError{Err: err}
		}
		timer := time.NewTimer(r.saveRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return &StorageError{Err: errors.Join(err, ctx.Err())}
		case <-timer.C:
		}
	}
}

// StorageError is permanent with respect to model retries. Save already retried
// transient local failures; returning this error must abort the execution run.
type StorageError struct{ Err error }

func (e *StorageError) Error() string { return fmt.Sprintf("candidate persistence failed: %v", e.Err) }
func (e *StorageError) Unwrap() error { return e.Err }
func (*StorageError) Permanent() bool { return true }

type permanentWorkError interface{ Permanent() bool }

func permanentStorageError(err error) bool {
	var p permanentWorkError
	return errors.As(err, &p) && p.Permanent()
}

type requestSession struct {
	runtime       *ExecutionRuntime
	store         RoundStore
	reporter      progress.Reporter
	intent        RequestIntent
	permits       []*backend.RequestPermit
	lastID        string
	calls         int
	err           error
	parsing       bool
	pending       []string
	completionCtx context.Context
}
type requestSessionKey struct{}

func (s *requestSession) finish() error {
	defer s.releaseResponse()
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

func (s *requestSession) record(ctx context.Context, id string, record RequestRecord) error {
	err := s.runtime.Save(ctx, func(saveCtx context.Context) error { return s.store.Record(saveCtx, id, record) })
	if err == nil {
		progress.NotifyWorkState(s.reporter)
	}
	return err
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
	if s.err == nil {
		s.err = err
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
	if err := requestBackendPolicy(ctx, b.inner); err != nil {
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
	if session != nil && session.store != nil {
		if err := session.runtime.Save(ctx, func(saveCtx context.Context) error { return session.store.Reserve(saveCtx, intent) }); err != nil {
			if errors.Is(err, backend.ErrDispatchPaused) {
				// The durable pause intent may win before the in-memory callback.
				// No reservation succeeded, so this is a dispatch stop, not a failed
				// response save or a debit eligible for refund.
				if b.runtime.Gate != nil {
					b.runtime.Gate.Pause()
				}
				return nil, backend.ErrDispatchPaused
			}
			return nil, session.fail(err)
		}
	}
	permit, err := b.runtime.Admission.Acquire(ctx, backend.RequestAdmissionIntent{RoundIndex: intent.RoundIndex, ResourceID: intent.ResourceID, BackendID: b.id, Stage: intent.Stage}, b.runtime.Gate)
	if err != nil {
		// Acquire joins any racing grant before returning an error; this caller
		// never dispatched. Refund even when pause/cancellation ended the wait.
		if session != nil && session.store != nil {
			if aborter, ok := session.store.(RequestAborter); ok {
				if abortErr := session.runtime.Save(context.WithoutCancel(ctx), func(saveCtx context.Context) error {
					return aborter.AbortRequest(saveCtx, intent.ID)
				}); abortErr != nil {
					return nil, session.fail(errors.Join(err, abortErr))
				}
				progress.NotifyWorkState(session.reporter)
			}
		}
		if policyErr := requestBackendPolicy(ctx, b.inner); policyErr != nil {
			return nil, policyErr
		}
		return nil, err
	}
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
		go func() {
			sentDone <- session.record(context.WithoutCancel(ctx), intent.ID, RequestRecord{State: "sent"})
		}()
	}
	resp, callErr := b.inner.Translate(backend.WithAdmittedRequest(ctx), req)
	duration := time.Since(start)
	permit.RequestDone()
	if session != nil && session.store != nil {
		// Join the intermediate fact first. Received records usage but retains a
		// saving state until reliable handoff/confirmation calls session.finish.
		sentErr := <-sentDone
		record := RequestRecord{State: "received", Duration: duration, UsageKnown: resp != nil}
		if resp != nil {
			record.Usage = resp.Usage
		}
		if callErr != nil {
			record.State = "failed"
			record.Error = callErr.Error()
		}
		if err := session.record(context.WithoutCancel(ctx), intent.ID, record); err != nil {
			return nil, session.fail(errors.Join(sentErr, err))
		}
		if callErr == nil {
			session.pending = append(session.pending, intent.ID)
			session.completionCtx = context.WithoutCancel(ctx)
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
