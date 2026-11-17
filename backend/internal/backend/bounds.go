package backend

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
)

const MaxBuildDepth = 64

var ErrBuildCapacity = errors.New("bounded construction capacity exceeded")

type BuildCapacityError struct{ Resource string }

func (e *BuildCapacityError) Error() string {
	return ErrBuildCapacity.Error() + ": " + e.Resource
}
func (e *BuildCapacityError) Unwrap() error { return ErrBuildCapacity }
func (*BuildCapacityError) Permanent() bool { return true }

// BuildBounds meters owned capacity in a short build. Unowned reservations are
// retained conservatively; only an allocation owner can release its backing.
// AllocationLimit is optional and independent of input/output byte policies.
type BuildBounds struct {
	capacityMu                                                sync.Mutex
	owned                                                     int64
	meterSource                                               *providerAllocation
	text, objectBytes, live, peak, allocated, allocationLimit int64
	inputBytes                                                int64
	depth                                                     int
	outputBytes, stringLimit                                  int64
	fieldCache                                                *decoderFieldCache
	decodeAllocations                                         *decodeAllocationScope
	ctx                                                       context.Context
	reserve                                                   func(int64) error
	release                                                   func(int64)
	retired                                                   bool
}

type BuildSnapshot struct {
	TextBytes, ObjectBytes, LiveBytes, MeasuredRequiredBytes, AllocationBytes, AllocationLimit int64
	Depth                                                                                      int
}

func NewBuildBounds(outputBytes, inputBytes int64, allocationLimit ...int64) (*BuildBounds, error) {
	if outputBytes < 0 || inputBytes < 0 || outputBytes > math.MaxInt64-inputBytes || len(allocationLimit) > 1 {
		return nil, &BuildCapacityError{"budget arithmetic"}
	}
	limit := int64(0)
	if len(allocationLimit) == 1 {
		limit = allocationLimit[0]
	}
	if limit < 0 {
		return nil, &BuildCapacityError{"allocation limit"}
	}
	return &BuildBounds{inputBytes: inputBytes, allocationLimit: limit, outputBytes: outputBytes, stringLimit: math.MaxInt64}, nil
}
func (b *BuildBounds) OutputLimit() int64 { return b.outputBytes }
func (b *BuildBounds) InputLimit() int64  { return b.inputBytes }

func (b *BuildBounds) ReserveText(length, capacity int64) error {
	credit, err := textAllocationCredit(capacity)
	if err != nil || length < 0 || capacity < length || capacity > int64(math.MaxInt) {
		return &BuildCapacityError{"text backing capacity"}
	}
	if err := b.reserveBytes(credit); err != nil {
		return err
	}
	b.text += credit
	return nil
}

// textAllocationCredit counts the known payload capacity. Runtime allocation
// classes, headers, pooling and heap placement are deliberately not predicted.
func textAllocationCredit(capacity int64) (int64, error) {
	if capacity < 0 || capacity > int64(math.MaxInt) {
		return 0, &BuildCapacityError{"negative allocation"}
	}
	return capacity, nil
}

// AllocationBytes checks a future contiguous backing allocation before make.
func AllocationBytes(count, unitBytes int64) (int64, error) {
	n, err := checkedAllocationProduct(count, unitBytes)
	if err != nil {
		return 0, &BuildCapacityError{"allocation arithmetic"}
	}
	if count > int64(math.MaxInt) || n > int64(math.MaxInt) {
		return 0, &BuildCapacityError{"addressable allocation"}
	}
	return textAllocationCredit(n)
}
func (b *BuildBounds) ReserveAllocation(count, unitBytes int64) error {
	credit, err := AllocationBytes(count, unitBytes)
	if err != nil {
		return err
	}
	if err := b.reserveBytes(credit); err != nil {
		return err
	}
	b.objectBytes += credit
	return nil
}

type AllocationCapacityError struct{ Used, Requested, Limit int64 }

func (e *AllocationCapacityError) Error() string {
	return "bounded construction capacity exceeded: allocation bytes used=" + strconv.FormatInt(e.Used, 10) + " requested=" + strconv.FormatInt(e.Requested, 10) + " limit=" + strconv.FormatInt(e.Limit, 10)
}
func (*AllocationCapacityError) Unwrap() error   { return ErrBuildCapacity }
func (*AllocationCapacityError) Permanent() bool { return true }
func (b *BuildBounds) reserveBytes(n int64) error {
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	return b.reserveBytesLocked(n)
}
func (b *BuildBounds) reserveBytesLocked(n int64) error {
	if b.retired {
		return &BuildCapacityError{"retired construction"}
	}
	if err := b.CheckContext(); err != nil {
		return err
	}
	if n == 0 {
		if b.meterSource != nil {
			return b.meterSource.reserve(0)
		}
		return nil
	}
	live, err := CheckedAdd(b.live, n)
	if err != nil {
		return err
	}
	flow := b.allocated
	if n > math.MaxInt64-flow {
		flow = math.MaxInt64
	} else {
		flow += n
	}
	if b.allocationLimit > 0 && live > b.allocationLimit {
		return &AllocationCapacityError{b.live, n, b.allocationLimit}
	}
	if b.reserve != nil {
		if err := b.reserve(n); err != nil {
			return err
		}
	}
	b.live, b.allocated, b.peak = live, flow, max(b.peak, live)
	return nil
}

// OwnedAllocation grants release rights to one particular backing allocation.
// Transfer invalidates the old handle. It never increases or resets the meter.
type OwnedAllocation struct {
	mu     sync.Mutex
	bounds *BuildBounds
	bytes  int64
}

func (b *BuildBounds) OwnAllocation(count, unitBytes int64) (*OwnedAllocation, error) {
	n, err := AllocationBytes(count, unitBytes)
	if err != nil {
		return nil, err
	}
	return b.ownBytes(n)
}
func (b *BuildBounds) ownBytes(n int64) (*OwnedAllocation, error) {
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if err := b.reserveBytesLocked(n); err != nil {
		return nil, err
	}
	b.owned += n
	return &OwnedAllocation{bounds: b, bytes: n}, nil
}

// RetainedAllocation gives ownership to already-counted, unowned live backing.
// It creates no new capacity. Callers identify actual retained output from this
// original build, after temporary borrowers have ended.
func (b *BuildBounds) RetainedAllocation(bytes int64) (*OwnedAllocation, error) {
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || bytes < 0 || bytes > b.live-b.owned {
		return nil, &AllocationOwnershipError{Requested: bytes, Live: b.live, Owned: b.owned, Retired: b.retired}
	}
	b.owned += bytes
	return &OwnedAllocation{bounds: b, bytes: bytes}, nil
}

type AllocationOwnershipError struct {
	Requested, Live, Owned int64
	Retired                bool
}

func (e *AllocationOwnershipError) Error() string {
	return ErrBuildCapacity.Error() + ": retained receipt requested=" + strconv.FormatInt(e.Requested, 10) + " live=" + strconv.FormatInt(e.Live, 10) + " owned=" + strconv.FormatInt(e.Owned, 10) + " retired=" + strconv.FormatBool(e.Retired)
}
func (*AllocationOwnershipError) Unwrap() error { return ErrBuildCapacity }
func (a *OwnedAllocation) Release() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bounds != nil {
		a.bounds.capacityMu.Lock()
		defer a.bounds.capacityMu.Unlock()
		if a.bounds.retired {
			a.bounds = nil
			return
		}
		a.bounds.live -= a.bytes
		a.bounds.owned -= a.bytes
		if a.bounds.release != nil {
			a.bounds.release(a.bytes)
		}
		a.bounds = nil
	}
}

// ReturnToBuild gives a receipt back to its original build's retained
// live capacity. No backing is freed and no callback is refunded. A later
// RetainedAllocation may move the field; otherwise CloseRetired ends its life.
func (a *OwnedAllocation) ReturnToBuild() error {
	if a == nil {
		return ErrBuildCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bounds
	if b == nil {
		return ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || a.bytes > b.owned {
		return ErrBuildCapacity
	}
	b.owned -= a.bytes
	a.bounds = nil
	return nil
}

// DetachBuild keeps this particular backing under the same actual callbacks
// while its encoder's temporary build retires. It performs no new reservation,
// release, or meter reset; only the receipt's lifetime becomes independent.
func (a *OwnedAllocation) DetachBuild() error {
	if a == nil {
		return ErrBuildCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bounds
	if b == nil {
		return ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || a.bytes > b.live || a.bytes > b.owned {
		return ErrBuildCapacity
	}
	b.live -= a.bytes
	b.owned -= a.bytes
	a.bounds = &BuildBounds{live: a.bytes, owned: a.bytes, peak: a.bytes, inputBytes: b.inputBytes, outputBytes: b.outputBytes, stringLimit: b.stringLimit, allocationLimit: b.allocationLimit, ctx: b.ctx, reserve: b.reserve, release: b.release, meterSource: b.meterSource}
	return nil
}

// DetachToBuild moves this exact backing to the next build stage under the
// same meter and callbacks. It consumes the receipt without reserving or
// releasing its capacity. The caller must clear the previous stage's other
// borrowers before permanently closing that build.
func (a *OwnedAllocation) DetachToBuild() (*BuildBounds, error) {
	if a == nil {
		return nil, ErrBuildCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bounds
	if b == nil {
		return nil, ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || a.bytes > b.live || a.bytes > b.owned {
		return nil, ErrBuildCapacity
	}
	b.live -= a.bytes
	b.owned -= a.bytes
	next := &BuildBounds{live: a.bytes, peak: a.bytes, inputBytes: b.inputBytes, outputBytes: b.outputBytes, stringLimit: b.stringLimit, allocationLimit: b.allocationLimit, ctx: b.ctx, reserve: b.reserve, release: b.release, meterSource: b.meterSource}
	a.bounds = nil
	return next, nil
}

// CloseRetired ends this build permanently. Callers must first clear every
// borrowed/output reference, or transfer each still-live backing to its next
// owner. This is an explicit lifetime boundary, never an in-progress reset.
// Diagnostic peak and flow remain available; allocation cannot restart.
func (b *BuildBounds) CloseRetired() {
	if b == nil {
		return
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired {
		return
	}
	bytes := b.live
	b.fieldCache = nil
	b.live = 0
	b.owned = 0
	b.retired = true
	if b.release != nil && bytes > 0 {
		b.release(bytes)
	}
}
func (a *OwnedAllocation) Transfer() *OwnedAllocation {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bounds == nil {
		return nil
	}
	a.bounds.capacityMu.Lock()
	defer a.bounds.capacityMu.Unlock()
	if a.bounds.retired {
		return nil
	}
	next := &OwnedAllocation{bounds: a.bounds, bytes: a.bytes}
	a.bounds = nil
	return next
}
func CheckedAdd(a, c int64) (int64, error) {
	if a < 0 || c < 0 || a > math.MaxInt64-c {
		return 0, &BuildCapacityError{"allocation arithmetic"}
	}
	return a + c, nil
}
func checkedAllocationProduct(a, c int64) (int64, error) {
	if a < 0 || c < 0 || a != 0 && c > math.MaxInt64/a {
		return 0, &BuildCapacityError{"allocation arithmetic"}
	}
	return a * c, nil
}

func (b *BuildBounds) Enter() error {
	if err := b.CheckContext(); err != nil {
		return err
	}
	if b.depth >= MaxBuildDepth {
		return &BuildCapacityError{"nesting depth"}
	}
	b.depth++
	return nil
}
func (b *BuildBounds) Leave() {
	if b.depth > 0 {
		b.depth--
	}
}
func (b *BuildBounds) Snapshot() BuildSnapshot {
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	return BuildSnapshot{b.text, b.objectBytes, b.live, b.peak, b.allocated, b.allocationLimit, b.depth}
}

// BoundedWriter checks the future allocation before appending. Growing from c
// to 2c keeps at most two buffers live; cumulative backing charges are <4*limit.
// An initialized writer owns its backing and must not be copied while in use.
type BoundedWriter struct {
	bounds     *BuildBounds
	limit      int64
	data       []byte
	allocation *OwnedAllocation
}

// NewBoundedWriter initializes the caller's writer slot. Only its separately
// owned byte backing needs an allocation owner.
func NewBoundedWriter(bounds *BuildBounds, maxBytes int64) BoundedWriter {
	return BoundedWriter{bounds: bounds, limit: maxBytes}
}

// NewFixedBoundedWriter allocates the measured capacity once, after credit.
func NewFixedBoundedWriter(bounds *BuildBounds, size int64) (BoundedWriter, error) {
	if size < 0 || size > int64(math.MaxInt) {
		return BoundedWriter{}, &BuildCapacityError{"fixed writer bytes"}
	}
	allocation, err := bounds.OwnAllocation(size, 1)
	if err != nil {
		return BoundedWriter{}, err
	}
	return BoundedWriter{bounds: bounds, limit: size, data: make([]byte, 0, int(size)), allocation: allocation}, nil
}

// boundedWriterLength checks the addressable size before adding lengths.
func boundedWriterLength(current, incoming int) (int, error) {
	if current < 0 || incoming < 0 || incoming > math.MaxInt-current {
		return 0, &BuildCapacityError{"addressable bytes"}
	}
	return current + incoming, nil
}

// boundedWriterCapacity grows within both the credited limit and int's range.
// required has already passed the length and limit checks in prepareWrite.
func boundedWriterCapacity(current, required int, limit int64) int {
	maximum := int(min(limit, int64(math.MaxInt)))
	capacity := maximum
	if current <= maximum/2 {
		capacity = current * 2
	}
	return min(maximum, max(required, 64, capacity))
}

func (w *BoundedWriter) prepareWrite(incoming int) error {
	if w.limit < 0 || int64(incoming) > w.limit-int64(len(w.data)) {
		return &BuildCapacityError{"encoded bytes"}
	}
	n, err := boundedWriterLength(len(w.data), incoming)
	if err != nil {
		return err
	}
	if n > cap(w.data) {
		capacity := boundedWriterCapacity(cap(w.data), n, w.limit)
		allocation, err := w.bounds.OwnAllocation(int64(capacity), 1)
		if err != nil {
			return err
		}
		data := make([]byte, len(w.data), capacity)
		copy(data, w.data)
		w.data = data
		w.allocation.Release()
		w.allocation = allocation
	}
	return nil
}

func (w *BoundedWriter) Write(p []byte) (int, error) {
	if err := w.prepareWrite(len(p)); err != nil {
		return 0, err
	}
	w.data = append(w.data, p...)
	return len(p), nil
}
func (w *BoundedWriter) Bytes() []byte { return w.data }

// Release drops this writer's storage and credit. Borrowers of Bytes must have
// stopped using it before the owner calls Release.
func (w *BoundedWriter) Release() { w.data = nil; w.allocation.Release(); w.allocation = nil }

// WriteString avoids io.WriteString's otherwise unaccounted []byte conversion.
func (w *BoundedWriter) WriteString(s string) (int, error) {
	if err := w.prepareWrite(len(s)); err != nil {
		return 0, err
	}
	w.data = append(w.data, s...)
	return len(s), nil
}
