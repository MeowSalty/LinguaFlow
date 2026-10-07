// Package diskspace coordinates remaining physical writes within one process.
// It is independent of the durable object quota ledger.
package diskspace

import (
	"context"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

type Observation struct {
	FilesystemID   string
	TotalBytes     uint64
	AvailableBytes uint64
	ObservedAt     time.Time
}
type Probe func(context.Context, string) (Observation, error)

// Threshold selects either an absolute reserve or a percentage of volume size.
type Threshold struct {
	Bytes   int64
	Percent float64
}

func DefaultThreshold() Threshold { return Threshold{Percent: 1} }

// ParseThreshold accepts decimal positive bytes or a decimal percentage. Empty
// values, signs, whitespace, exponents and units other than % are invalid.
func ParseThreshold(value string) (Threshold, error) {
	invalid := errors.New("disk threshold must be positive bytes or a percentage below 100")
	percent := strings.HasSuffix(value, "%")
	number := strings.TrimSuffix(value, "%")
	if number == "" {
		return Threshold{}, invalid
	}
	dots := 0
	for _, ch := range number {
		if percent && ch == '.' {
			dots++
			if dots <= 1 {
				continue
			}
		}
		if ch < '0' || ch > '9' {
			return Threshold{}, invalid
		}
	}
	var threshold Threshold
	if percent {
		parsed, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return Threshold{}, invalid
		}
		threshold.Percent = parsed
	} else {
		parsed, err := strconv.ParseInt(number, 10, 64)
		if err != nil {
			return Threshold{}, invalid
		}
		threshold.Bytes = parsed
	}
	if err := threshold.Validate(); err != nil {
		return Threshold{}, invalid
	}
	return threshold, nil
}
func (t Threshold) Validate() error {
	if t.Bytes < 0 || math.IsNaN(t.Percent) || math.IsInf(t.Percent, 0) || t.Percent < 0 || t.Percent >= 100 || (t.Bytes > 0) == (t.Percent > 0) {
		return errors.New("disk threshold requires positive bytes or a percentage below 100")
	}
	return nil
}
func (t Threshold) bytes(total uint64) uint64 {
	if t.Bytes > 0 {
		return uint64(t.Bytes)
	}
	return uint64(math.Ceil(float64(total) * t.Percent / 100))
}

type Coordinator struct {
	mu        sync.Mutex
	probe     Probe
	threshold Threshold
	held      map[string]uint64
}

func (c *Coordinator) ThresholdBytes(total uint64) uint64 { return c.threshold.bytes(total) }

func New(threshold Threshold, probe Probe) (*Coordinator, error) {
	if err := threshold.Validate(); err != nil {
		return nil, err
	}
	if probe == nil {
		probe = SystemProbe
	}
	return &Coordinator{probe: probe, threshold: threshold, held: make(map[string]uint64)}, nil
}
func (c *Coordinator) observe(ctx context.Context, path string) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	o, err := c.probe(ctx, path)
	if err != nil || o.FilesystemID == "" || o.TotalBytes == 0 || o.AvailableBytes > o.TotalBytes {
		return Observation{}, storage.ErrDiskSpaceUnknown
	}
	return o, nil
}
func (c *Coordinator) Observe(ctx context.Context, path string) (Observation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.observe(ctx, path)
}
func (c *Coordinator) check(o Observation, extra uint64) error {
	threshold := c.threshold.bytes(o.TotalBytes)
	held := c.held[o.FilesystemID]
	if threshold > o.AvailableBytes || held > o.AvailableBytes-threshold || extra > o.AvailableBytes-threshold-held {
		return storage.ErrDiskSpaceInsufficient
	}
	return nil
}
func (c *Coordinator) Check(ctx context.Context, path string, size int64) error {
	if size < 0 {
		return storage.ErrPayloadTooLarge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	o, err := c.observe(ctx, path)
	if err != nil {
		return err
	}
	return c.check(o, uint64(size))
}

// CheckPlan verifies an entire materialization peak without retaining a future
// reservation. Aliases on the same filesystem are added together, and existing
// unwritten reservations are counted once. Every actual write still reserves.
func (c *Coordinator) CheckPlan(ctx context.Context, plan map[string]int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	paths := make([]string, 0, len(plan))
	for path, size := range plan {
		if size < 0 {
			return storage.ErrPayloadTooLarge
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	observations := make(map[string]Observation)
	bytes := make(map[string]uint64)
	for _, path := range paths {
		o, err := c.observe(ctx, path)
		if err != nil {
			return err
		}
		if uint64(plan[path]) > math.MaxUint64-bytes[o.FilesystemID] {
			return storage.ErrPayloadTooLarge
		}
		bytes[o.FilesystemID] += uint64(plan[path])
		if previous, exists := observations[o.FilesystemID]; exists {
			// Probe values can change between paths; retain the conservative
			// observations rather than manufacturing an atomic OS snapshot.
			if previous.AvailableBytes < o.AvailableBytes {
				o.AvailableBytes = previous.AvailableBytes
			}
			if previous.TotalBytes > o.TotalBytes {
				o.TotalBytes = previous.TotalBytes
			}
		}
		observations[o.FilesystemID] = o
	}
	for id, o := range observations {
		if err := c.check(o, bytes[id]); err != nil {
			return err
		}
	}
	return nil
}

type Reservation struct {
	c          *Coordinator
	ctx        context.Context
	path       string
	filesystem string
	remaining  uint64
	released   bool
}

func (c *Coordinator) Reserve(ctx context.Context, path string, size int64) (*Reservation, error) {
	if size < 0 {
		return nil, storage.ErrPayloadTooLarge
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	o, err := c.observe(ctx, path)
	if err != nil {
		return nil, err
	}
	if err = c.check(o, uint64(size)); err != nil {
		return nil, err
	}
	c.held[o.FilesystemID] += uint64(size)
	return &Reservation{c: c, ctx: ctx, path: path, filesystem: o.FilesystemID, remaining: uint64(size)}, nil
}

// Release retires only unwritten bytes. Written bytes are already reflected by
// the filesystem probe, even if a partial product still awaits exact cleanup.
func (r *Reservation) Release() {
	if r == nil {
		return
	}
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if !r.released {
		r.c.held[r.filesystem] -= r.remaining
		r.remaining = 0
		r.released = true
	}
}
func (r *Reservation) Writer(destination io.Writer) io.Writer {
	return &writer{r: r, destination: destination}
}

// Seal finishes allocation while retaining the reservation's closed-write
// boundary: future nonempty writes fail the processing limit.
func (r *Reservation) Seal() {
	if r == nil {
		return
	}
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if !r.released {
		r.c.held[r.filesystem] -= r.remaining
		r.remaining = 0
	}
}

type writer struct {
	r           *Reservation
	destination io.Writer
}

func (w *writer) Write(p []byte) (int, error) {
	r := w.r
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if r.released {
		return 0, storage.ErrDiskSpaceUnknown
	}
	if uint64(len(p)) > r.remaining {
		return 0, storage.ErrPayloadTooLarge
	}
	o, err := r.c.observe(r.ctx, r.path)
	if err != nil {
		return 0, err
	}
	if o.FilesystemID != r.filesystem {
		return 0, storage.ErrDiskSpaceUnknown
	}
	if err = r.c.check(o, 0); err != nil {
		return 0, err
	}
	// Serialize physical allocation and reservation retirement with probes.
	n, err := w.destination.Write(p)
	if n < 0 || n > len(p) {
		return 0, io.ErrShortWrite
	}
	r.remaining -= uint64(n)
	r.c.held[r.filesystem] -= uint64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, Classify(err)
}

// Classify preserves non-disk errors for the caller's existing error handling.
func Classify(err error) error {
	if isSpaceError(err) {
		return storage.ErrDiskSpaceInsufficient
	}
	return err
}
