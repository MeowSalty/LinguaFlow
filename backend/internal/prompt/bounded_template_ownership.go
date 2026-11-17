package prompt

import (
	"math"
	"text/template/parse"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// These lists have one private writer and no borrower after replacement.
func appendTemplateValue[T any](b *backend.BuildBounds, values *[]T, owner **backend.OwnedAllocation, value T) error {
	if len(*values) == cap(*values) {
		if cap(*values) > math.MaxInt/2 {
			return backend.ErrBuildCapacity
		}
		capacity := max(1, cap(*values)*2)
		var zero T
		allocation, err := b.OwnAllocation(int64(capacity), int64(unsafe.Sizeof(zero)))
		if err != nil {
			return err
		}
		next := make([]T, len(*values), capacity)
		copy(next, *values)
		old, oldOwner := *values, *owner
		*values, *owner = next, allocation
		clear(old)
		old = nil
		oldOwner.Release()
	}
	*values = append(*values, value)
	return nil
}

type templateTextOwners struct{ retire func() }

func (o *templateTextOwners) release() {
	if o.retire != nil {
		o.retire()
		o.retire = nil
	}
}

// Tree.Copy belongs to the standard parser. Its copied Text backing is public,
// so observe actual cap after copying; other private parser storage is unknown.
func (o *templateTextOwners) hold(b *backend.BuildBounds, node parse.Node) error {
	if node == nil {
		return nil
	}
	switch n := node.(type) {
	case *parse.TextNode:
		if n == nil {
			return nil
		}
		allocation, err := b.OwnAllocation(int64(cap(n.Text)), 1)
		if err != nil {
			return err
		}
		previous := o.retire
		o.retire = func() {
			n.Text = nil
			allocation.Release()
			if previous != nil {
				previous()
			}
		}
	case *parse.ListNode:
		if n != nil {
			for _, child := range n.Nodes {
				if err := o.hold(b, child); err != nil {
					return err
				}
			}
		}
	case *parse.RangeNode:
		if err := o.hold(b, n.List); err != nil {
			return err
		}
		return o.hold(b, n.ElseList)
	case *parse.IfNode:
		if err := o.hold(b, n.List); err != nil {
			return err
		}
		return o.hold(b, n.ElseList)
	case *parse.WithNode:
		if err := o.hold(b, n.List); err != nil {
			return err
		}
		return o.hold(b, n.ElseList)
	}
	return nil
}
