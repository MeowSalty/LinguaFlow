package pipeline

import (
	"errors"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type documentSlot struct {
	document *Document
	index    int
}

func (r *ExecutionRuntime) installDocumentBacking(b *backend.BuildBounds, doc *Document, index int, source string, plan DocumentAllocationPlan, borrowed uint8, install func()) error {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	key := documentSlot{doc, index}
	old := r.documentOwners[key]
	amounts := [4]int64{plan.TargetBytes, plan.IssuesBytes, plan.MetaBytes, plan.ProtectedBytes}
	next := old
	var transfers [4]backend.BuildAllocationTransfer
	count := 0
	for i, amount := range amounts {
		if borrowed&(1<<i) != 0 {
			continue
		}
		next[i] = "document:" + source + ":" + strconv.Itoa(index) + ":" + strconv.Itoa(i)
		if next[i] == old[i] {
			return backend.ErrBuildCapacity
		}
		if amount != 0 {
			transfers[count] = backend.BuildAllocationTransfer{Owner: next[i], Bytes: amount}
			count++
		}
	}
	if count > 0 {
		if err := r.Admission.MoveBuildAllocations(source, transfers[:count]); err != nil {
			return err
		}
	}
	if r.documentOwners == nil {
		r.documentOwners = make(map[documentSlot][4]string)
	}
	install()
	r.documentOwners[key] = next
	var result error
	for i := range old {
		if borrowed&(1<<i) == 0 && old[i] != "" {
			if err := r.Admission.ReleaseBuild(old[i]); err != nil {
				result = errors.Join(result, err)
				r.retainDocumentRefund(key, old[i])
			}
		}
	}
	return result
}

// The backing has ended before a refund is attempted. A failed refund leaves
// only a conservative ledger reservation, not live capacity or borrowing credit.
// Keep that identity so a later close can complete the accounting release.
func (r *ExecutionRuntime) retainDocumentRefund(key documentSlot, owner string) {
	if r.documentPendingRefunds == nil {
		r.documentPendingRefunds = make(map[documentSlot][]string)
	}
	r.documentPendingRefunds[key] = append(r.documentPendingRefunds[key], owner)
}

// CloseDocument ends the in-memory Document's ownership after its consumer has
// finished. An external document lease uses its own equivalent close instead.
func (r *ExecutionRuntime) CloseDocument(doc *Document) error {
	r.ownerMu.Lock()
	defer r.ownerMu.Unlock()
	doc.Segments = nil
	var result error
	for key, owners := range r.documentPendingRefunds {
		if key.document != doc {
			continue
		}
		remaining := owners[:0]
		for _, owner := range owners {
			if err := r.Admission.ReleaseBuild(owner); err != nil {
				result = errors.Join(result, err)
				remaining = append(remaining, owner)
			}
		}
		clear(owners[len(remaining):])
		if len(remaining) == 0 {
			delete(r.documentPendingRefunds, key)
		} else {
			r.documentPendingRefunds[key] = remaining
		}
	}
	for key, fields := range r.documentOwners {
		if key.document != doc {
			continue
		}
		delete(r.documentOwners, key)
		for _, owner := range fields {
			if owner != "" {
				if err := r.Admission.ReleaseBuild(owner); err != nil {
					result = errors.Join(result, err)
					r.retainDocumentRefund(key, owner)
				}
			}
		}
	}
	if len(r.documentOwners) == 0 {
		r.documentOwners = nil
	}
	if len(r.documentPendingRefunds) == 0 {
		r.documentPendingRefunds = nil
	}
	return result
}
