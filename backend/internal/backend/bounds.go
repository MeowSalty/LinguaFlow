package backend

import (
	"errors"
	"math"
)

// Each permitted node allocation includes its record, backing capacity and
// alignment. JSON codec arenas use fixed records and exact-capacity slices;
// maps charge 1024 bytes per entry (including buckets, overflow and pointers).
// Consumers must charge actual capacity, never merely the used length.
const MaxNodeAllocationBytes int64 = 1024
const MaxBuildDepth = 64

var ErrBuildCapacity = errors.New("bounded construction capacity exceeded")

type BuildCapacityError struct{ Resource string }

func (e *BuildCapacityError) Error() string {
	return ErrBuildCapacity.Error() + ": " + e.Resource
}
func (e *BuildCapacityError) Unwrap() error { return ErrBuildCapacity }
func (*BuildCapacityError) Permanent() bool { return true }

// BuildBounds is a short-lived CPU allocation budget, not a durable ledger.
// Charges are cumulative for the build: reclaimed allocations are not refunded.
type BuildBounds struct {
	textLimit, nodeLimit, text, nodes, objectBytes int64
	depth                                          int
	encodeNodes                                    int64
	outputBytes, stringLimit                       int64
	fieldCache                                     *decoderFieldCache
	regexpReserved                                 bool
	formatReserved                                 bool
}

// ReserveFormatWorkspace includes an active <=64KiB fmt printer cache and its
// fixed state once per short build. It carries no business credit or owner.
func (b *BuildBounds) ReserveFormatWorkspace() error {
	if b.formatReserved {
		return nil
	}
	if err := b.ReserveNodes(65, MaxNodeAllocationBytes); err != nil {
		return err
	}
	b.formatReserved = true
	return nil
}

type BuildSnapshot struct {
	TextBytes, Nodes, ObjectBytes, TextLimit, NodeLimit int64
	Depth                                               int
}

func NewBuildBounds(outputBytes, inputBytes int64) (*BuildBounds, error) {
	if outputBytes < 0 || inputBytes < 0 || outputBytes > math.MaxInt64-inputBytes || outputBytes+inputBytes > math.MaxInt64/8 {
		return nil, &BuildCapacityError{"budget arithmetic"}
	}
	n := outputBytes + inputBytes
	return &BuildBounds{textLimit: 8 * n, nodeLimit: min(int64(65536), 1+n/8), outputBytes: outputBytes, stringLimit: math.MaxInt64}, nil
}
func (b *BuildBounds) OutputLimit() int64 { return b.outputBytes }

func (b *BuildBounds) ReserveText(length, capacity int64) error {
	credit, err := textAllocationCredit(capacity)
	if err != nil || length < 0 || capacity < length || credit > b.textLimit-b.text {
		return &BuildCapacityError{"text backing capacity"}
	}
	b.text += credit
	return nil
}

// Go 1.26 small allocation size classes are <= the next power of two (minimum
// 16 here also covers tiny allocations). Eight extra bytes conservatively cover
// the malloc header of pointer-bearing objects. Large objects round to 8 KiB
// runtime pages. This is allocation credit, not just slice capacity or length.
func textAllocationCredit(capacity int64) (int64, error) {
	if capacity < 0 {
		return 0, &BuildCapacityError{"negative allocation"}
	}
	if capacity == 0 {
		return 0, nil
	}
	if capacity >= 512 {
		if capacity > math.MaxInt64-8 {
			return 0, &BuildCapacityError{"allocation arithmetic"}
		}
		capacity += 8
	}
	if capacity <= 32768 {
		n := int64(16)
		for n < capacity {
			n *= 2
		}
		return n, nil
	}
	if capacity > math.MaxInt64-8191 {
		return 0, &BuildCapacityError{"allocation arithmetic"}
	}
	return (capacity + 8191) / 8192 * 8192, nil
}

func (b *BuildBounds) ReserveNodes(count, unitBytes int64) error {
	if count < 0 || unitBytes < 0 || unitBytes > MaxNodeAllocationBytes || count > b.nodeLimit-b.nodes {
		return &BuildCapacityError{"node backing capacity"}
	}
	credit, err := textAllocationCredit(count * unitBytes) // <= 65536*1024 before rounding.
	if err != nil {
		return err
	}
	chargedNodes := max(count, (credit+MaxNodeAllocationBytes-1)/MaxNodeAllocationBytes)
	if chargedNodes > b.nodeLimit-b.nodes {
		return &BuildCapacityError{"node allocation rounding"}
	}
	b.nodes += chargedNodes
	b.objectBytes += credit
	return nil
}

func (b *BuildBounds) Enter() error {
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
	return BuildSnapshot{b.text, b.nodes, b.objectBytes, b.textLimit, b.nodeLimit, b.depth}
}

// BoundedWriter checks the future allocation before appending. Growing from c
// to 2c keeps at most two buffers live; cumulative backing charges are <4*limit.
type BoundedWriter struct {
	bounds *BuildBounds
	limit  int64
	data   []byte
}

func NewBoundedWriter(bounds *BuildBounds, maxBytes int64) *BoundedWriter {
	return &BoundedWriter{bounds: bounds, limit: maxBytes}
}

// NewFixedBoundedWriter allocates the measured capacity once, after credit.
func NewFixedBoundedWriter(bounds *BuildBounds, size int64) (*BoundedWriter, error) {
	if size < 0 || size > int64(math.MaxInt) {
		return nil, &BuildCapacityError{"fixed writer bytes"}
	}
	if err := bounds.ReserveText(size, size); err != nil {
		return nil, err
	}
	return &BoundedWriter{bounds: bounds, limit: size, data: make([]byte, 0, int(size))}, nil
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
		if err := w.bounds.ReserveText(int64(n), int64(capacity)); err != nil {
			return err
		}
		data := make([]byte, len(w.data), capacity)
		copy(data, w.data)
		w.data = data
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

// WriteString avoids io.WriteString's otherwise unaccounted []byte conversion.
func (w *BoundedWriter) WriteString(s string) (int, error) {
	if err := w.prepareWrite(len(s)); err != nil {
		return 0, err
	}
	w.data = append(w.data, s...)
	return len(s), nil
}
