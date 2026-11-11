package backend

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// RequestStage selects a frozen request budget, independently of the backend.
type RequestStage string

const (
	RequestStageMain      RequestStage = "main"
	RequestStageAlignment RequestStage = "alignment"
)

var (
	ErrDispatchPaused  = errors.New("request dispatch is paused")
	ErrAdmissionClosed = errors.New("request admission is closed")
)

// DispatchGate linearizes closing dispatch with the complete admission decision.
// The callback must be short and must not block, perform I/O, or call the gate.
type DispatchGate interface {
	TryStartDispatch(admit func() bool) bool
	Done() <-chan struct{}
	ReleaseInflight()
}

// RequestAdmissionConfig belongs to one Job or one synchronous engine call.
// MainConcurrency is shared across that round's resources. AlignmentConcurrency
// is shared across every resource and round in this scope.
type RequestAdmissionConfig struct {
	MainConcurrency      map[int]int
	AlignmentConcurrency int
}

type RequestAdmissionIntent struct {
	RequestID    string
	CandidateIDs []string
	InputBytes   int64
	RoundIndex   int
	ResourceID   int
	BackendID    int
	Stage        RequestStage
}

// AdmissionWait describes a currently unavailable permit. A coordinator can skip
// this work and dispatch another ready key. Notifications only prompt a fresh
// TryAdmit; they never authorize dispatch using an obsolete policy or timer.
type AdmissionWait struct {
	Reason          string
	AvailableAt     time.Time
	CapacityChanged <-chan struct{}
	PolicyChanged   <-chan struct{}
	PauseRequested  <-chan struct{}
}

// RequestAdmission owns request and pending-result capacity. Callers reserve
// candidate space and durable attempts first, and must have a worker ready to
// execute immediately when a permit is granted. Admission never queues permits.
// The lock order is gate -> admission -> limiter pool; release never reverses it.
type RequestAdmission struct {
	root                                     *resourcebudget.RootBudget
	rootScope                                resourcebudget.Scope
	rootID                                   string
	storageCredits                           map[string]int64
	storageRepresentationBound               func(int64) (int64, error)
	candidateReadAllocationBound             func(int64) (int64, error)
	candidateReads                           map[string]*candidateReadCredit
	rootMetadataBase                         int64
	requestMetadata                          map[string]*requestMetadataCredit
	prepareOwners, buildWorkers, descriptors int64
	mainPriority                             bool
	priorityResource, priorityRound          int
	budget                                   *pipelineBudget
	mu                                       sync.Mutex
	pool                                     *LimiterPool
	config                                   RequestAdmissionConfig
	main                                     map[int]int
	mainResults                              map[int]int
	auxResults                               int
	alignment                                int
	completions                              int
	closed                                   bool
	changed                                  chan struct{}
	version                                  uint64
	queueOnce                                sync.Once
	queue                                    chan *admissionRequest
	queueWake                                chan struct{}
	stopped                                  chan struct{}
	queued                                   atomic.Int64
}

func NewRequestAdmission(pool *LimiterPool, config RequestAdmissionConfig) (*RequestAdmission, error) {
	if config.AlignmentConcurrency < 0 {
		return nil, errors.New("request admission capacities must not be negative")
	}
	limits := make(map[int]int, len(config.MainConcurrency))
	for round, capacity := range config.MainConcurrency {
		if round < 0 || capacity <= 0 {
			return nil, fmt.Errorf("invalid main request capacity for round %d", round)
		}
		limits[round] = capacity
	}
	config.MainConcurrency = limits
	return &RequestAdmission{
		pool: pool, config: config, main: make(map[int]int), mainResults: make(map[int]int), changed: make(chan struct{}),
		queue: make(chan *admissionRequest), queueWake: make(chan struct{}, 1), stopped: make(chan struct{}),
	}, nil
}

// TryAdmit is nonblocking. A nil permit and nil error means capacity/RPM is not
// currently available. ErrDispatchPaused means the gate closed before admission;
// already-granted permits remain valid for graceful draining.
func (a *RequestAdmission) TryAdmit(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate) (*RequestPermit, AdmissionWait, error) {
	return a.tryAdmit(ctx, intent, gate, nil)
}

// ReadyHint inspects capacity and current RPM without consuming any budget.
// Acquire always performs the authoritative joint decision afterwards.
func (a *RequestAdmission) ReadyHint(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate) (AdmissionWait, error) {
	var w AdmissionWait
	if err := ctx.Err(); err != nil {
		return w, err
	}
	if gate != nil {
		w.PauseRequested = gate.Done()
		select {
		case <-w.PauseRequested:
			return w, ErrDispatchPaused
		default:
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	w.CapacityChanged = a.changed
	if a.closed {
		return w, ErrAdmissionClosed
	}
	_, ok := a.config.MainConcurrency[intent.RoundIndex]
	if !ok {
		return w, fmt.Errorf("request round %d has no frozen budget", intent.RoundIndex)
	}
	shared := intent.Stage == RequestStageMain
	if !shared && a.config.AlignmentConcurrency == 0 {
		return w, errors.New("alignment request has no frozen budget")
	}
	if intent.Stage != RequestStageMain && intent.Stage != RequestStageAlignment {
		return w, fmt.Errorf("unsupported stage %q", intent.Stage)
	}
	_, reason, err := a.capacityLocked(intent, false)
	w.Reason = reason
	if err != nil {
		return w, err
	}
	if a.pool != nil {
		a.pool.mu.Lock()
		defer a.pool.mu.Unlock()
		h, ok := a.pool.limiters[intent.BackendID]
		if a.pool.closed {
			return w, ErrLimiterClosed
		}
		if !ok || h.closed {
			return w, ErrLimiterMissing
		}
		w.PolicyChanged = h.changed
		if h.rpm > 0 {
			now := time.Now()
			tokens := min(float64(h.rpm), h.tokens+max(now.Sub(h.updated).Minutes(), 0)*float64(h.rpm))
			if tokens < 1 {
				w.AvailableAt = now.Add(max(time.Duration((1-tokens)/float64(h.rpm)*float64(time.Minute)), time.Nanosecond))
				if intent.Stage == RequestStageMain && a.mainPriority && a.priorityResource == intent.ResourceID && a.priorityRound == intent.RoundIndex {
					a.mainPriority = false
					a.notifyLocked()
					w.CapacityChanged = a.changed
				}
				if w.Reason == "" {
					w.Reason = "backend_rpm"
				}
			}
		}
	}
	return w, nil
}

var errAdmissionTurnChanged = errors.New("request admission turn changed")

type admissionTurn struct {
	initialized bool
	version     uint64
}

func (a *RequestAdmission) tryAdmit(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate, turn *admissionTurn) (*RequestPermit, AdmissionWait, error) {
	var wait AdmissionWait
	if err := ctx.Err(); err != nil {
		return nil, wait, err
	}
	if intent.Stage != RequestStageMain && intent.Stage != RequestStageAlignment {
		return nil, wait, fmt.Errorf("unsupported request stage %q", intent.Stage)
	}
	if _, ok := a.config.MainConcurrency[intent.RoundIndex]; !ok {
		return nil, wait, fmt.Errorf("request round %d has no frozen budget", intent.RoundIndex)
	}
	shared := intent.Stage == RequestStageMain
	if !shared && a.config.AlignmentConcurrency == 0 {
		return nil, wait, errors.New("alignment request has no frozen budget")
	}
	if gate != nil {
		wait.PauseRequested = gate.Done()
	}
	var admissionErr error
	var dispatchPolicy <-chan struct{}
	admit := func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		wait.CapacityChanged = a.changed
		if turn != nil {
			if turn.initialized && turn.version != a.version {
				admissionErr = errAdmissionTurnChanged
				return false
			}
			turn.initialized = true
			turn.version = a.version
		}
		if admissionErr = ctx.Err(); admissionErr != nil {
			return false
		}
		if a.closed {
			admissionErr = ErrAdmissionClosed
			return false
		}
		lane, reason, capacityErr := a.capacityLocked(intent, true)
		wait.Reason, admissionErr = reason, capacityErr
		if capacityErr != nil || reason != "" {
			return false
		}
		if a.pool != nil {
			a.pool.mu.Lock()
			defer a.pool.mu.Unlock()
			h, ok := a.pool.limiters[intent.BackendID]
			if ok {
				dispatchPolicy = h.changed
			}
			if a.pool.closed {
				admissionErr = ErrLimiterClosed
				return false
			}
			if !ok || h.closed {
				admissionErr = ErrLimiterMissing
				return false
			}
			if h.rpm > 0 {
				now := time.Now()
				tokens := min(float64(h.rpm), h.tokens+max(now.Sub(h.updated).Minutes(), 0)*float64(h.rpm))
				if tokens < 1 {
					wait.Reason = "backend_rpm"
					wait.PolicyChanged = h.changed
					wait.AvailableAt = now.Add(max(time.Duration((1-tokens)/float64(h.rpm)*float64(time.Minute)), time.Nanosecond))
					return false
				}
			}
			if err := a.reserveRootDispatchLocked(intent, lane); err != nil {
				err = a.classifyRootCapacityLocked(err)
				if aggregateCapacity(err) {
					wait.Reason = "aggregate_capacity"
					wait.CapacityChanged = a.root.Changed()
				} else {
					admissionErr = err
				}
				return false
			}
			var admitted bool
			admitted, wait.AvailableAt, wait.PolicyChanged, admissionErr = a.pool.tryTakeLocked(intent.BackendID, time.Now())
			if !admitted {
				wait.Reason = "backend_rpm"
				return false
			}
		} else if err := a.reserveRootDispatchLocked(intent, lane); err != nil {
			err = a.classifyRootCapacityLocked(err)
			if aggregateCapacity(err) {
				wait.Reason = "aggregate_capacity"
				wait.CapacityChanged = a.root.Changed()
			} else {
				admissionErr = err
			}
			return false
		}
		if shared {
			a.mainPriority = false
			a.main[intent.RoundIndex]++
			a.mainResults[intent.RoundIndex]++
		} else {
			a.alignment++
			a.auxResults++
		}
		if a.budget != nil {
			a.budget.applyGrant(intent, lane)
		}
		a.completions++
		a.version++
		return true
	}
	started := false
	if gate == nil {
		started = admit()
	} else {
		started = gate.TryStartDispatch(admit)
	}
	if !started {
		if admissionErr != nil {
			return nil, wait, admissionErr
		}
		if gate != nil {
			select {
			case <-gate.Done():
				wait.Reason = "paused"
				return nil, wait, ErrDispatchPaused
			default:
			}
		}
		return nil, wait, nil
	}
	permit := &RequestPermit{admission: a, intent: intent, shared: shared, gate: gate, policy: dispatchPolicy}
	return permit, AdmissionWait{}, nil
}

func (a *RequestAdmission) YieldMainPriority(resource, round int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mainPriority && a.priorityResource == resource && a.priorityRound == round {
		a.mainPriority = false
		a.notifyLocked()
	}
}

// WaitMainTurn protects the first retained complete batch before input build.
// It consumes no member/response credit. Do not signal here: the failed probe's
// own notification is not a new drain opportunity. A real consumer release wakes
// the waiter; grant, yielding or leaving the scope releases the turn.
func (a *RequestAdmission) WaitMainTurn(resource, round int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.mainPriority {
		a.mainPriority = true
		a.priorityResource, a.priorityRound = resource, round
	}
}

// Acquire queues a prepared request through one fair dispatcher. Requests rotate
// across (round, resource) groups and alternate stages when both can use the same
// backend. Blocked work never prevents another backend or budget from dispatch.
func (a *RequestAdmission) Acquire(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate) (*RequestPermit, error) {
	return a.acquireFair(ctx, intent, gate)
}

func (a *RequestAdmission) notifyLocked() {
	if a.root != nil {
		_ = a.syncRootLocked(0)
	}
	a.version++
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *RequestAdmission) Changed() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root != nil {
		return a.root.Changed()
	}
	return a.changed
}
func (a *RequestAdmission) PolicyChanged() <-chan struct{} {
	if a.pool == nil {
		return nil
	}
	return a.pool.Changed()
}

// Close prevents new dispatch and wakes waiters. Existing permits must still be
// released by their owner after response handling has joined.
func (a *RequestAdmission) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		_ = a.detachRootLocked()
		a.closed = true
		close(a.stopped)
		a.notifyLocked()
	}
}

type RequestAdmissionSnapshot struct {
	MainInflight      map[int]int
	AlignmentInflight int
	PendingResults    int
	QueuedRequests    int64
}

func (a *RequestAdmission) Snapshot() RequestAdmissionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	main := make(map[int]int, len(a.main))
	for round, n := range a.main {
		main[round] = n
	}
	return RequestAdmissionSnapshot{MainInflight: main, AlignmentInflight: a.alignment, PendingResults: a.completions, QueuedRequests: a.queued.Load()}
}

// RequestPermit has two lifetimes: RequestDone releases network capacity, while
// Release returns the completion slot after parsing and reliable saving finish.
// Both methods are idempotent and safe to call from cleanup paths.
type RequestPermit struct {
	policy         <-chan struct{}
	admission      *RequestAdmission
	intent         RequestAdmissionIntent
	shared         bool
	gate           DispatchGate
	requestOnce    sync.Once
	completionOnce sync.Once
}

var ErrStalePermit = errors.New("request dispatch policy changed before send")

// StartDispatch is the final policy linearization point. No construction or
// persistence may intervene between this check and the external invocation.
func (p *RequestPermit) StartDispatch(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil {
		return ErrAdmissionClosed
	}
	if p.gate != nil {
		select {
		case <-p.gate.Done():
			return ErrDispatchPaused
		default:
		}
	}
	if p.policy != nil {
		select {
		case <-p.policy:
			return ErrStalePermit
		default:
		}
	}
	return nil
}

func (p *RequestPermit) RequestDone() {
	if p == nil {
		return
	}
	p.requestOnce.Do(func() {
		a := p.admission
		a.mu.Lock()
		if p.shared {
			a.main[p.intent.RoundIndex]--
		} else {
			a.alignment--
		}
		a.notifyLocked()
		a.mu.Unlock()
		if p.gate != nil {
			p.gate.ReleaseInflight()
		}
	})
}

func (p *RequestPermit) Release() {
	if p == nil {
		return
	}
	p.RequestDone()
	p.completionOnce.Do(func() {
		a := p.admission
		a.mu.Lock()
		a.completions--
		if p.shared {
			a.mainResults[p.intent.RoundIndex]--
		} else {
			a.auxResults--
		}
		a.releaseRequestBudgetLocked(p.intent)
		a.notifyLocked()
		a.mu.Unlock()
	})
}
