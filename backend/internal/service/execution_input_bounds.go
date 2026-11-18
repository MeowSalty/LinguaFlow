package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/glossaryentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

const DefaultExecutionInputBytes int64 = 64 << 20

// MaxJobDefinitionSegmentIDs bounds the total decoded segment IDs of one job
// definition across all of its resources. It is validated by an exact SQL
// array-length sum before any JSON decode: ~6MB of raw JSON and ~8MB of
// decoded []int at the cap, roughly 20x headroom over observed large jobs.
const MaxJobDefinitionSegmentIDs int64 = 1_000_000

// ExecutionDefinitionCount sizes real routing records and the possible
// resource/round matrix. Descriptor policy is applied by the existing Root.
func ExecutionDefinitionCount(resources, roundRows, rounds int64) (int64, error) {
	product, err := backend.CheckedProduct(resources, rounds)
	if err != nil {
		return 0, err
	}
	count := int64(1)
	for _, n := range []int64{resources, max(roundRows, product), rounds} {
		count, err = backend.CheckedAdd(count, n)
		if err != nil {
			return 0, err
		}
	}
	if count > int64(math.MaxInt) {
		return 0, backend.ErrBuildCapacity
	}
	return count, nil
}

type ExecutionInputBudgetProvider interface {
	ExecutionInputBudget() (*resourcebudget.RootBudget, func(context.Context, int) error, int64)
}
type executionInputKey struct{}
type executionBodyKey struct{}

// WithExecutionBodyOwnership records that the HTTP decoder retains and funds
// request string backing until encoding completes; Document borrows it.
func WithExecutionBodyOwnership(ctx context.Context) context.Context {
	return context.WithValue(ctx, executionBodyKey{}, true)
}
func ExecutionBodyOwned(ctx context.Context) bool {
	v, _ := ctx.Value(executionBodyKey{}).(bool)
	return v
}
func ExecutionRowsOwned(ctx context.Context, rows []*ent.Segment) bool {
	s := executionInputState(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		if _, ok := s.metadataFor(row); !ok {
			return false
		}
	}
	return len(rows) > 0
}

type executionInputs struct {
	ctx      context.Context
	root     *resourcebudget.RootBudget
	scope    resourcebudget.Scope
	limit    int64
	mu       sync.Mutex
	leases   []*ExecutionInputLease
	payload  int64
	metadata *executionMetadata
	document *ExecutionInputLease
}

type executionMetadata struct {
	entries []executionMetadataEntry
	next    *executionMetadata
}
type executionMetadataEntry struct {
	row   *ent.Segment
	value map[string]any
}

func (m *executionMetadata) entryFor(row *ent.Segment) *executionMetadataEntry {
	i, found := slices.BinarySearchFunc(m.entries, row.ID, func(entry executionMetadataEntry, id int) int {
		if entry.row.ID < id {
			return -1
		}
		if entry.row.ID > id {
			return 1
		}
		return 0
	})
	if !found {
		return nil
	}
	for ; i < len(m.entries) && m.entries[i].row.ID == row.ID; i++ {
		if m.entries[i].row == row {
			return &m.entries[i]
		}
	}
	return nil
}
func (m *executionMetadata) sort() {
	slices.SortFunc(m.entries, func(a, b executionMetadataEntry) int {
		if a.row.ID < b.row.ID {
			return -1
		}
		if a.row.ID > b.row.ID {
			return 1
		}
		return 0
	})
}

// Callers hold the execution input mutex while reading this owner index.
func (s *executionInputs) metadataFor(row *ent.Segment) (map[string]any, bool) {
	for owner := s.metadata; owner != nil; owner = owner.next {
		if entry := owner.entryFor(row); entry != nil {
			return entry.value, true
		}
	}
	return nil, false
}
func (s *executionInputs) removeMetadata(owner *executionMetadata) {
	for link := &s.metadata; *link != nil; link = &(*link).next {
		if *link == owner {
			*link = owner.next
			clear(owner.entries)
			owner.entries = nil
			owner.next = nil
			return
		}
	}
}

var inputSequence atomic.Uint64

// WithExecutionInputBudget is installed before any execution body is loaded.
// It does not prepare actor inventory or perform I/O in a grant critical section.
func WithExecutionInputBudget(ctx context.Context, root *resourcebudget.RootBudget, scope resourcebudget.Scope, limit int64) context.Context {
	if limit <= 0 {
		limit = DefaultExecutionInputBytes
	}
	allocationLimit := int64(0)
	if root != nil {
		allocationLimit = root.Snapshot(scope.ActorID).Policy.MaxBuildAllocationBytes
	}
	ctx = backend.WithBuildAllocation(ctx, allocationLimit, nil, nil)
	return context.WithValue(ctx, executionInputKey{}, &executionInputs{ctx: ctx, root: root, scope: scope, limit: limit})
}
func PrepareExecutionInputs(ctx context.Context, provider any) (context.Context, error) {
	if _, ok := ctx.Value(executionInputKey{}).(*executionInputs); ok {
		return ctx, nil
	}
	var root *resourcebudget.RootBudget
	var prepare func(context.Context, int) error
	limit := DefaultExecutionInputBytes
	if p, ok := provider.(ExecutionInputBudgetProvider); ok {
		root, prepare, limit = p.ExecutionInputBudget()
	}
	scope := ExecutionScope(ctx)
	if root != nil && prepare != nil {
		if err := prepare(ctx, scope.ActorID); err != nil {
			return nil, err
		}
	}
	return WithExecutionInputBudget(ctx, root, scope, limit), nil
}
func executionInputState(ctx context.Context) *executionInputs {
	s, ok := ctx.Value(executionInputKey{}).(*executionInputs)
	if !ok {
		return &executionInputs{limit: DefaultExecutionInputBytes}
	}
	return s
}

// ExecutionInputOwnership transfers the input/result lifetime to the HTTP
// consumer. The handler releases it after encoding and clearing its result.
type ExecutionInputOwnership struct{ inputs *executionInputs }

func ExecutionInputOwner(ctx context.Context) ExecutionInputOwnership {
	return ExecutionInputOwnership{executionInputState(ctx)}
}
func (o ExecutionInputOwnership) ReleaseExecutionInputs() {
	if o.inputs == nil {
		return
	}
	o.inputs.mu.Lock()
	leases := o.inputs.leases
	o.inputs.leases = nil
	o.inputs.mu.Unlock()
	// Clear all mutually borrowed backing before retiring any of their leases.
	for _, l := range leases {
		l.clearBacking()
	}
	for _, l := range leases {
		l.Close()
	}
	o.inputs.mu.Lock()
	o.inputs.metadata = nil
	o.inputs.document = nil
	o.inputs.mu.Unlock()
	if o.inputs.ctx != nil && o.inputs.scope.JobID == 0 {
		ReleaseExecutionActor(o.inputs.ctx)
	}
	if o.inputs.root != nil {
		_ = o.inputs.root.ForgetActor(o.inputs.scope.ActorID)
	}
}
func (l *ExecutionInputLease) clearBacking() {
	if l == nil {
		return
	}
	l.mu.Lock()
	cleanup := l.clear
	l.clear = nil
	l.mu.Unlock()
	if cleanup != nil {
		cleanup()
	}
}

// Result consumers clear their owned containers before returning the lease.
// Finite status/usage scalars remain available after release.
func (o *PreviewOutput) ReleaseExecutionInputs() {
	if o == nil {
		return
	}
	o.SourceText = ""
	o.TargetText = ""
	o.ApplyToken = ""
	o.Snapshot = nil
	clear(o.QualityIssues)
	o.QualityIssues = nil
	clear(o.BatchEvents)
	o.BatchEvents = nil
	clear(o.Warnings)
	o.Warnings = nil
	clear(o.RoundSummary)
	o.RoundSummary = nil
	o.ExecutionInputOwnership.ReleaseExecutionInputs()
}
func (o *RevisionPreviewOutput) ReleaseExecutionInputs() {
	if o == nil {
		return
	}
	o.SourceText = ""
	o.OriginalTargetText = ""
	o.TargetText = ""
	o.ApplyToken = ""
	o.Snapshot = nil
	clear(o.QualityIssues)
	o.QualityIssues = nil
	clear(o.FixIssues)
	o.FixIssues = nil
	clear(o.BatchEvents)
	o.BatchEvents = nil
	clear(o.Warnings)
	o.Warnings = nil
	clear(o.RoundSummary)
	o.RoundSummary = nil
	o.ExecutionInputOwnership.ReleaseExecutionInputs()
}
func (o *QuickTranslateOutput) ReleaseExecutionInputs() {
	if o == nil {
		return
	}
	o.SourceText = ""
	o.TargetText = ""
	o.SourceLang = ""
	o.TargetLang = ""
	clear(o.QualityIssues)
	o.QualityIssues = nil
	clear(o.BatchEvents)
	o.BatchEvents = nil
	clear(o.Warnings)
	o.Warnings = nil
	clear(o.RoundSummary)
	o.RoundSummary = nil
	o.ExecutionInputOwnership.ReleaseExecutionInputs()
}
func (o *PreviewResult) ReleaseExecutionInputs() {
	if o == nil {
		return
	}
	o.SourceText = ""
	o.TargetText = ""
	o.Snapshot = nil
	o.Baseline = nil
	clear(o.QualityIssues)
	o.QualityIssues = nil
	clear(o.Warnings)
	o.Warnings = nil
	clear(o.RoundSummary)
	o.RoundSummary = nil
	if o.Collector != nil {
		_ = o.Collector.Close()
		o.Collector = nil
	}
	o.ExecutionInputOwnership.ReleaseExecutionInputs()
}
func (o *RevisionPreviewResult) ReleaseExecutionInputs() {
	if o == nil {
		return
	}
	o.SourceText = ""
	o.TargetText = ""
	clear(o.QualityIssues)
	o.QualityIssues = nil
	clear(o.Warnings)
	o.Warnings = nil
	clear(o.RoundSummary)
	o.RoundSummary = nil
	if o.Collector != nil {
		_ = o.Collector.Close()
		o.Collector = nil
	}
	o.ExecutionInputOwnership.ReleaseExecutionInputs()
}
func (o *QuickTranslateResult) ReleaseExecutionInputs() {
	if o == nil {
		return
	}
	o.SourceText = ""
	o.TargetText = ""
	clear(o.QualityIssues)
	o.QualityIssues = nil
	clear(o.Warnings)
	o.Warnings = nil
	clear(o.RoundSummary)
	o.RoundSummary = nil
	if o.Collector != nil {
		_ = o.Collector.Close()
		o.Collector = nil
	}
	o.ExecutionInputOwnership.ReleaseExecutionInputs()
}

// PrepareExecutionRowMetadata gives direct runner inputs one measured metadata
// owner. SQL-loaded rows already have a validated, leased graph.
func PrepareExecutionRowMetadata(ctx context.Context, rows []*ent.Segment, lease *ExecutionInputLease) error {
	s := executionInputState(ctx)
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, DefaultExecutionInputBytes)
	if err != nil {
		return err
	}
	defer b.CloseRetired()
	if err = backend.ReserveSlice[*ent.Segment](b, int64(len(rows))); err != nil {
		return err
	}
	keys := make([]*ent.Segment, 0, len(rows))
	defer func() { clear(keys); keys = nil }()
	for _, row := range rows {
		s.mu.Lock()
		_, ok := s.metadataFor(row)
		s.mu.Unlock()
		if !ok {
			keys = append(keys, row)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if err = backend.ReserveSlice[executionMetadataEntry](b, int64(len(keys))); err != nil {
		return err
	}
	if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(executionMetadata{}))); err != nil {
		return err
	}
	owner := &executionMetadata{entries: make([]executionMetadataEntry, len(keys))}
	installed := false
	defer func() {
		if !installed {
			s.mu.Lock()
			s.removeMetadata(owner)
			s.mu.Unlock()
			clear(owner.entries)
			owner.entries = nil
		}
	}()
	for i, row := range keys {
		var meta map[string]any
		if row.Meta != nil && *row.Meta != "" {
			if err := decodeExecutionString(b, *row.Meta, &meta, min(s.limit, DefaultExecutionInputBytes)); err != nil {
				return err
			}
		}
		owner.entries[i] = executionMetadataEntry{row, meta}
	}
	owner.sort()
	if err = lease.RetainBacking(func() { s.mu.Lock(); s.removeMetadata(owner); s.mu.Unlock() }); err != nil {
		return err
	}
	s.mu.Lock()
	owner.next = s.metadata
	s.metadata = owner
	s.mu.Unlock()
	private, err := backend.AllocationBytes(int64(cap(owner.entries)), int64(unsafe.Sizeof(executionMetadataEntry{})))
	if err != nil {
		return err
	}
	private, err = backend.CheckedAdd(private, int64(unsafe.Sizeof(executionMetadata{})))
	if err != nil {
		return err
	}
	for _, entry := range owner.entries {
		n, err := backend.ValueAllocationBytes(entry.value)
		if err != nil {
			return err
		}
		private, err = backend.CheckedAdd(private, n)
		if err != nil {
			return err
		}
	}
	err = lease.AdoptRetainedAllocation(ctx, b, private)
	installed = err == nil
	return err
}

type ExecutionInputLease struct {
	mu      sync.Mutex
	buildMu sync.Mutex
	state   *executionInputs
	id      string
	usage   resourcebudget.Vector
	slots   []executionDocumentSlot
	closed  bool
	clear   func()
}

// ExecutionInputAllocationBound validates the accurately queried SQL bytes.
// Decoder and driver internals do not become estimated admission requirements.
func ExecutionInputAllocationBound(bytes, rows int64) (int64, error) {
	if bytes < 0 || rows < 0 || bytes > DefaultExecutionInputBytes {
		return 0, backend.ErrBuildCapacity
	}
	// SQL supplies these exact serialized bytes. Decoder graphs and ORM/driver
	// internals are unknown and do not become admission requirements.
	return backend.AllocationBytes(bytes, 1)
}

type executionSegmentProjection struct {
	ID             int
	SegmentIndex   int     `json:"segment_index"`
	ContentVersion int64   `json:"content_version"`
	SourceText     string  `json:"source_text"`
	TargetText     *string `json:"target_text"`
	Status         string
	Meta           *string
	Issues         string
}

// Only metadata and issue cells are decoded as arbitrary JSON. Ordinary SQL
// source/target/status text needs its actual string representation, not an arena.
func executionSegmentAllocationBound(totalBytes, jsonBytes, rows int64) (int64, error) {
	if totalBytes < 0 || totalBytes > DefaultExecutionInputBytes || jsonBytes < 0 || jsonBytes > totalBytes {
		return 0, backend.ErrBuildCapacity
	}
	return ExecutionInputAllocationBound(totalBytes, rows)
}

type executionGlossaryProjection struct {
	Source, Target, Notes string
	CaseSensitive         bool `json:"case_sensitive"`
	Forbidden, Mandatory  bool
}

// Only the exact aggregate string length is visible to this owner.
func fragmentedTextAllocationBound(bytes, pieces int64) (int64, error) {
	if pieces < 0 {
		return 0, backend.ErrBuildCapacity
	}
	return backend.AllocationBytes(bytes, 1)
}
func executionGlossaryAllocationBound(bytes, rows int64) (int64, error) {
	return ExecutionInputAllocationBound(bytes, rows)
}

func checkExecutionAllocationPlan(ctx context.Context, required int64, stage string) error {
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return err
	}
	limit := b.Snapshot().AllocationLimit
	if required < 0 {
		return backend.ErrBuildCapacity
	}
	if limit > 0 && required > limit {
		return executionCapacityFailure(&backend.AllocationCapacityError{Requested: required, Limit: limit}, stage, fmt.Sprintf("job:%d/call:%s", executionInputState(ctx).scope.JobID, executionInputState(ctx).scope.CallID))
	}
	return nil
}
func NewExecutionInputLease(ctx context.Context, bytes int64) (*ExecutionInputLease, error) {
	for {
		state := executionInputState(ctx)
		var changed <-chan struct{}
		if state.root != nil {
			changed = state.root.Changed()
		}
		lease, err := newExecutionInputLease(ctx, bytes, false)
		if !resourcebudget.CapacityHasDrain(err) || changed == nil {
			return lease, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// MarshalExecutionOutput owns the entire response frame before allocation.
// The bounded codec measures exactly the escaping it writes, avoiding the
// standard encoder's additional HTML expansion and implicit growth buffers.
// CPU ownership has ended on return; callers write raw to HTTP outside it,
// drop their raw slice reference, and Close after I/O completes.
func MarshalExecutionOutput(ctx context.Context, payload any) ([]byte, *ExecutionInputLease, error) {
	l, err := NewExecutionInputLease(ctx, 0)
	if err != nil {
		return nil, nil, err
	}
	var raw []byte
	l.clear = func() { clear(raw); raw = nil }
	buildCtx := backend.WithBuildReservation(ctx, l.RetainPayload, func(n int64) { _ = l.ReleasePayload(n) })
	b, err := backend.NewBuildBoundsFromContext(buildCtx, 6*DefaultExecutionInputBytes, 0)
	if err != nil {
		l.Close()
		return nil, nil, err
	}
	defer b.CloseRetired()
	var n int64
	err = l.RunBuild(ctx, func() error {
		var err error
		n, err = b.MeasureJSONLimited(payload, 6*DefaultExecutionInputBytes)
		return err
	})
	if err == nil && (n < 0 || n > 6*DefaultExecutionInputBytes) {
		err = &backend.BuildCapacityError{Resource: "HTTP output bytes"}
	}
	if err == nil {
		err = l.RunBuild(ctx, func() error { var err error; raw, err = b.EncodeJSONMeasured(payload, n, n); return err })
	}
	if err != nil {
		clear(raw)
		raw = nil
		b.CloseRetired()
		l.Close()
		return nil, nil, err
	}
	allocation, err := b.RetainedAllocation(int64(cap(raw)))
	if err != nil {
		clear(raw)
		raw = nil
		b.CloseRetired()
		l.Close()
		return nil, nil, err
	}
	if err = allocation.DetachBuild(); err != nil {
		clear(raw)
		raw = nil
		allocation.Release()
		b.CloseRetired()
		l.Close()
		return nil, nil, err
	}
	// The network frame keeps its existing callback receipt after the encoder's
	// real scratch retires. It is cleared before the receipt is released.
	l.clear = func() { clear(raw); raw = nil; allocation.Release() }
	return raw, l, nil
}

func executionOutputAllocationCredit(n int64) (int64, error) {
	// Only the frame's explicit byte capacity is retained by the HTTP writer.
	return backend.AllocationBytes(n, 1)
}

func executionOutputBounds(n int64) (*backend.BuildBounds, int64, error) {
	if n < 0 || n > 6*DefaultExecutionInputBytes {
		return nil, 0, backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBounds(n, 0)
	if err != nil {
		return nil, 0, err
	}
	output, err := backend.AllocationBytes(n, 1)
	if err != nil {
		return nil, 0, err
	}
	return b, output, nil
}
func newExecutionInputLease(ctx context.Context, bytes int64, prepare bool) (*ExecutionInputLease, error) {
	s := executionInputState(ctx)
	if bytes < 0 || bytes > int64(math.MaxInt) {
		return nil, backend.ErrBuildCapacity
	}
	l := &ExecutionInputLease{state: s, id: fmt.Sprintf("input:%d:%s:%d", s.scope.JobID, s.scope.CallID, inputSequence.Add(1)), usage: resourcebudget.Vector{PayloadBytes: bytes}}
	if prepare {
		l.usage.PrepareOwners = 1
	}
	if s.root != nil {
		// An empty lease has no Root identity yet, but its attribution must
		// already satisfy the same identity contract as a later real claim.
		if s.scope.ActorID <= 0 || s.scope.JobID < 0 || (s.scope.JobID == 0) == (s.scope.CallID == "") || len(s.scope.CallID) > 4096 || len(l.id) > 4096 {
			return nil, executionCapacityFailure(resourcebudget.ErrIdentity, "input_admission", l.id)
		}
		p := s.root.Snapshot(s.scope.ActorID).Policy
		s.mu.Lock()
		own := s.payload
		s.mu.Unlock()
		if bytes > 0 && (p.Actor.MaxPayloadBytes > 0 && bytes > p.Actor.MaxPayloadBytes-own ||
			p.Instance.MaxPayloadBytes > 0 && bytes > p.Instance.MaxPayloadBytes-own) {
			limit := p.Actor.MaxPayloadBytes
			if limit == 0 || p.Instance.MaxPayloadBytes > 0 && p.Instance.MaxPayloadBytes < limit {
				limit = p.Instance.MaxPayloadBytes
			}
			return nil, &ExecutionCapacityFailure{Stage: "input_admission", Dimension: "payload_bytes", Owner: l.id, Used: own, Requested: bytes, Limit: limit, Cause: backend.ErrBuildCapacity}
		}
	}
	if s.root != nil && l.usage != (resourcebudget.Vector{}) {
		if err := s.root.Claim(s.scope, l.id, l.usage); err != nil {
			return nil, executionCapacityFailure(err, "input_admission", l.id)
		}
	}
	s.mu.Lock()
	s.leases = append(s.leases, l)
	s.payload += l.usage.PayloadBytes
	s.mu.Unlock()
	return l, nil
}

// The caller holds l.mu. Zero means there is no active Root identity; a later
// actual payload, prepare owner, build worker or transferred receipt establishes
// it. Releasing the complete vector removes that identity again.
func (l *ExecutionInputLease) syncRootLocked(next resourcebudget.Vector) error {
	if l.state.root == nil {
		return nil
	}
	if l.usage == (resourcebudget.Vector{}) {
		if next == (resourcebudget.Vector{}) {
			return nil
		}
		return l.state.root.Claim(l.state.scope, l.id, next)
	}
	return l.state.root.Transfer(l.state.scope, l.id, next)
}
func (l *ExecutionInputLease) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	cleanup := l.clear
	l.clear = nil
	l.mu.Unlock()
	// Backing cleanup can take the dictionary lock; never hold the lease lock
	// across it because Add acquires credit while holding that dictionary lock.
	if cleanup != nil {
		cleanup()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.root != nil {
		_ = l.syncRootLocked(resourcebudget.Vector{})
	}
	l.state.mu.Lock()
	l.state.payload -= l.usage.PayloadBytes
	for i, e := range l.state.leases {
		if e == l {
			copy(l.state.leases[i:], l.state.leases[i+1:])
			l.state.leases[len(l.state.leases)-1] = nil
			l.state.leases = l.state.leases[:len(l.state.leases)-1]
			break
		}
	}
	l.state.mu.Unlock()
	l.usage = resourcebudget.Vector{}
	l.slots = nil
}

// RetainPayload increases credit before an append or copy; callers clear the
// corresponding backing before Close. Only this temporary identity is changed.
func (l *ExecutionInputLease) RetainPayload(bytes int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return resourcebudget.ErrIdentity
	}
	if bytes < 0 || bytes > int64(math.MaxInt)-l.usage.PayloadBytes {
		return backend.ErrBuildCapacity
	}
	next := l.usage
	next.PayloadBytes += bytes
	if l.state.root != nil {
		if err := l.syncRootLocked(next); err != nil {
			return executionCapacityFailure(err, "input_growth", l.id)
		}
	}
	l.state.mu.Lock()
	l.state.payload += bytes
	l.state.mu.Unlock()
	l.usage = next
	return nil
}

// retainOutsideBuild waits only after the caller has given up its CPU permit.
// Growth performed inside RunBuild continues to use nonblocking RetainPayload.
func (l *ExecutionInputLease) retainOutsideBuild(ctx context.Context, bytes int64) error {
	for {
		var changed <-chan struct{}
		if l.state.root != nil {
			changed = l.state.root.Changed()
		}
		err := l.RetainPayload(bytes)
		if !resourcebudget.CapacityHasDrain(err) || changed == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (l *ExecutionInputLease) ReleasePayload(bytes int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return resourcebudget.ErrIdentity
	}
	if bytes < 0 || bytes > l.usage.PayloadBytes {
		return resourcebudget.ErrInvalid
	}
	next := l.usage
	next.PayloadBytes -= bytes
	if l.state.root != nil {
		if err := l.syncRootLocked(next); err != nil {
			return err
		}
	}
	l.state.mu.Lock()
	l.state.payload -= bytes
	l.state.mu.Unlock()
	l.usage = next
	return nil
}
func (l *ExecutionInputLease) RetainBacking(cleanup func()) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return resourcebudget.ErrIdentity
	}
	old := l.clear
	l.clear = func() {
		if old != nil {
			old()
		}
		cleanup()
	}
	return nil
}
func (l *ExecutionInputLease) RunBuild(ctx context.Context, build func() error) error {
	l.buildMu.Lock()
	defer l.buildMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var changed <-chan struct{}
		if l.state.root != nil {
			changed = l.state.root.Changed()
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return resourcebudget.ErrIdentity
		}
		next := l.usage
		next.BuildWorkers = 1
		if l.state.root != nil {
			if err := l.syncRootLocked(next); err != nil {
				l.mu.Unlock()
				if !errors.Is(err, resourcebudget.ErrCapacity) {
					return err
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-changed:
				}
				continue
			}
		}
		l.usage = next
		l.mu.Unlock()
		break
	}
	defer func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.closed {
			return
		}
		next := l.usage
		next.BuildWorkers = 0
		if err := l.syncRootLocked(next); err == nil {
			l.usage = next
		}
	}()
	return executionCapacityFailure(build(), "input_build", l.id)
}
func (l *ExecutionInputLease) Prepare(on bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return resourcebudget.ErrIdentity
	}
	next := l.usage
	next.PrepareOwners = 0
	if on {
		next.PrepareOwners = 1
	}
	if l.state.root != nil {
		if err := l.syncRootLocked(next); err != nil {
			return err
		}
	}
	l.usage = next
	return nil
}

func inputTextLength(s *sql.Selector, col string) string {
	c := "COALESCE(CAST(" + s.C(col) + " AS TEXT),'')"
	if s.Dialect() == dialect.Postgres {
		return "OCTET_LENGTH(" + c + ")"
	}
	return "LENGTH(CAST(" + c + " AS BLOB))"
}
func inputReadTx(ctx context.Context, client *ent.Client) (*ent.Tx, error) {
	d := ""
	_, err := client.Segment.Query().Aggregate(ent.Count()).Modify(func(s *sql.Selector) { d = s.Dialect() }).Int(ctx)
	if err != nil {
		return nil, err
	}
	if d == dialect.Postgres {
		return client.BeginTx(ctx, &stdsql.TxOptions{Isolation: stdsql.LevelRepeatableRead, ReadOnly: true})
	}
	return client.Tx(ctx)
}

// LoadExecutionSegments measures and materializes from the same read snapshot.
// Raw JSON is projected as text and decoded under one cumulative BuildBounds;
// the ORM never gets to allocate an unvalidated arbitrary quality-issue graph.
func LoadExecutionSegments(ctx context.Context, client *ent.Client, resourceID int) ([]*ent.Segment, *ExecutionInputLease, error) {
	for {
		s := executionInputState(ctx)
		var changed <-chan struct{}
		if s.root != nil {
			changed = s.root.Changed()
		}
		rows, lease, err := loadExecutionSegments(ctx, client, resourceID)
		if !errors.Is(err, resourcebudget.ErrCapacity) || s.root == nil {
			return rows, lease, err
		}
		if !resourcebudget.CapacityHasDrain(err) {
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-changed:
		}
	}
}
func loadExecutionSegments(ctx context.Context, client *ent.Client, resourceID int) ([]*ent.Segment, *ExecutionInputLease, error) {
	tx, err := inputReadTx(ctx, client)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	c := tx.Client()
	var stats []struct {
		Count     int64 `json:"count"`
		Bytes     int64 `json:"bytes"`
		JSONBytes int64 `json:"json_bytes"`
	}
	err = c.Segment.Query().Where(segment.ResourceIDEQ(resourceID)).Select(segment.FieldID).Modify(func(s *sql.Selector) {
		expr := inputTextLength(s, segment.FieldSourceText) + "+" + inputTextLength(s, segment.FieldTargetText) + "+" + inputTextLength(s, segment.FieldMeta) + "+" + inputTextLength(s, segment.FieldQualityIssues) + "+" + inputTextLength(s, segment.FieldStatus)
		jsonExpr := inputTextLength(s, segment.FieldMeta) + "+" + inputTextLength(s, segment.FieldQualityIssues)
		s.Select("COUNT(*) AS count", "COALESCE(SUM("+expr+"),0) AS bytes", "COALESCE(SUM("+jsonExpr+"),0) AS json_bytes")
	}).Scan(ctx, &stats)
	if err != nil {
		return nil, nil, err
	}
	if len(stats) != 1 {
		return nil, nil, backend.ErrBuildCapacity
	}
	st := stats[0]
	if st.Count < 0 || st.Count > int64(math.MaxInt) || st.Bytes < 0 || st.Bytes > min(executionInputState(ctx).limit, DefaultExecutionInputBytes) {
		return nil, nil, backend.ErrBuildCapacity
	}
	credit, err := executionSegmentAllocationBound(st.Bytes, st.JSONBytes, st.Count)
	if err != nil {
		return nil, nil, err
	}
	if err = checkExecutionAllocationPlan(ctx, credit, "input_projection"); err != nil {
		return nil, nil, err
	}
	l, err := newExecutionInputLease(ctx, 0, true)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			l.Close()
		}
	}()
	if err = l.RetainExplicitAllocation(ctx, credit); err != nil {
		return nil, nil, err
	}
	var raw []executionSegmentProjection
	defer func() { clear(raw) }()
	err = c.Segment.Query().Where(segment.ResourceIDEQ(resourceID)).Order(segment.BySegmentIndex(), segment.ByID()).Select(segment.FieldID, segment.FieldSegmentIndex, segment.FieldContentVersion, segment.FieldSourceText, segment.FieldTargetText, segment.FieldStatus, segment.FieldMeta).Modify(func(s *sql.Selector) {
		s.AppendSelect("COALESCE(CAST(" + s.C(segment.FieldQualityIssues) + " AS TEXT),'[]') AS issues")
	}).Scan(ctx, &raw)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(raw)) != st.Count {
		return nil, nil, backend.ErrBuildCapacity
	}
	var pointerBytes int64
	for _, row := range raw {
		if row.TargetText != nil {
			pointerBytes += int64(unsafe.Sizeof(""))
		}
		if row.Meta != nil {
			pointerBytes += int64(unsafe.Sizeof(""))
		}
	}
	if err = l.RetainExplicitAllocation(ctx, pointerBytes); err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	var rows []*ent.Segment
	var metadata *executionMetadata
	var rowBounds *backend.BuildBounds
	defer func() {
		if !ok {
			l.Close()
		}
		rowBounds.CloseRetired()
	}()
	l.clear = func() {
		l.state.mu.Lock()
		l.state.removeMetadata(metadata)
		l.state.mu.Unlock()
		clear(rows)
	}
	err = l.RunBuild(ctx, func() error {
		b, err := executionMetadataBounds(ctx, st.Bytes)
		if err != nil {
			return err
		}
		rowBounds = b
		if err = backend.ReserveSlice[*ent.Segment](b, int64(len(raw))); err != nil {
			return err
		}
		rows = make([]*ent.Segment, len(raw))
		if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(resourceID))); err != nil {
			return err
		}
		for i, r := range raw {
			if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(ent.Segment{}))); err != nil {
				return err
			}
			rows[i] = &ent.Segment{ID: r.ID, ResourceID: &resourceID, SegmentIndex: r.SegmentIndex, ContentVersion: r.ContentVersion, SourceText: r.SourceText, TargetText: r.TargetText, Status: segment.Status(r.Status), Meta: r.Meta}
		}
		if err = backend.ReserveSlice[executionMetadataEntry](b, int64(len(rows))); err != nil {
			return err
		}
		if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(executionMetadata{}))); err != nil {
			return err
		}
		metadata = &executionMetadata{entries: make([]executionMetadataEntry, len(rows))}
		for i, r := range raw {
			var issues []qa.QualityIssue
			var meta map[string]any
			// Each immutable row keeps its decoded graph under the input lease.
			// The codec records only visible allocations it actually constructs.
			size := int64(len(r.Issues))
			if r.Meta != nil {
				size += int64(len(*r.Meta))
			}
			if r.Issues != "" {
				if err := decodeExecutionString(b, r.Issues, &issues, size); err != nil {
					return err
				}
			}
			if r.Meta != nil && *r.Meta != "" {
				if err := decodeExecutionString(b, *r.Meta, &meta, size); err != nil {
					return err
				}
			}
			rows[i].QualityIssues = issues
			metadata.entries[i] = executionMetadataEntry{rows[i], meta}
		}
		metadata.sort()
		l.state.mu.Lock()
		metadata.next = l.state.metadata
		l.state.metadata = metadata
		l.state.mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	clear(raw)
	raw = nil
	retained, err := executionRowsRetainedAllocation(rows, metadata)
	if err != nil {
		return nil, nil, err
	}
	var retainedSQL int64
	for _, row := range rows {
		retainedSQL += int64(len(row.SourceText) + len(row.Status))
		if row.TargetText != nil {
			retainedSQL += int64(len(*row.TargetText))
		}
		if row.Meta != nil {
			retainedSQL += int64(len(*row.Meta))
		}
	}
	err = l.AdoptRetainedAllocation(ctx, rowBounds, retained-retainedSQL-pointerBytes)
	if err == nil {
		err = l.ReleasePayload(credit - retainedSQL)
	}
	if err != nil {
		return nil, nil, err
	}
	if err = l.Prepare(false); err != nil {
		return nil, nil, err
	}
	ok = true
	return rows, l, nil
}

// ReplaceExecutionDocumentLease is called only after the replacement document
// is installed and no old body reference remains. New credit is obtained first.
func ReplaceExecutionDocumentLease(ctx context.Context, l *ExecutionInputLease) {
	s := executionInputState(ctx)
	s.mu.Lock()
	old := s.document
	s.document = l
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func ExecutionDocumentLease(ctx context.Context) *ExecutionInputLease {
	s := executionInputState(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.document
}

// ExecutionSegmentMeta reuses the graph validated during the snapshot read.
// Direct runner fixtures are still decoded through a bounded codec.
func ExecutionSegmentMeta(ctx context.Context, row *ent.Segment) (map[string]any, error) {
	s := executionInputState(ctx)
	s.mu.Lock()
	m, ok := s.metadataFor(row)
	s.mu.Unlock()
	if ok {
		return m, nil
	}
	if row.Meta == nil || *row.Meta == "" {
		return nil, nil
	}
	b, err := executionMetadataBounds(ctx, int64(len(*row.Meta)))
	if err != nil {
		return nil, err
	}
	err = decodeExecutionString(b, *row.Meta, &m, min(s.limit, DefaultExecutionInputBytes))
	return m, err
}

func executionMetadataBounds(ctx context.Context, bytes int64) (*backend.BuildBounds, error) {
	if bytes < 0 || bytes > DefaultExecutionInputBytes {
		return nil, backend.ErrBuildCapacity
	}
	return backend.NewBuildBoundsFromContext(ctx, 0, bytes)
}

func executionRowsRetainedAllocation(rows []*ent.Segment, owner *executionMetadata) (int64, error) {
	credit, err := backend.AllocationBytes(int64(len(rows)), int64(unsafe.Sizeof(*new(*ent.Segment))))
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		idBytes, err := backend.AllocationBytes(1, int64(unsafe.Sizeof(int(0))))
		if err != nil {
			return 0, err
		}
		credit, err = backend.CheckedAdd(credit, idBytes)
		if err != nil {
			return 0, err
		}
	}
	add := func(n int64, e error) error {
		if e != nil {
			return e
		}
		var err error
		credit, err = backend.CheckedAdd(credit, n)
		return err
	}
	for _, row := range rows {
		if err = add(backend.AllocationBytes(1, int64(unsafe.Sizeof(ent.Segment{})))); err != nil {
			return 0, err
		}
		for _, text := range []string{row.SourceText, string(row.Status)} {
			if err = add(backend.AllocationBytes(int64(len(text)), 1)); err != nil {
				return 0, err
			}
		}
		for _, text := range []*string{row.TargetText, row.Meta} {
			if text == nil {
				continue
			}
			if err = add(backend.AllocationBytes(1, int64(unsafe.Sizeof("")))); err != nil {
				return 0, err
			}
			if err = add(backend.AllocationBytes(int64(len(*text)), 1)); err != nil {
				return 0, err
			}
		}
		if err = add(backend.ValueAllocationBytes(row.QualityIssues)); err != nil {
			return 0, err
		}
		if owner != nil {
			if entry := owner.entryFor(row); entry != nil {
				if err = add(backend.ValueAllocationBytes(entry.value)); err != nil {
					return 0, err
				}
			}
		}
	}
	if owner != nil {
		if err = add(backend.AllocationBytes(int64(len(rows)), int64(unsafe.Sizeof(executionMetadataEntry{})))); err != nil {
			return 0, err
		}
		if err = add(backend.AllocationBytes(1, int64(unsafe.Sizeof(executionMetadata{})))); err != nil {
			return 0, err
		}
	}
	return credit, nil
}

// LoadExecutionGlossary avoids the general editor glossary's unrestricted
// ORM load and per-text lookup cache. Count and UTF-8 projection share a snapshot.
func LoadExecutionGlossary(ctx context.Context, client *ent.Client, project *ent.Project, persist bool) (glossary.Glossary, *ExecutionInputLease, error) {
	if project == nil {
		return nil, nil, ErrProjectNotFound
	}
	for {
		s := executionInputState(ctx)
		var changed <-chan struct{}
		if s.root != nil {
			changed = s.root.Changed()
		}
		g, l, err := loadExecutionGlossary(ctx, client, project, persist)
		if !errors.Is(err, resourcebudget.ErrCapacity) || s.root == nil {
			return g, l, err
		}
		if !resourcebudget.CapacityHasDrain(err) {
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-changed:
		}
	}
}
func loadExecutionGlossary(ctx context.Context, client *ent.Client, project *ent.Project, persist bool) (glossary.Glossary, *ExecutionInputLease, error) {
	tx, err := inputReadTx(ctx, client)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	c := tx.Client()
	var stats []struct {
		Count int64
		Bytes int64
	}
	err = c.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(project.ID)).Select(glossaryentry.FieldID).Modify(func(s *sql.Selector) {
		expr := inputTextLength(s, glossaryentry.FieldSource) + "+" + inputTextLength(s, glossaryentry.FieldTarget) + "+" + inputTextLength(s, glossaryentry.FieldNotes) + "+" + inputTextLength(s, glossaryentry.FieldSourceKey)
		s.Select("COUNT(*) AS count", "COALESCE(SUM("+expr+"),0) AS bytes")
	}).Scan(ctx, &stats)
	if err != nil {
		return nil, nil, err
	}
	if len(stats) != 1 || stats[0].Count < 0 || stats[0].Count > int64(math.MaxInt) || stats[0].Bytes < 0 || stats[0].Bytes > min(executionInputState(ctx).limit, DefaultExecutionInputBytes) {
		return nil, nil, backend.ErrBuildCapacity
	}
	st := stats[0]
	credit, err := executionGlossaryAllocationBound(st.Bytes, st.Count)
	if err != nil {
		return nil, nil, err
	}
	if err = checkExecutionAllocationPlan(ctx, credit, "glossary_projection"); err != nil {
		return nil, nil, err
	}
	l, err := newExecutionInputLease(ctx, 0, true)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			l.Close()
		}
	}()
	if err = l.RetainExplicitAllocation(ctx, credit); err != nil {
		return nil, nil, err
	}
	var raw []executionGlossaryProjection
	defer func() { clear(raw) }()
	err = c.GlossaryEntry.Query().Where(glossaryentry.ProjectIDEQ(project.ID)).Order(glossaryentry.BySourceKey(), glossaryentry.ByID()).Select(glossaryentry.FieldSource, glossaryentry.FieldTarget, glossaryentry.FieldNotes, glossaryentry.FieldCaseSensitive, glossaryentry.FieldForbidden, glossaryentry.FieldMandatory).Scan(ctx, &raw)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(raw)) != st.Count {
		return nil, nil, backend.ErrBuildCapacity
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	g := &executionGlossary{lease: l, bytes: st.Bytes}
	if persist {
		g.database = &DatabaseGlossary{client: client, projectID: project.ID, lookupCache: make(map[string][]glossary.Entry)}
	}
	l.clear = func() {
		g.mutation.Lock()
		defer g.mutation.Unlock()
		g.mu.Lock()
		defer g.mu.Unlock()
		clear(g.entries)
		g.entries = nil
		g.head = nil
		if g.database != nil {
			g.database.mu.Lock()
			if pending := g.database.pendingCache; pending != nil {
				if pending.settlement.rollback != nil {
					_ = pending.settlement.rollback()
				}
				g.database.pendingCache = nil
			}
			clear(g.database.entries)
			g.database.entries = nil
			clear(g.database.lookupCache)
			g.database.entrySnapshot.Store(nil)
			g.database.mu.Unlock()
		}
	}
	err = l.RunBuild(ctx, func() error {
		var err error
		g.entryBacking, err = backend.AllocationBytes(int64(len(raw)), int64(unsafe.Sizeof(glossary.Entry{})))
		if err != nil {
			return err
		}
		known, err := backend.CheckedProduct(int64(len(raw)), int64(unsafe.Sizeof(glossary.EntrySnapshot{})))
		if err != nil {
			return err
		}
		known, err = backend.CheckedAdd(known, g.entryBacking)
		if err != nil {
			return err
		}
		known, err = backend.CheckedAdd(known, int64(unsafe.Sizeof(executionGlossary{})))
		if err != nil {
			return err
		}
		if g.database != nil {
			known, err = backend.CheckedAdd(known, int64(unsafe.Sizeof(DatabaseGlossary{})))
		}
		if err != nil {
			return err
		}
		if err = l.RetainExplicitAllocation(ctx, known); err != nil {
			return err
		}
		g.entries = make([]glossary.Entry, len(raw))
		for i, r := range raw {
			g.entries[i] = glossary.Entry{Source: r.Source, Target: r.Target, Notes: r.Notes, CaseSensitive: r.CaseSensitive, Forbidden: r.Forbidden, Mandatory: r.Mandatory}
			g.head = &glossary.EntrySnapshot{Entry: g.entries[i], Order: i, Previous: g.head}
		}
		if g.database != nil {
			g.database.entries = g.entries
			g.database.entrySnapshot.Store(g.head)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	clear(raw)
	raw = nil
	retained, err := executionGlossaryRetainedAllocation(g)
	if err != nil {
		return nil, nil, err
	}
	if retained > l.usage.PayloadBytes {
		err = l.RetainPayload(retained - l.usage.PayloadBytes)
	} else {
		err = l.ReleasePayload(l.usage.PayloadBytes - retained)
	}
	if err != nil {
		return nil, nil, err
	}
	if err = l.Prepare(false); err != nil {
		return nil, nil, err
	}
	ok = true
	return g, l, nil
}
func NewMemoryExecutionGlossary(ctx context.Context) (glossary.Glossary, error) {
	header, err := backend.AllocationBytes(1, int64(unsafe.Sizeof(executionGlossary{})))
	if err != nil {
		return nil, err
	}
	if err = checkExecutionAllocationPlan(ctx, header, "glossary_header"); err != nil {
		return nil, err
	}
	l, err := NewExecutionInputLease(ctx, header)
	if err != nil {
		return nil, err
	}
	g := &executionGlossary{lease: l}
	l.clear = func() { g.mu.Lock(); clear(g.entries); g.entries = nil; g.head = nil; g.mu.Unlock() }
	return g, nil
}

type executionGlossary struct {
	mutation     sync.Mutex // serialize writers without holding reader lock across build permits
	mu           sync.RWMutex
	lease        *ExecutionInputLease
	entries      []glossary.Entry
	head         *glossary.EntrySnapshot
	database     *DatabaseGlossary
	bytes        int64
	entryBacking int64
}

func executionGlossaryRetainedAllocation(g *executionGlossary) (int64, error) {
	credit, err := backend.AllocationBytes(1, int64(unsafe.Sizeof(executionGlossary{})))
	if err != nil {
		return 0, err
	}
	add := func(n int64, e error) error {
		if e != nil {
			return e
		}
		var err error
		credit, err = backend.CheckedAdd(credit, n)
		return err
	}
	if err = add(g.entryBacking, nil); err != nil {
		return 0, err
	}
	for _, entry := range g.entries {
		for _, text := range []string{entry.Source, entry.Target, entry.Notes} {
			if err = add(backend.AllocationBytes(int64(len(text)), 1)); err != nil {
				return 0, err
			}
		}
		if err = add(backend.AllocationBytes(1, int64(unsafe.Sizeof(glossary.EntrySnapshot{})))); err != nil {
			return 0, err
		}
	}
	if g.database != nil {
		if err = add(backend.AllocationBytes(1, int64(unsafe.Sizeof(DatabaseGlossary{})))); err != nil {
			return 0, err
		}
	}
	return credit, nil
}

func (g *executionGlossary) retireReplacedBacking(oldBacking int64) error {
	backing, err := backend.AllocationBytes(int64(cap(g.entries)), int64(unsafe.Sizeof(glossary.Entry{})))
	if err != nil {
		return err
	}
	g.entryBacking = backing
	return g.lease.ReleasePayload(oldBacking)
}
func (g *executionGlossary) snapshot() *glossary.EntrySnapshot {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.database != nil {
		return g.database.entrySnapshot.Load()
	}
	return g.head
}
func (g *executionGlossary) Lookup(ctx context.Context, text, src, tgt string) ([]glossary.Entry, error) {
	if int64(len(text)) > DefaultExecutionInputBytes {
		return nil, backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, DefaultExecutionInputBytes)
	if err != nil {
		return nil, err
	}
	return g.LookupEntriesBounded(ctx, b, text, src, tgt)
}
func (g *executionGlossary) LookupEntriesBounded(ctx context.Context, b *backend.BuildBounds, text, _, _ string) ([]glossary.Entry, error) {
	return glossary.LookupEntrySnapshotContextBounded(ctx, b, g.snapshot(), text, false)
}
func (g *executionGlossary) VisitMatchesBounded(ctx context.Context, text, _, _ string, b *backend.BuildBounds, run func(context.Context, func() error) error, yield func(glossary.Entry) error) error {
	return glossary.VisitEntrySnapshotMatches(ctx, b, g.snapshot(), text, false, run, yield)
}
func (g *executionGlossary) LookupSourcesBounded(ctx context.Context, text, _, _ string, limit int64, run func(context.Context, func() error) error) ([]string, error) {
	g.mu.RLock()
	entries := g.entries
	if g.database != nil {
		g.database.mu.RLock()
		entries = g.database.entries
		g.database.mu.RUnlock()
	}
	g.mu.RUnlock()
	var out []string
	fn := func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, limit, int64(len(text)))
		if err != nil {
			return err
		}
		out, err = glossary.LookupSourcesFromEntries(b, entries, text, limit)
		return err
	}
	var err error
	if run != nil {
		err = run(ctx, fn)
	} else {
		err = fn()
	}
	return out, err
}
func (g *executionGlossary) Add(ctx context.Context, entries ...glossary.Entry) (glossary.AddResult, error) {
	g.mutation.Lock()
	defer g.mutation.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	var result glossary.AddResult
	if len(entries) > math.MaxInt-len(g.entries) {
		return result, backend.ErrBuildCapacity
	}
	addedCredit, err := backend.AllocationBytes(int64(len(entries)), int64(unsafe.Sizeof(glossary.Entry{})))
	if err != nil {
		return result, err
	}
	skippedCredit, err := backend.AllocationBytes(int64(len(entries)), int64(unsafe.Sizeof(glossary.SkippedEntry{})))
	if err != nil {
		return result, err
	}
	resultCredit, err := backend.CheckedAdd(addedCredit, skippedCredit)
	if err != nil {
		return result, err
	}
	resultLease, err := newExecutionInputLease(ctx, 0, false)
	if err != nil {
		return result, err
	}
	if err = resultLease.RetainExplicitAllocation(ctx, resultCredit); err != nil {
		resultLease.Close()
		return result, err
	}
	result.ReleaseOwned = resultLease.Close
	if g.database != nil {
		oldBacking := g.entryBacking
		oldCount := len(g.entries)
		r, addErr := g.database.Add(g.allocationContext(ctx), entries...)
		g.entries = g.database.entries
		g.head = g.database.entrySnapshot.Load()
		for _, entry := range r.Added {
			g.bytes += int64(len(entry.Source) + len(entry.Target) + len(entry.Notes))
		}
		r.ReleaseOwned = resultLease.Close
		_ = resultLease.RetainBacking(func() { clear(r.Added); clear(r.Skipped); r.Added = nil; r.Skipped = nil })
		if len(g.entries) != oldCount {
			addErr = errors.Join(addErr, g.retireReplacedBacking(oldBacking))
		}
		return r, addErr
	}
	// These capacities are the actual make calls below, independent of how many
	// proposed entries later pass normalization or duplicate checks.
	backing, err := backend.AllocationBytes(int64(len(g.entries)+len(entries)), int64(unsafe.Sizeof(glossary.Entry{})))
	if err != nil {
		resultLease.Close()
		return result, err
	}
	if err = g.lease.RetainExplicitAllocation(ctx, backing); err != nil {
		resultLease.Close()
		return result, err
	}
	result.Added = make([]glossary.Entry, 0, len(entries))
	result.Skipped = make([]glossary.SkippedEntry, 0, len(entries))
	oldBacking := g.entryBacking
	updated := make([]glossary.Entry, len(g.entries), len(g.entries)+len(entries))
	copy(updated, g.entries)
	g.entries = updated
	g.entryBacking = backing
	_ = g.lease.ReleasePayload(oldBacking)
	_ = resultLease.RetainBacking(func() { clear(result.Added); clear(result.Skipped); result.Added = nil; result.Skipped = nil })
	for _, e := range entries {
		e.Source = strings.TrimSpace(e.Source)
		e.Target = strings.TrimSpace(e.Target)
		if e.Source == "" || e.Target == "" && !e.Forbidden {
			result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: e, Reason: glossary.SkipReasonEmpty})
			continue
		}
		in := GlossaryEntryInput{Source: e.Source, Target: e.Target, Forbidden: e.Forbidden}
		if existing, ok := findCachedGlossaryEntry(g.entries, in); ok {
			if existing.Target != e.Target {
				result.Skipped = append(result.Skipped, glossary.SkippedEntry{Proposed: e, Existing: existing, Reason: glossary.SkipReasonExists})
			}
			continue
		}
		text := int64(len(e.Source) + len(e.Target) + len(e.Notes))
		if text > DefaultExecutionInputBytes-g.bytes {
			return result, backend.ErrBuildCapacity
		}
		known, err := backend.CheckedAdd(text, int64(unsafe.Sizeof(glossary.EntrySnapshot{})))
		if err != nil {
			return result, err
		}
		if err := g.lease.RetainExplicitAllocation(ctx, known); err != nil {
			return result, err
		}
		e.Source, e.Target, e.Notes = strings.Clone(e.Source), strings.Clone(e.Target), strings.Clone(e.Notes)
		g.head = &glossary.EntrySnapshot{Entry: e, Order: len(g.entries), Previous: g.head}
		g.entries = append(g.entries, e)
		g.bytes += text
		result.Added = append(result.Added, e)
	}
	return result, nil
}
