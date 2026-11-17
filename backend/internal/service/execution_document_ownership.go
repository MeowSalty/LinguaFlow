package service

import (
	"context"
	"math"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// ExecutionDocumentAllocation describes private backing retained by one slot.
// Source and any fields borrowed from another owner are excluded.
type ExecutionDocumentAllocation struct {
	TargetBytes, IssuesBytes, MetaBytes, ProtectedBytes int64
}

const (
	DocumentBorrowTarget uint8 = 1 << iota
	DocumentBorrowIssues
	DocumentBorrowMeta
	DocumentBorrowProtected
)

type executionDocumentSlot struct {
	allocation ExecutionDocumentAllocation
	row        *ent.Segment
	metadata   *executionMetadataEntry
	loaded     uint8
}

func (p ExecutionDocumentAllocation) total() (int64, error) {
	var n int64
	for _, field := range []int64{p.TargetBytes, p.IssuesBytes, p.MetaBytes, p.ProtectedBytes} {
		if field < 0 {
			return 0, resourcebudget.ErrInvalid
		}
		var err error
		n, err = backend.CheckedAdd(n, field)
		if err != nil {
			return 0, err
		}
	}
	return n, nil
}

// InitializeBorrowedDocumentSlots gives a document whose initial fields belong
// to an HTTP/SQL input owner a private, finite replacement index. It acquires no
// second credit for the borrowed initial fields.
func (l *ExecutionInputLease) InitializeBorrowedDocumentSlots(ctx context.Context, count int) error {
	if count < 0 {
		return resourcebudget.ErrInvalid
	}
	credit, err := backend.AllocationBytes(int64(count), int64(unsafe.Sizeof(executionDocumentSlot{})))
	if err != nil {
		return err
	}
	if err = checkExecutionAllocationPlan(ctx, credit, "document_slots"); err != nil {
		return err
	}
	if err = l.RetainExplicitAllocation(ctx, credit); err != nil {
		return err
	}
	l.mu.Lock()
	if l.closed || l.slots != nil {
		l.mu.Unlock()
		_ = l.ReleasePayload(credit)
		return resourcebudget.ErrIdentity
	}
	slots := make([]executionDocumentSlot, count)
	l.slots = slots
	oldClear := l.clear
	l.clear = func() {
		if oldClear != nil {
			oldClear()
		}
		clear(slots)
	}
	l.mu.Unlock()
	return nil
}

// InitializeDocumentSlots indexes the already funded canonical row backing.
// Its typed index is claimed before make; reflection measurement has a separate
// finite owner and completes outside a build worker.
func (l *ExecutionInputLease) InitializeDocumentSlots(ctx context.Context, rows []*ent.Segment) error {
	credit, err := backend.AllocationBytes(int64(len(rows)), int64(unsafe.Sizeof(executionDocumentSlot{})))
	if err != nil {
		return err
	}
	if err = checkExecutionAllocationPlan(ctx, credit, "document_slots"); err != nil {
		return err
	}
	if err = l.RetainExplicitAllocation(ctx, credit); err != nil {
		return err
	}
	slots := make([]executionDocumentSlot, len(rows))
	complete := false
	defer func() {
		if !complete {
			clear(slots)
			slots = nil
			_ = l.ReleasePayload(credit)
		}
	}()
	for i, row := range rows {
		if row == nil {
			return resourcebudget.ErrInvalid
		}
		slot := &slots[i]
		slot.row = row
		slot.loaded = DocumentBorrowTarget | DocumentBorrowIssues | DocumentBorrowMeta
		if row.TargetText != nil {
			pointer, e := backend.AllocationBytes(1, int64(unsafe.Sizeof("")))
			if e != nil {
				return e
			}
			text, e := backend.AllocationBytes(int64(len(*row.TargetText)), 1)
			if e != nil {
				return e
			}
			slot.allocation.TargetBytes, err = backend.CheckedAdd(pointer, text)
			if err != nil {
				return err
			}
		}
		slot.allocation.IssuesBytes, err = measureExecutionAllocation(ctx, row.QualityIssues, backend.ValueAllocationBytesFromContext)
		if err != nil {
			return err
		}
		l.state.mu.Lock()
		for owner := l.state.metadata; owner != nil; owner = owner.next {
			if entry := owner.entryFor(row); entry != nil {
				slot.metadata = entry
				break
			}
		}
		l.state.mu.Unlock()
		if slot.metadata != nil {
			slot.allocation.MetaBytes, err = measureExecutionAllocation(ctx, slot.metadata.value, backend.ValueAllocationBytesFromContext)
			if err != nil {
				return err
			}
		}
		if row.Meta != nil {
			pointer, e := backend.AllocationBytes(1, int64(unsafe.Sizeof("")))
			if e != nil {
				return e
			}
			text, e := backend.AllocationBytes(int64(len(*row.Meta)), 1)
			if e != nil {
				return e
			}
			slot.allocation.MetaBytes, err = backend.CheckedAdd(slot.allocation.MetaBytes, pointer)
			if err == nil {
				slot.allocation.MetaBytes, err = backend.CheckedAdd(slot.allocation.MetaBytes, text)
			}
			if err != nil {
				return err
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.slots != nil {
		return resourcebudget.ErrIdentity
	}
	l.slots = slots
	oldClear := l.clear
	l.clear = func() {
		if oldClear != nil {
			oldClear()
		}
		clear(slots)
	}
	complete = true
	return nil
}

// ReplaceOwnedSlot moves only final private fields from a live build owner.
// install must only replace references and cannot fail. All prior borrowers of
// replaced fields must have ended; borrowed bits retain the old field's owner.
func (l *ExecutionInputLease) ReplaceOwnedSlot(slot int, owned ExecutionDocumentAllocation, borrowed uint8, admission *backend.RequestAdmission, owner string, install func()) error {
	if l == nil || admission == nil || install == nil || borrowed > 15 {
		return resourcebudget.ErrInvalid
	}
	added, err := owned.total()
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || slot < 0 || slot >= len(l.slots) {
		return resourcebudget.ErrIdentity
	}
	old := &l.slots[slot]
	next := owned
	retired := int64(0)
	fields := []struct {
		bit     uint8
		prior   int64
		current *int64
	}{
		{DocumentBorrowTarget, old.allocation.TargetBytes, &next.TargetBytes},
		{DocumentBorrowIssues, old.allocation.IssuesBytes, &next.IssuesBytes},
		{DocumentBorrowMeta, old.allocation.MetaBytes, &next.MetaBytes},
		{DocumentBorrowProtected, old.allocation.ProtectedBytes, &next.ProtectedBytes},
	}
	for _, field := range fields {
		if borrowed&field.bit != 0 {
			if *field.current != 0 {
				return resourcebudget.ErrInvalid
			}
			*field.current = field.prior
		} else {
			retired, err = backend.CheckedAdd(retired, field.prior)
			if err != nil {
				return err
			}
		}
	}
	if added > int64(math.MaxInt)-l.usage.PayloadBytes || retired > l.usage.PayloadBytes {
		return resourcebudget.ErrInvalid
	}
	if err = admission.MoveBuildTo(owner, l.state.root, l.state.scope, l.id, added); err != nil {
		return executionCapacityFailure(err, "document_transfer", l.id)
	}
	l.usage.PayloadBytes += added
	l.state.mu.Lock()
	l.state.payload += added
	l.state.mu.Unlock()
	install()
	if old.row != nil {
		if old.loaded&DocumentBorrowTarget != 0 && borrowed&DocumentBorrowTarget == 0 {
			old.row.TargetText = nil
		}
		if old.loaded&DocumentBorrowIssues != 0 && borrowed&DocumentBorrowIssues == 0 {
			old.row.QualityIssues = nil
		}
		if old.loaded&DocumentBorrowMeta != 0 && borrowed&DocumentBorrowMeta == 0 {
			old.row.Meta = nil
			if old.metadata != nil {
				l.state.mu.Lock()
				old.metadata.value = nil
				l.state.mu.Unlock()
			}
		}
	}
	old.loaded &= borrowed
	old.allocation = next
	nextUsage := l.usage
	nextUsage.PayloadBytes -= retired
	if l.state.root != nil {
		if err = l.syncRootLocked(nextUsage); err != nil {
			// Keep both credits on an exceptional Root failure after installation.
			// Close remains the authoritative owner release after all users join.
			return executionCapacityFailure(err, "document_retire", l.id)
		}
	}
	l.usage = nextUsage
	l.state.mu.Lock()
	l.state.payload -= retired
	l.state.mu.Unlock()
	return nil
}
