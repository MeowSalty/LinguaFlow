package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"unsafe"
)

// The overlay's dictionary remains private to its execution lease. Preparation
// never mutates the visible dictionary or a project's database rows.
func (g *executionGlossary) processMemoryResponse(ctx context.Context, in glossary.ResponseProcess) (out glossary.ResponseBusiness, err error) {
	g.retirement.Lock()
	defer g.retirement.Unlock()
	g.mutation.Lock()
	locked := true
	unlockPublication := func() {
		if locked {
			locked = false
			g.mutation.Unlock()
		}
	}
	defer unlockPublication()
	g.lease.mu.Lock()
	closed := g.lease.closed
	g.lease.mu.Unlock()
	if closed {
		return out, errors.New("execution glossary closed")
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, int64(len(in.EntriesPayload)))
	if err != nil {
		return out, err
	}
	defer func() { unlockPublication(); b.CloseRetired() }()
	var result glossary.AddResult
	err = glossary.VisitResponseEntries(ctx, in, func(_ int, entry glossary.Entry) error {
		input, e := normalizeGlossaryEntryInput(GlossaryEntryInput{Source: entry.Source, Target: entry.Target, Notes: entry.Notes, CaseSensitive: entry.CaseSensitive, Forbidden: entry.Forbidden, Mandatory: &entry.Mandatory})
		if e != nil {
			result.Skipped, e = backend.AppendNode(b, result.Skipped, glossary.SkippedEntry{Reason: glossary.SkipReasonEmpty})
			return e
		}
		entry.Source, entry.Target, entry.Notes = input.Source, input.Target, input.Notes
		var owned glossary.Entry
		if e = b.CloneInto(entry, &owned); e != nil {
			return e
		}
		existing, found := findCachedGlossaryEntry(g.entries, input)
		if !found {
			existing, found = findCachedGlossaryEntry(result.Added, input)
		}
		if found {
			if existing.Target != entry.Target {
				result.Skipped, e = backend.AppendNode(b, result.Skipped, glossary.SkippedEntry{Proposed: owned, Existing: existing, Reason: glossary.SkipReasonExists})
			}
			return e
		}
		result.Added, e = backend.AppendNode(b, result.Added, owned)
		return e
	})
	if err != nil {
		return out, err
	}
	defer func() { clear(result.Added); clear(result.Skipped); result = glossary.AddResult{} }()
	cache := &DatabaseGlossary{entries: g.entries}
	cache.entrySnapshot.Store(g.head)
	next, head, settlement, err := cache.prepareCachedEntries(g.allocationContext(ctx), result.Added)
	if err != nil {
		return out, err
	}
	cache.entries = nil
	cache.entrySnapshot.Store(nil)
	var paid, text int64
	if next != nil {
		paid, err = backend.AllocationBytes(int64(cap(next)), int64(unsafe.Sizeof(glossary.Entry{})))
		if err != nil {
			return out, err
		}
		nodes, e := backend.AllocationBytes(int64(len(result.Added)), int64(unsafe.Sizeof(glossary.EntrySnapshot{})))
		if e != nil {
			return out, e
		}
		paid += nodes
		for _, entry := range result.Added {
			text += int64(len(entry.Source) + len(entry.Target) + len(entry.Notes))
		}
		paid += text
	}
	committed, installed := false, false
	rollback := func() error {
		clear(next)
		next = nil
		head = nil
		if committed {
			return g.lease.ReleasePayload(paid)
		}
		if settlement.rollback != nil {
			return settlement.rollback()
		}
		return nil
	}
	defer func() {
		if !installed {
			unlockPublication()
			out.Release()
			err = errors.Join(err, rollback())
		}
	}()
	out, err = glossary.BuildResponseBusiness(ctx, in, result)
	if err != nil {
		return out, err
	}
	if settlement.commit != nil {
		if err = settlement.commit(); err != nil {
			return out, err
		}
	}
	committed = true
	oldBacking := g.entryBacking
	nextBacking := oldBacking
	if next != nil {
		nextBacking, err = backend.AllocationBytes(int64(cap(next)), int64(unsafe.Sizeof(glossary.Entry{})))
		if err != nil {
			return out, err
		}
	}
	prepared := &responsecontract.PreparedPublication{Install: func() {
		if next != nil {
			g.entries = next
			g.head = head
			g.bytes += text
			g.entryBacking = nextBacking
		}
		installed = true
	}, Rollback: rollback, Park: settlement.park, Retire: func() error {
		if next == nil {
			return nil
		}
		return g.lease.ReleasePayload(oldBacking)
	}}
	// Only the final publication holds the reader lock; streamed member reads
	// and build permits precede it. The installation callback only assigns state.
	g.mu.Lock()
	if in.Publish == nil {
		err = prepared.InstallOnce()
	} else {
		err = in.Publish(ctx, out, prepared)
	}
	g.mu.Unlock()
	if !prepared.Installed() {
		if err == nil {
			err = fmt.Errorf("response publisher omitted dictionary installation")
		}
		return out, err
	}
	if prepared.Retire != nil {
		unlockPublication()
		err = errors.Join(err, prepared.Retire())
	}
	return out, err
}
