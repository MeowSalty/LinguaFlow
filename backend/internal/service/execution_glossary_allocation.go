package service

import (
	"context"
	"strings"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type executionGlossaryAllocationKey struct{}
type glossaryAllocation func(bytes, text int64) (func(), error)

func (g *executionGlossary) allocationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, executionGlossaryAllocationKey{}, glossaryAllocation(func(bytes, text int64) (func(), error) {
		if text > DefaultExecutionInputBytes-g.bytes {
			return nil, backend.ErrBuildCapacity
		}
		if err := g.lease.RetainExplicitAllocation(ctx, bytes); err != nil {
			return nil, err
		}
		return func() { _ = g.lease.ReleasePayload(bytes) }, nil
	}))
}

// prepareCachedEntries sizes the actual committed subset and the exact make
// capacity. Database/driver work and map internals are not allocation promises.
func (g *DatabaseGlossary) prepareCachedEntries(ctx context.Context, added []glossary.Entry) ([]glossary.Entry, *glossary.EntrySnapshot, func(), error) {
	if len(added) == 0 {
		return nil, nil, nil, nil
	}
	n, err := backend.CheckedAdd(int64(len(g.entries)), int64(len(added)))
	if err != nil {
		return nil, nil, nil, err
	}
	bytes, err := backend.AllocationBytes(n, int64(unsafe.Sizeof(glossary.Entry{})))
	if err != nil {
		return nil, nil, nil, err
	}
	nodes, err := backend.AllocationBytes(int64(len(added)), int64(unsafe.Sizeof(glossary.EntrySnapshot{})))
	if err != nil {
		return nil, nil, nil, err
	}
	bytes, err = backend.CheckedAdd(bytes, nodes)
	if err != nil {
		return nil, nil, nil, err
	}
	var text int64
	for _, entry := range added {
		for _, field := range []string{entry.Source, entry.Target, entry.Notes} {
			text, err = backend.CheckedAdd(text, int64(len(field)))
			if err != nil {
				return nil, nil, nil, err
			}
		}
	}
	bytes, err = backend.CheckedAdd(bytes, text)
	if err != nil {
		return nil, nil, nil, err
	}
	var refund func()
	if reserve, ok := ctx.Value(executionGlossaryAllocationKey{}).(glossaryAllocation); ok {
		refund, err = reserve(bytes, text)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	next := make([]glossary.Entry, len(g.entries), int(n))
	copy(next, g.entries)
	head := g.entrySnapshot.Load()
	for _, entry := range added {
		entry.Source, entry.Target, entry.Notes = strings.Clone(entry.Source), strings.Clone(entry.Target), strings.Clone(entry.Notes)
		head = &glossary.EntrySnapshot{Entry: entry, Order: len(next), Previous: head}
		next = append(next, entry)
	}
	return next, head, func() {
		clear(next)
		next = nil
		head = nil
		if refund != nil {
			refund()
		}
	}, nil
}
