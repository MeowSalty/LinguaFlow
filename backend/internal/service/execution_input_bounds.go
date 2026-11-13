package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"

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
		if _, ok := s.metadata[row]; !ok {
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
	metadata map[*ent.Segment]map[string]any
	document *ExecutionInputLease
}

var inputSequence atomic.Uint64

// WithExecutionInputBudget is installed before any execution body is loaded.
// It does not prepare actor inventory or perform I/O in a grant critical section.
func WithExecutionInputBudget(ctx context.Context, root *resourcebudget.RootBudget, scope resourcebudget.Scope, limit int64) context.Context {
	if limit <= 0 {
		limit = DefaultExecutionInputBytes
	}
	return context.WithValue(ctx, executionInputKey{}, &executionInputs{ctx: ctx, root: root, scope: scope, limit: limit, metadata: make(map[*ent.Segment]map[string]any)})
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
	clear(o.inputs.metadata)
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

// PrepareExecutionRowMetadata validates direct runner fixtures/callers under
// one cumulative node budget. SQL-loaded rows already have a validated graph.
func PrepareExecutionRowMetadata(ctx context.Context, rows []*ent.Segment, bytes int64) error {
	b, err := executionMetadataBounds(bytes)
	if err != nil {
		return err
	}
	s := executionInputState(ctx)
	for _, row := range rows {
		s.mu.Lock()
		_, ok := s.metadata[row]
		s.mu.Unlock()
		if ok {
			continue
		}
		var meta map[string]any
		if row.Meta != nil && *row.Meta != "" {
			if err := b.DecodeJSONLimited([]byte(*row.Meta), &meta, min(s.limit, DefaultExecutionInputBytes)); err != nil {
				return err
			}
		}
		s.mu.Lock()
		if s.metadata == nil {
			s.metadata = make(map[*ent.Segment]map[string]any)
		}
		s.metadata[row] = meta
		s.mu.Unlock()
	}
	return nil
}

type ExecutionInputLease struct {
	mu      sync.Mutex
	buildMu sync.Mutex
	state   *executionInputs
	id      string
	usage   resourcebudget.Vector
	closed  bool
	clear   func()
}

// ExecutionInputAllocationBound uses the existing codec's declared text/node
// ceilings plus six finite ORM/input/document container records per row.
// It is a deterministic allocation ceiling, not a sampled heap estimate.
func ExecutionInputAllocationBound(bytes, rows int64) (int64, error) {
	if bytes < 0 || rows < 0 || bytes > DefaultExecutionInputBytes || rows > 65536 {
		return 0, backend.ErrBuildCapacity
	}
	b, err := executionMetadataBounds(bytes)
	if err != nil {
		return 0, err
	}
	nodes := b.Snapshot().NodeLimit
	if bytes > math.MaxInt64/2 || rows > math.MaxInt64/(6*backend.MaxNodeAllocationBytes) {
		return 0, backend.ErrBuildCapacity
	}
	return 2*bytes + b.Snapshot().TextLimit + nodes*backend.MaxNodeAllocationBytes + 6*rows*backend.MaxNodeAllocationBytes, nil
}
func NewExecutionInputLease(ctx context.Context, bytes int64) (*ExecutionInputLease, error) {
	return newExecutionInputLease(ctx, bytes, false)
}

// MarshalExecutionOutput owns the entire response frame before allocation.
// The bounded codec measures exactly the escaping it writes, avoiding the
// standard encoder's additional HTML expansion and implicit growth buffers.
// CPU ownership has ended on return; callers write raw to HTTP outside it,
// drop their raw slice reference, and Close after I/O completes.
func MarshalExecutionOutput(ctx context.Context, payload any) ([]byte, *ExecutionInputLease, error) {
	measure, err := backend.NewBuildBounds(0, DefaultExecutionInputBytes)
	if err != nil {
		return nil, nil, err
	}
	// Measurement borrows strings but can inspect the codec's entire fixed
	// reflection node arena. Fund it before walking a typed caller graph.
	header := measure.Snapshot().NodeLimit*backend.MaxNodeAllocationBytes + 4096
	l, err := NewExecutionInputLease(ctx, header)
	if err != nil {
		return nil, nil, err
	}
	var raw []byte
	l.clear = func() { clear(raw); raw = nil }
	err = l.RunBuild(ctx, func() error {
		n, err := measure.MeasureJSON(payload)
		if err != nil {
			return err
		}
		if n < 0 || n > 6*DefaultExecutionInputBytes {
			return backend.ErrBuildCapacity
		}
		b, arena, err := executionOutputBounds(n)
		if err != nil {
			return err
		}
		if err = l.RetainPayload(arena); err != nil {
			return err
		}
		raw, err = b.EncodeJSON(payload, n)
		return err
	})
	if err != nil {
		l.Close()
		return nil, nil, err
	}
	// The short build has retired measurement/sort/reflection scratch. Only
	// the exact capacity frame is still referenced by the network writer.
	allocation, err := executionOutputAllocationCredit(int64(cap(raw)))
	if err != nil {
		l.Close()
		return nil, nil, err
	}
	if err = l.ReleasePayload(l.usage.PayloadBytes - allocation); err != nil {
		l.Close()
		return nil, nil, err
	}
	return raw, l, nil
}

func executionOutputAllocationCredit(n int64) (int64, error) {
	// ReserveText is the shared codec's checked Go allocation-size contract;
	// cap alone omits minimum classes, malloc headers and page rounding.
	b, _, err := executionOutputBounds(n)
	if err != nil {
		return 0, err
	}
	if err = b.ReserveText(n, n); err != nil {
		return 0, err
	}
	return b.Snapshot().TextBytes, nil
}

func executionOutputBounds(n int64) (*backend.BuildBounds, int64, error) {
	if n < 0 || n > 6*DefaultExecutionInputBytes || n > math.MaxInt64/4 {
		return nil, 0, backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBounds(n, max(int64(4096), 4*n))
	if err != nil {
		return nil, 0, err
	}
	s := b.Snapshot()
	nodes := s.NodeLimit * backend.MaxNodeAllocationBytes
	if n > math.MaxInt64/2 || s.TextLimit > math.MaxInt64-2*n || nodes > math.MaxInt64-2*n-s.TextLimit {
		return nil, 0, backend.ErrBuildCapacity
	}
	return b, 2*n + s.TextLimit + nodes, nil
}
func newExecutionInputLease(ctx context.Context, bytes int64, prepare bool) (*ExecutionInputLease, error) {
	s := executionInputState(ctx)
	maximum, _ := ExecutionInputAllocationBound(DefaultExecutionInputBytes, 65536)
	if bytes < 0 || bytes > maximum {
		return nil, backend.ErrBuildCapacity
	}
	s.mu.Lock()
	full := len(s.leases) >= 65536
	s.mu.Unlock()
	if full {
		return nil, backend.ErrBuildCapacity
	}
	l := &ExecutionInputLease{state: s, id: fmt.Sprintf("input:%d:%s:%d", s.scope.JobID, s.scope.CallID, inputSequence.Add(1)), usage: resourcebudget.Vector{PayloadBytes: max(int64(1), bytes)}}
	if prepare {
		l.usage.PrepareOwners = 1
	}
	if s.root != nil {
		p := s.root.Snapshot(s.scope.ActorID).Policy
		s.mu.Lock()
		own := s.payload
		s.mu.Unlock()
		if p.Actor.MaxPayloadBytes > 0 && bytes > p.Actor.MaxPayloadBytes-own ||
			p.Instance.MaxPayloadBytes > 0 && bytes > p.Instance.MaxPayloadBytes-own {
			return nil, backend.ErrBuildCapacity
		}
	}
	if s.root != nil {
		if err := s.root.Claim(s.scope, l.id, l.usage); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.leases = append(s.leases, l)
	s.payload += l.usage.PayloadBytes
	s.mu.Unlock()
	return l, nil
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
		_ = l.state.root.Transfer(l.state.scope, l.id, resourcebudget.Vector{})
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
}

// RetainPayload increases credit before an append or copy; callers clear the
// corresponding backing before Close. Only this temporary identity is changed.
func (l *ExecutionInputLease) RetainPayload(bytes int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return resourcebudget.ErrIdentity
	}
	maximum, _ := ExecutionInputAllocationBound(DefaultExecutionInputBytes, 65536)
	if bytes < 0 || bytes > maximum-l.usage.PayloadBytes {
		return backend.ErrBuildCapacity
	}
	next := l.usage
	next.PayloadBytes += bytes
	if l.state.root != nil {
		if err := l.state.root.Transfer(l.state.scope, l.id, next); err != nil {
			return err
		}
	}
	l.state.mu.Lock()
	l.state.payload += bytes
	l.state.mu.Unlock()
	l.usage = next
	return nil
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
		if err := l.state.root.Transfer(l.state.scope, l.id, next); err != nil {
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
			if err := l.state.root.Transfer(l.state.scope, l.id, next); err != nil {
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
		l.usage.BuildWorkers = 0
		if l.state.root != nil {
			_ = l.state.root.Transfer(l.state.scope, l.id, l.usage)
		}
	}()
	return build()
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
		if err := l.state.root.Transfer(l.state.scope, l.id, next); err != nil {
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
		Count int64
		Bytes int64
	}
	err = c.Segment.Query().Where(segment.ResourceIDEQ(resourceID)).Select(segment.FieldID).Modify(func(s *sql.Selector) {
		expr := inputTextLength(s, segment.FieldSourceText) + "+" + inputTextLength(s, segment.FieldTargetText) + "+" + inputTextLength(s, segment.FieldMeta) + "+" + inputTextLength(s, segment.FieldQualityIssues) + "+" + inputTextLength(s, segment.FieldStatus)
		s.Select("COUNT(*) AS count", "COALESCE(SUM("+expr+"),0) AS bytes")
	}).Scan(ctx, &stats)
	if err != nil {
		return nil, nil, err
	}
	if len(stats) != 1 {
		return nil, nil, backend.ErrBuildCapacity
	}
	st := stats[0]
	if st.Count < 0 || st.Count > 65536 || st.Count > int64(math.MaxInt) || st.Bytes < 0 || st.Bytes > min(executionInputState(ctx).limit, DefaultExecutionInputBytes) {
		return nil, nil, backend.ErrBuildCapacity
	}
	credit, err := ExecutionInputAllocationBound(st.Bytes, st.Count)
	if err != nil {
		return nil, nil, err
	}
	l, err := newExecutionInputLease(ctx, credit, true)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			l.Close()
		}
	}()
	var raw []struct {
		ID             int
		SegmentIndex   int     `json:"segment_index"`
		ContentVersion int64   `json:"content_version"`
		SourceText     string  `json:"source_text"`
		TargetText     *string `json:"target_text"`
		Status         string
		Meta           *string
		Issues         string
	}
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
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	rows := make([]*ent.Segment, len(raw))
	b, err := executionMetadataBounds(st.Bytes)
	if err != nil {
		return nil, nil, err
	}
	l.clear = func() {
		l.state.mu.Lock()
		for _, row := range rows {
			delete(l.state.metadata, row)
		}
		l.state.mu.Unlock()
		clear(rows)
	}
	err = l.RunBuild(ctx, func() error {
		for i, r := range raw {
			var issues []qa.QualityIssue
			var meta map[string]any
			if r.Issues != "" {
				if err := b.DecodeJSONLimited([]byte(r.Issues), &issues, st.Bytes); err != nil {
					return err
				}
			}
			if r.Meta != nil && *r.Meta != "" {
				if err := b.DecodeJSONLimited([]byte(*r.Meta), &meta, st.Bytes); err != nil {
					return err
				}
			}
			rows[i] = &ent.Segment{ID: r.ID, ResourceID: &resourceID, SegmentIndex: r.SegmentIndex, ContentVersion: r.ContentVersion, SourceText: r.SourceText, TargetText: r.TargetText, Status: segment.Status(r.Status), Meta: r.Meta, QualityIssues: issues}
			state := l.state
			state.mu.Lock()
			if state.metadata == nil {
				state.metadata = make(map[*ent.Segment]map[string]any)
			}
			state.metadata[rows[i]] = meta
			state.mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	clear(raw)
	raw = nil
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

// ExecutionSegmentMeta reuses the graph validated during the snapshot read.
// Direct runner fixtures are still decoded through a bounded codec.
func ExecutionSegmentMeta(ctx context.Context, row *ent.Segment) (map[string]any, error) {
	s := executionInputState(ctx)
	s.mu.Lock()
	m, ok := s.metadata[row]
	s.mu.Unlock()
	if ok {
		return m, nil
	}
	if row.Meta == nil || *row.Meta == "" {
		return nil, nil
	}
	b, err := executionMetadataBounds(int64(len(*row.Meta)))
	if err != nil {
		return nil, err
	}
	err = b.DecodeJSONLimited([]byte(*row.Meta), &m, min(s.limit, DefaultExecutionInputBytes))
	return m, err
}

// Fixed codec field-cache records and short JSON scalars need a finite floor.
// Four input bytes of construction credit per observed UTF-8 byte provide the
// existing codec's declared node/text bounds; this exact bound is preclaimed.
func executionMetadataBounds(bytes int64) (*backend.BuildBounds, error) {
	if bytes < 0 || bytes > DefaultExecutionInputBytes {
		return nil, backend.ErrBuildCapacity
	}
	return backend.NewBuildBounds(0, max(int64(4096), 4*bytes))
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
	if len(stats) != 1 || stats[0].Count < 0 || stats[0].Count > 65536 || stats[0].Bytes < 0 || stats[0].Bytes > min(executionInputState(ctx).limit, DefaultExecutionInputBytes) {
		return nil, nil, backend.ErrBuildCapacity
	}
	st := stats[0]
	credit, err := ExecutionInputAllocationBound(st.Bytes, st.Count)
	if err != nil {
		return nil, nil, err
	}
	l, err := newExecutionInputLease(ctx, credit, true)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			l.Close()
		}
	}()
	var raw []struct {
		Source        string
		Target        string
		Notes         string
		CaseSensitive bool `json:"case_sensitive"`
		Forbidden     bool
		Mandatory     bool
	}
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
		g.mu.Lock()
		defer g.mu.Unlock()
		clear(g.entries)
		g.entries = nil
		g.head = nil
		if g.database != nil {
			g.database.mu.Lock()
			clear(g.database.entries)
			g.database.entries = nil
			clear(g.database.lookupCache)
			g.database.entrySnapshot.Store(nil)
			g.database.mu.Unlock()
		}
	}
	err = l.RunBuild(ctx, func() error {
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
	if err = l.Prepare(false); err != nil {
		return nil, nil, err
	}
	ok = true
	return g, l, nil
}
func NewMemoryExecutionGlossary(ctx context.Context) (glossary.Glossary, error) {
	l, err := NewExecutionInputLease(ctx, 1)
	if err != nil {
		return nil, err
	}
	g := &executionGlossary{lease: l}
	l.clear = func() { g.mu.Lock(); clear(g.entries); g.entries = nil; g.head = nil; g.mu.Unlock() }
	return g, nil
}

type executionGlossary struct {
	mu       sync.RWMutex
	lease    *ExecutionInputLease
	entries  []glossary.Entry
	head     *glossary.EntrySnapshot
	database *DatabaseGlossary
	bytes    int64
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
	b, err := backend.NewBuildBounds(0, DefaultExecutionInputBytes)
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
		b, err := backend.NewBuildBounds(limit, int64(len(text)))
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
	g.mu.Lock()
	defer g.mu.Unlock()
	var result glossary.AddResult
	// Growth is checked before normalization, append, returned conflict records,
	// and persistent ORM writes. Old immutable snapshots remain funded.
	if len(entries) > 65536-len(g.entries) {
		return result, backend.ErrBuildCapacity
	}
	var n int64
	for _, e := range entries {
		v := int64(len(e.Source) + len(e.Target) + len(e.Notes))
		if v > DefaultExecutionInputBytes-g.bytes-n {
			return result, backend.ErrBuildCapacity
		}
		n += v
	}
	if err := g.lease.RetainPayload(4*n + int64(len(entries))*6*backend.MaxNodeAllocationBytes); err != nil {
		return result, err
	}
	g.bytes += n
	if g.database != nil {
		r, err := g.database.Add(ctx, entries...)
		g.entries = g.database.entries
		return r, err
	}
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
		g.head = &glossary.EntrySnapshot{Entry: e, Order: len(g.entries), Previous: g.head}
		g.entries = append(g.entries, e)
		result.Added = append(result.Added, e)
	}
	return result, nil
}
