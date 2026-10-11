package backend

import (
	"context"
	"reflect"
	"time"
)

type admissionRequest struct {
	ctx            context.Context
	intent         RequestAdmissionIntent
	gate           DispatchGate
	result         chan admissionResult
	rpmWaitStarted time.Time
}

type admissionResult struct {
	permit *RequestPermit
	err    error
}

type admissionQueueGroup struct {
	stage                    RequestStage
	round, resource, backend int
}

func (a *RequestAdmission) acquireFair(ctx context.Context, intent RequestAdmissionIntent, gate DispatchGate) (*RequestPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.queueOnce.Do(func() { go a.runAdmissionQueue() })
	request := &admissionRequest{ctx: ctx, intent: intent, gate: gate, result: make(chan admissionResult, 1)}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.stopped:
		return nil, ErrAdmissionClosed
	case a.queue <- request:
	}
	select {
	case result := <-request.result:
		return result.permit, result.err
	case <-ctx.Done():
		// The dispatcher may have granted a permit immediately before cancellation.
		// Join its decision and return all capacity before reporting cancellation.
		select {
		case a.queueWake <- struct{}{}:
		default:
		}
		result := <-request.result
		result.permit.Release()
		return nil, ctx.Err()
	}
}

// runAdmissionQueue owns ordering, not persistence or execution. A ready caller
// waits on its result channel and executes immediately after receiving a permit.
// Signals are deduplicated and timers are rebuilt after every admission decision.
func (a *RequestAdmission) runAdmissionQueue() {
	var pending []*admissionRequest
	lastStage := make(map[int]RequestStage)
	accept := func(request *admissionRequest) {
		pending = append(pending, request)
		a.queued.Add(1)
	}
	finish := func(request *admissionRequest, result admissionResult) {
		if !request.rpmWaitStarted.IsZero() && a.pool != nil {
			a.pool.mu.Lock()
			a.pool.metrics.Waiters--
			a.pool.metrics.WaitDurationSecondsCount++
			a.pool.metrics.WaitDurationSecondsSum += time.Since(request.rpmWaitStarted).Seconds()
			if result.err != nil {
				a.pool.metrics.WaitCancelledTotal++
			}
			a.pool.mu.Unlock()
		}
		a.queued.Add(-1)
		request.result <- result
	}
	for {
		// Include all already-waiting callers before choosing the next group.
		for draining := true; draining; {
			select {
			case request := <-a.queue:
				accept(request)
			default:
				draining = false
			}
		}
		select {
		case <-a.stopped:
			for _, request := range pending {
				finish(request, admissionResult{err: ErrAdmissionClosed})
			}
			return
		default:
		}

		progressed := false
		var waits []AdmissionWait
		visited := make(map[*admissionRequest]bool, len(pending))
		visitedGroups := make(map[admissionQueueGroup]bool, len(pending))
		turn := &admissionTurn{}
		try := func(request *admissionRequest) bool {
			group := admissionQueueGroup{stage: request.intent.Stage, round: request.intent.RoundIndex, resource: request.intent.ResourceID, backend: request.intent.BackendID}
			if visited[request] || visitedGroups[group] {
				return false
			}
			visited[request] = true
			visitedGroups[group] = true
			permit, wait, err := a.tryAdmit(request.ctx, request.intent, request.gate, turn)
			// Releasing capacity midway through a scan must restart at the next
			// fair group; a later sibling must not overtake its blocked head.
			if err == errAdmissionTurnChanged {
				return true
			}
			if permit == nil && err == nil {
				if wait.Reason == "backend_rpm" && request.rpmWaitStarted.IsZero() && a.pool != nil {
					request.rpmWaitStarted = time.Now()
					a.pool.mu.Lock()
					a.pool.metrics.Waiters++
					a.pool.mu.Unlock()
				}
				waits = append(waits, wait)
				return false
			}
			pending = removeAdmissionRequest(pending, request, permit != nil)
			if permit != nil {
				lastStage[request.intent.BackendID] = request.intent.Stage
			}
			finish(request, admissionResult{permit: permit, err: err})
			return true
		}
		for _, request := range pending {
			// Stage preference applies only to this backend. A cooling opposite
			// stage is skipped, and never blocks this stage or unrelated backends.
			if lastStage[request.intent.BackendID] == request.intent.Stage {
				for _, other := range pending {
					if other.intent.BackendID == request.intent.BackendID && other.intent.Stage != request.intent.Stage {
						if try(other) {
							progressed = true
							break
						}
					}
				}
				if progressed {
					break
				}
			}
			if try(request) {
				progressed = true
				break
			}
		}
		if progressed {
			continue
		}

		cases := []reflect.SelectCase{
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a.queue)},
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a.stopped)},
			{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(a.queueWake)},
		}
		seen := make(map[<-chan struct{}]bool)
		var next time.Time
		for _, wait := range waits {
			for _, changed := range []<-chan struct{}{wait.CapacityChanged, wait.PolicyChanged, wait.PauseRequested} {
				if changed != nil && !seen[changed] {
					seen[changed] = true
					cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(changed)})
				}
			}
			if !wait.AvailableAt.IsZero() && (next.IsZero() || wait.AvailableAt.Before(next)) {
				next = wait.AvailableAt
			}
		}
		var timer *time.Timer
		if !next.IsZero() {
			timer = time.NewTimer(max(time.Until(next), time.Nanosecond))
			cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(timer.C)})
		}
		chosen, value, _ := reflect.Select(cases)
		if timer != nil {
			timer.Stop()
		}
		if chosen == 0 {
			accept(value.Interface().(*admissionRequest))
		}
	}
}

// A dispatched group moves behind other groups while retaining FIFO order
// internally. Error/cancellation removal does not consume its fairness turn.
func removeAdmissionRequest(pending []*admissionRequest, selected *admissionRequest, rotate bool) []*admissionRequest {
	out := make([]*admissionRequest, 0, len(pending)-1)
	var sameGroup []*admissionRequest
	for _, request := range pending {
		if request == selected {
			continue
		}
		if rotate && request.intent.Stage == selected.intent.Stage && request.intent.RoundIndex == selected.intent.RoundIndex && request.intent.ResourceID == selected.intent.ResourceID {
			sameGroup = append(sameGroup, request)
		} else {
			out = append(out, request)
		}
	}
	return append(out, sameGroup...)
}
