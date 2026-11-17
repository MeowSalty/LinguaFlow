package pipeline

import (
	"context"
	"reflect"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// DocumentAllocationPlan describes the private backing installed in one slot.
// Borrowed fields retain their previous owner's credit instead of moving it.
type DocumentAllocationPlan struct{ TargetBytes, IssuesBytes, MetaBytes, ProtectedBytes int64 }

const (
	BorrowTarget uint8 = 1 << iota
	BorrowIssues
	BorrowMeta
	BorrowProtected
)

type documentAllocationOwnerKey struct{}
type DocumentAllocationOwner func(context.Context, int, string, DocumentAllocationPlan, uint8, func()) error

func WithDocumentAllocationOwner(ctx context.Context, owner DocumentAllocationOwner) context.Context {
	return context.WithValue(ctx, documentAllocationOwnerKey{}, owner)
}

// retainedBacking visits already-owned values without allocating clones or
// charging the visit. Inline fields are paid by their enclosing object. A
// restored JSON document is an acyclic private tree; typed metadata restoration
// must preserve that contract. Borrowed fields do not enter this traversal.
func retainedBacking(ctx context.Context, b *backend.BuildBounds, v reflect.Value, depth int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !v.IsValid() {
		return 0, nil
	}
	if depth >= backend.MaxBuildDepth {
		return 0, backend.ErrBuildCapacity
	}
	var total int64
	add := func(n int64, e error) error {
		if e != nil {
			return e
		}
		var err error
		total, err = backend.CheckedAdd(total, n)
		return err
	}
	child := func(x reflect.Value) error { return add(retainedBacking(ctx, b, x, depth+1)) }
	switch v.Kind() {
	case reflect.String:
		return backend.AllocationBytes(int64(v.Len()), 1)
	case reflect.Interface:
		if v.IsNil() {
			return 0, nil
		}
		x := v.Elem()
		if err := child(x); err != nil {
			return 0, err
		}
	case reflect.Pointer:
		if v.IsNil() {
			return 0, nil
		}
		if err := add(backend.AllocationBytes(1, int64(v.Type().Elem().Size()))); err != nil {
			return 0, err
		}
		if err := child(v.Elem()); err != nil {
			return 0, err
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice {
			if v.IsNil() {
				return 0, nil
			}
			if err := add(backend.AllocationBytes(int64(v.Cap()), int64(v.Type().Elem().Size()))); err != nil {
				return 0, err
			}
		}
		for i := 0; i < v.Len(); i++ {
			if err := child(v.Index(i)); err != nil {
				return 0, err
			}
		}
	case reflect.Map:
		if v.IsNil() {
			return 0, nil
		}
		keyCredit, err := b.OwnAllocation(1, int64(v.Type().Key().Size()))
		if err != nil {
			return 0, err
		}
		defer keyCredit.Release()
		valueCredit, err := b.OwnAllocation(1, int64(v.Type().Elem().Size()))
		if err != nil {
			return 0, err
		}
		defer valueCredit.Release()
		key, value := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		it := v.MapRange()
		for it.Next() {
			key.SetIterKey(it)
			value.SetIterValue(it)
			if err := child(key); err != nil {
				return 0, err
			}
			if err := child(value); err != nil {
				return 0, err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := child(v.Field(i)); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}
func installConfirmedFields(ctx context.Context, runtime *ExecutionRuntime, doc *Document, c *Candidate, result TranslatedSegment, prepared bool, owner string, previous Segment, afterInstall func()) error {
	ctx = runtime.BuildContext(ctx, owner)
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return err
	}
	var plan DocumentAllocationPlan
	borrowed := uint8(0)
	old := &doc.Segments[c.Index]
	if unsafe.StringData(result.TargetText) == unsafe.StringData(previous.Target) && len(result.TargetText) == len(previous.Target) {
		borrowed |= BorrowTarget
	}
	if !prepared && unsafe.SliceData(result.Issues) == unsafe.SliceData(previous.Issues) && cap(result.Issues) == cap(previous.Issues) {
		borrowed |= BorrowIssues
	}
	if !prepared && reflect.ValueOf(result.Meta).UnsafePointer() == reflect.ValueOf(previous.Meta).UnsafePointer() {
		borrowed |= BorrowMeta
	}
	if !prepared && reflect.ValueOf(result.Protected).UnsafePointer() == reflect.ValueOf(previous.Protected).UnsafePointer() {
		borrowed |= BorrowProtected
	}
	values := []struct {
		bit   uint8
		value any
		dst   *int64
	}{{BorrowTarget, result.TargetText, &plan.TargetBytes}, {BorrowIssues, result.Issues, &plan.IssuesBytes}, {BorrowMeta, result.Meta, &plan.MetaBytes}, {BorrowProtected, result.Protected, &plan.ProtectedBytes}}
	for _, field := range values {
		if borrowed&field.bit != 0 {
			continue
		}
		n, err := retainedBacking(ctx, b, reflect.ValueOf(field.value), 0)
		if err != nil {
			return err
		}
		*field.dst = n
	}
	install := func() {
		old.Target = result.TargetText
		old.Issues = result.Issues
		old.Meta = result.Meta
		old.Protected = result.Protected
		old.Status = c.Segment.Status
		old.ContentVersion = c.Segment.ContentVersion
		old.TargetIsNull = false
		previous = Segment{}
		if afterInstall != nil {
			afterInstall()
		}
	}
	ctx = WithExecutionRuntime(ctx, runtime)
	if receiver, _ := ctx.Value(documentAllocationOwnerKey{}).(DocumentAllocationOwner); receiver != nil {
		return receiver(ctx, c.Index, owner, plan, borrowed, install)
	}
	return runtime.installDocumentBacking(b, doc, c.Index, owner, plan, borrowed, install)
}
