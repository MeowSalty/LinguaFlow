package backend

import (
	"context"
	"io"
	"math"
	"strconv"
	"sync"
)

type providerAllocationKey struct{}
type providerAllocation struct {
	mu                            sync.Mutex
	bounds                        *BuildBounds
	output                        *OwnedAllocation
	live, peak, flow, paid, limit int64
	wireCredit                    int64
	rootReserve                   func(int64) error
	rootRelease                   func(int64)
	ownership                     providerResponseOwnership
	sourceAdmission               *RequestAdmission
	owner                         string
	closed                        bool
}

type ProviderAllocationLifecycleError struct {
	Owner                      string
	LiveBytes, RemainingCredit int64
}

func (e *ProviderAllocationLifecycleError) Error() string {
	return ErrBuildCapacity.Error() + ": provider lifetime owner=" + e.Owner + " live=" + strconv.FormatInt(e.LiveBytes, 10) + " remaining_r=" + strconv.FormatInt(e.RemainingCredit, 10)
}
func (*ProviderAllocationLifecycleError) Unwrap() error { return ErrBuildCapacity }

func (p *providerAllocation) reserve(bytes int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrBuildCapacity
	}
	if a := p.ownership.admission; a != nil {
		a.mu.Lock()
		paid, err := a.responseCreditLocked(p.ownership)
		wire, wireErr := a.responseWireCreditLocked(p.ownership)
		a.mu.Unlock()
		if err != nil || wireErr != nil || paid != p.paid || wire != p.wireCredit {
			return ErrPipelineCapacity
		}
	}
	next, err := CheckedAdd(p.live, bytes)
	if err != nil {
		return err
	}
	flow := p.flow
	if bytes > math.MaxInt64-flow {
		flow = math.MaxInt64
	} else {
		flow += bytes
	}
	if p.limit > 0 && next > p.limit {
		return &AllocationCapacityError{p.live, bytes, p.limit}
	}
	credit := p.paid + p.wireCredit
	delta := max(next-credit, 0) - max(p.live-credit, 0)
	if delta > 0 && p.rootReserve != nil {
		if err = p.rootReserve(delta); err != nil {
			return err
		}
	}
	p.live, p.peak, p.flow = next, max(p.peak, next), flow
	return nil
}
func (p *providerAllocation) release(bytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.live - bytes
	if next < 0 {
		panic("provider capacity released twice")
	}
	credit := p.paid + p.wireCredit
	delta := max(p.live-credit, 0) - max(next-credit, 0)
	p.live = next
	if delta > 0 && p.rootRelease != nil {
		p.rootRelease(delta)
	}
}

// WithProviderResponseAllocation shares actual explicit buffer/string capacity.
// R is already-paid response protocol credit, not a prediction of heap usage.
// Only the observed live capacity above R reaches the supplied Root callbacks.
func WithProviderResponseAllocation(ctx context.Context, responseBytes int64) (context.Context, error) {
	if current, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation); ok {
		ownership, _ := ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
		if ownership != current.ownership {
			policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
			if policy.source != nil {
				return nil, ErrPipelineCapacity
			}
			ctx = context.WithValue(ctx, providerAllocationKey{}, struct{}{})
		} else {
			current.mu.Lock()
			closed := current.closed
			var activeErr error
			if a := current.ownership.admission; a != nil {
				a.mu.Lock()
				paid, err := a.responseCreditLocked(current.ownership)
				wire, wireErr := a.responseWireCreditLocked(current.ownership)
				a.mu.Unlock()
				if err != nil || wireErr != nil || paid != current.paid || wire != current.wireCredit {
					activeErr = ErrPipelineCapacity
				}
			}
			current.mu.Unlock()
			if closed {
				return nil, ErrBuildCapacity
			}
			if activeErr != nil {
				return nil, activeErr
			}
			if current.bounds.OutputLimit() != responseBytes {
				return nil, ErrBuildCapacity
			}
			return providerAllocationContext(ctx, current), nil
		}
	}
	if responseBytes <= 0 {
		return nil, ErrBuildCapacity
	}
	b, err := NewBuildBounds(responseBytes, responseBytes, BuildAllocationLimit(ctx))
	if err != nil {
		return nil, err
	}
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	current := &providerAllocation{bounds: b, limit: policy.limit, rootReserve: policy.reserve, rootRelease: policy.release}
	current.ownership, _ = ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
	current.sourceAdmission, current.owner = BuildOwnership(ctx)
	if current.sourceAdmission != current.ownership.admission || !validBuildOwner(current.owner) {
		current.ownership = providerResponseOwnership{}
	}
	if a := current.ownership.admission; a != nil && validBuildOwner(current.owner) {
		a.mu.Lock()
		if credits, ok := ctx.Value(requestCreditsKey{}).(RequestCredits); ok {
			if err := a.bindProviderWireCreditLocked(current.ownership, credits.InputBytes); err != nil {
				a.mu.Unlock()
				return nil, err
			}
		}
		paid, err := a.responseCreditLocked(current.ownership)
		wire, wireErr := a.responseWireCreditLocked(current.ownership)
		if err != nil {
			a.mu.Unlock()
			return nil, err
		}
		if wireErr != nil {
			a.mu.Unlock()
			return nil, wireErr
		}
		if _, err = CheckedAdd(paid, wire); err != nil {
			a.mu.Unlock()
			return nil, err
		}
		current.paid = paid
		current.wireCredit = wire
		a.mu.Unlock()
	}
	ctx = providerAllocationContext(ctx, current)
	b.ctx, b.reserve, b.release, b.meterSource = ctx, current.reserve, current.release, current
	return ctx, nil
}

// CloseProviderResponseAllocation terminates a fully consumed response meter.
// Call before releasing its request permit. An outstanding borrower prevents
// closure; this operation never discards live capacity or refunds an owner.
func CloseProviderResponseAllocation(ctx context.Context) error {
	p, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	if !ok {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.live != 0 {
		return &ProviderAllocationLifecycleError{Owner: p.owner, LiveBytes: p.live, RemainingCredit: p.paid}
	}
	p.closed = true
	return nil
}

func providerAllocationContext(ctx context.Context, current *providerAllocation) context.Context {
	policy := buildAllocationPolicy{limit: current.limit, reserve: current.reserve, release: current.release, source: current}
	ctx = context.WithValue(ctx, buildAllocationKey{}, policy)
	ctx = WithBuildOwnership(ctx, current.sourceAdmission, current.owner)
	ctx = context.WithValue(ctx, providerResponseOwnershipKey{}, current.ownership)
	return context.WithValue(ctx, providerAllocationKey{}, current)
}

// ReleaseProviderResponseOutput follows clearing every returned Text borrower.
// Reader/event/selected-string owners release themselves at their lifetime end.
func ReleaseProviderResponseOutput(ctx context.Context) {
	if current, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation); ok {
		current.mu.Lock()
		owner := current.output
		current.output = nil
		current.mu.Unlock()
		owner.Release()
	}
}

func OwnProviderResponseAllocation(ctx context.Context, count, unitBytes int64) (*OwnedAllocation, error) {
	current, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	if !ok {
		return nil, ErrBuildCapacity
	}
	return current.bounds.OwnAllocation(count, unitBytes)
}
func ProviderResponseSnapshot(ctx context.Context) BuildSnapshot {
	if current, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation); ok {
		current.mu.Lock()
		defer current.mu.Unlock()
		return BuildSnapshot{LiveBytes: current.live, MeasuredRequiredBytes: current.peak, AllocationBytes: current.flow, AllocationLimit: current.limit}
	}
	return BuildSnapshot{}
}

// DiagnosticFinishReason returns static provider vocabulary so terminal errors
// cannot retain an arbitrary response-sized string after its build owner ends.
func DiagnosticFinishReason(reason string) string {
	// Return literal constants, not the borrowed caller string.
	for _, literal := range [...]string{"", "stop", "length", "content_filter", "tool_calls", "function_call", "end_turn", "max_tokens", "stop_sequence", "tool_use", "pause_turn", "refusal", "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "LANGUAGE", "OTHER", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_OTHER", "NO_IMAGE"} {
		if reason == literal {
			return literal
		}
	}
	return "unknown"
}

func providerBounds(ctx context.Context, limit int64) (*BuildBounds, error) {
	if current, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation); ok {
		b := current.bounds
		if limit <= 0 || limit > b.OutputLimit() {
			return nil, ErrBuildCapacity
		}
		b.ctx = providerAllocationContext(ctx, current)
		return b, nil
	}
	// Standalone calls check explicit capacity against the optional policy too.
	ctx, err := WithProviderResponseAllocation(ctx, limit)
	if err != nil {
		return nil, err
	}
	return ctx.Value(providerAllocationKey{}).(*providerAllocation).bounds, nil
}

func BorrowWireJSONContext(ctx context.Context, data []byte, limit int64) (WireJSONValue, error) {
	b, err := providerBounds(ctx, limit)
	if err != nil {
		return WireJSONValue{}, err
	}
	if int64(len(data)) > limit {
		return WireJSONValue{}, &ResponseLimitError{limit}
	}
	return BorrowWireJSON(b, data)
}

// ReadProviderBounded credits each actual buffer growth before reading/copying.
// Small bodies keep small backing; no future response multiplier is charged.
// Release follows consumption of every borrowed cursor, including on errors.
func ReadProviderBounded(ctx context.Context, r io.Reader, limit int64, trace *OwnedTrace) ([]byte, func(), error) {
	b, err := providerBounds(ctx, limit)
	if err != nil {
		return nil, func() {}, err
	}
	w := NewBoundedWriter(b, limit)
	release := func() { w.Release(); trace.Clear() }
	fail := func(err error) ([]byte, func(), error) { release(); return nil, func() {}, err }
	var scratch [4096]byte
	for {
		if err = ctx.Err(); err != nil {
			return fail(err)
		}
		remaining := limit - int64(len(w.data))
		want := min(remaining, int64(len(scratch)))
		if want == 0 {
			var probe [1]byte
			n, e := r.Read(probe[:])
			if n > 0 {
				return fail(&ResponseLimitError{limit})
			}
			if e == io.EOF {
				return w.Bytes(), release, nil
			}
			if e != nil {
				return fail(e)
			}
			continue
		}
		n, e := r.Read(scratch[:want])
		if n > 0 {
			if _, err = w.Write(scratch[:n]); err != nil {
				return fail(err)
			}
			trace.Set(int64(cap(w.data)))
		}
		if e == io.EOF {
			return w.Bytes(), release, nil
		}
		if e != nil {
			return fail(e)
		}
	}
}
