package backend

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// RequestStage selects a frozen request budget, independently of the backend.
type RequestStage string

const (
	RequestStageMain      RequestStage = "main"
	RequestStageAlignment RequestStage = "alignment"
	DefaultMaxCompletions              = 16
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
// is shared across every resource and round in this scope. LegacyRoundShared
// instead charges both stages to the originating round's main budget.
type RequestAdmissionConfig struct {
	MainConcurrency      map[int]int
	AlignmentConcurrency int
	LegacyRoundShared    bool
	MaxCompletions       int
}

type RequestAdmissionIntent struct {
	RoundIndex int
	ResourceID int
	BackendID  int
	Stage      RequestStage
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
	mu          sync.Mutex
	pool        *LimiterPool
	config      RequestAdmissionConfig
	main        map[int]int
	alignment   int
	completions int
	closed      bool
	changed     chan struct{}
	version     uint64
	queueOnce   sync.Once
	queue       chan *admissionRequest
	queueWake   chan struct{}
	stopped     chan struct{}
	queued      atomic.Int64
}

func NewRequestAdmission(pool *LimiterPool, config RequestAdmissionConfig) (*RequestAdmission, error) {
	if config.AlignmentConcurrency < 0 || config.MaxCompletions < 0 {
		return nil, errors.New("request admission capacities must not be negative")
	}
	if config.MaxCompletions == 0 {
		config.MaxCompletions = DefaultMaxCompletions
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
		pool: pool, config: config, main: make(map[int]int), changed: make(chan struct{}),
		queue: make(chan *admissionRequest), queueWake: make(chan struct{}, 1), stopped: make(chan struct{}),
	}, nil
}

// TryAdmit is nonblocking. A nil permit and nil error means capacity/RPM is not
// currently available. ErrDispatchPaused means the gate closed before admission;
// already-granted permits remain valid for graceful draining.
func (a *RequestAdmission) TryAdmit(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate) (*RequestPermit, AdmissionWait, error) {
	return a.tryAdmit(ctx, intent, gate, nil)
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
	shared := intent.Stage == RequestStageMain || a.config.LegacyRoundShared
	if !shared && a.config.AlignmentConcurrency == 0 {
		return nil, wait, errors.New("alignment request has no frozen budget")
	}
	if gate != nil {
		wait.PauseRequested = gate.Done()
	}
	var admissionErr error
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
		if a.completions >= a.config.MaxCompletions {
			wait.Reason = "completion_capacity"
			return false
		}
		if shared && a.main[intent.RoundIndex] >= a.config.MainConcurrency[intent.RoundIndex] {
			wait.Reason = "main_concurrency"
			if a.config.LegacyRoundShared {
				wait.Reason = "legacy_round_concurrency"
			}
			return false
		}
		if !shared && a.alignment >= a.config.AlignmentConcurrency {
			wait.Reason = "alignment_concurrency"
			return false
		}
		if a.pool != nil {
			a.pool.mu.Lock()
			defer a.pool.mu.Unlock()
			var admitted bool
			admitted, wait.AvailableAt, wait.PolicyChanged, admissionErr = a.pool.tryTakeLocked(intent.BackendID, time.Now())
			if !admitted {
				wait.Reason = "backend_rpm"
				return false
			}
		}
		if shared {
			a.main[intent.RoundIndex]++
		} else {
			a.alignment++
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
	return &RequestPermit{admission: a, intent: intent, shared: shared, gate: gate}, AdmissionWait{}, nil
}

// Acquire queues a prepared request through one fair dispatcher. Requests rotate
// across (round, resource) groups and alternate stages when both can use the same
// backend. Blocked work never prevents another backend or budget from dispatch.
func (a *RequestAdmission) Acquire(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate) (*RequestPermit, error) {
	return a.acquireFair(ctx, intent, gate)
}

func (a *RequestAdmission) notifyLocked() {
	a.version++
	close(a.changed)
	a.changed = make(chan struct{})
}

// Close prevents new dispatch and wakes waiters. Existing permits must still be
// released by their owner after response handling has joined.
func (a *RequestAdmission) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
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
	admission      *RequestAdmission
	intent         RequestAdmissionIntent
	shared         bool
	gate           DispatchGate
	requestOnce    sync.Once
	completionOnce sync.Once
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
		a.notifyLocked()
		a.mu.Unlock()
	})
}
