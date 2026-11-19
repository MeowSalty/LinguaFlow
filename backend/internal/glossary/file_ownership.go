package glossary

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// BindAllocationOwner adopts this standalone dictionary into the existing
// request admission ledger. It is a dictionary lifetime, independent of any
// promotion build. Close follows all dictionary users and returned borrowers.
func (g *FileGlossary) BindAllocationOwner(ctx context.Context, a *backend.RequestAdmission) error {
	g.retirement.Lock()
	defer g.retirement.Unlock()
	g.mutation.Lock()
	defer g.mutation.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("glossary: closed")
	}
	if g.allocationAdmission != nil {
		if g.allocationAdmission != a {
			return backend.ErrBuildCapacity
		}
		return nil
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	owner := "file-glossary:" + hex.EncodeToString(id[:])
	b, err := backend.NewBuildBoundsFromContext(backend.WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuildBounded(owner, n, backend.BuildAllocationLimit(ctx)) }, func(n int64) { _ = a.ReleaseBuildBytes(owner, n) }), 0, 0)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			_ = a.ReleaseBuild(owner)
		}
	}()
	g.entryAllocation, err = backend.AllocationBytes(int64(cap(g.entries)), int64(unsafe.Sizeof(Entry{})))
	if err != nil {
		return err
	}
	if err = b.ReserveAllocation(g.entryAllocation, 1); err != nil {
		return err
	}
	if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(FileGlossary{}))); err != nil {
		return err
	}
	for _, entry := range g.entries {
		for _, text := range []string{entry.Source, entry.Target, entry.Notes} {
			if err = b.ReserveAllocation(int64(len(text)), 1); err != nil {
				return err
			}
		}
	}
	for key := range g.bySource {
		if err = b.ReserveAllocation(int64(len(key)), 1); err != nil {
			return err
		}
	}
	for node := g.entrySnapshot.Load(); node != nil; node = node.Previous {
		if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(EntrySnapshot{}))); err != nil {
			return err
		}
	}
	g.allocationAdmission, g.allocationOwner = a, owner
	installed = true
	return nil
}

func (g *FileGlossary) dictionaryBounds(ctx context.Context) (*backend.BuildBounds, error) {
	if g.allocationAdmission != nil {
		ctx = backend.WithBuildReservation(ctx, func(n int64) error {
			return g.allocationAdmission.ClaimBuildBounded(g.allocationOwner, n, backend.BuildAllocationLimit(ctx))
		}, func(n int64) { _ = g.allocationAdmission.ReleaseBuildBytes(g.allocationOwner, n) })
	}
	return backend.NewBuildBoundsFromContext(ctx, 0, 0)
}

func dictionaryKeyBounded(b *backend.BuildBounds, e Entry) (string, error) {
	source := strings.TrimSpace(e.Source)
	size := int64(2)
	for _, r := range source {
		n := utf8.RuneLen(unicode.ToLower(r))
		if n < 0 {
			n = 3
		}
		var err error
		size, err = backend.CheckedAdd(size, int64(n))
		if err != nil {
			return "", err
		}
	}
	if e.Forbidden {
		var err error
		size, err = backend.CheckedAdd(size, 1+int64(len(e.Target)))
		if err != nil {
			return "", err
		}
	}
	if err := b.ReserveAllocation(size, 1); err != nil {
		return "", err
	}
	var result strings.Builder
	result.Grow(int(size))
	if e.Forbidden {
		result.WriteString("F:")
	} else {
		result.WriteString("R:")
	}
	for _, r := range source {
		result.WriteRune(unicode.ToLower(r))
	}
	if e.Forbidden {
		result.WriteByte(0)
		result.WriteString(e.Target)
	}
	return result.String(), nil
}

// preparedFileEntries holds all allocations before either business endpoint is
// visible. Install is called under g.mu and contains only pointer assignments.
type preparedFileEntries struct {
	install  func()
	rollback func() error
	retire   func() error
}

func (g *FileGlossary) applyOwnedEntriesLocked(ctx context.Context, entries []Entry) error {
	p, err := g.prepareOwnedEntriesLocked(ctx, entries)
	if err != nil {
		return err
	}
	p.install()
	return p.retire()
}

func (g *FileGlossary) prepareOwnedEntriesLocked(ctx context.Context, entries []Entry) (preparedFileEntries, error) {
	if len(entries) == 0 {
		return preparedFileEntries{install: func() {}, rollback: func() error { return nil }, retire: func() error { return nil }}, nil
	}
	sourceAdmission, sourceOwner := backend.BuildOwnership(ctx)
	fromSource := g.allocationAdmission != nil && sourceAdmission == g.allocationAdmission && sourceOwner != "" && sourceOwner != g.allocationOwner
	var b *backend.BuildBounds
	var err error
	if fromSource {
		b, err = backend.NewBuildBoundsFromContext(ctx, 0, 0)
	} else {
		b, err = g.dictionaryBounds(ctx)
	}
	if err != nil {
		return preparedFileEntries{}, err
	}
	before := int64(0)
	if g.allocationAdmission != nil {
		before = g.allocationAdmission.BuildBytes(g.allocationOwner)
	}
	installed := false
	var next []Entry
	var keys map[string]int
	var head *EntrySnapshot
	defer func() {
		if !installed {
			next, keys, head = nil, nil, nil
			if fromSource {
				b.CloseRetired()
			} else if g.allocationAdmission != nil {
				_ = g.allocationAdmission.ResizeBuild(g.allocationOwner, before)
			}
		}
	}()
	count64, err := backend.CheckedAdd(int64(len(g.entries)), int64(len(entries)))
	if err != nil {
		return preparedFileEntries{}, err
	}
	count := int(count64)
	if err = backend.ReserveSlice[Entry](b, int64(count)); err != nil {
		return preparedFileEntries{}, err
	}
	next = make([]Entry, len(g.entries), count)
	copy(next, g.entries)
	keys = make(map[string]int, count)
	for key, index := range g.bySource {
		keys[key] = index
	}
	type orderedEntry struct {
		entry Entry
		key   string
	}
	orderCredit, err := b.OwnAllocation(int64(count), int64(unsafe.Sizeof(orderedEntry{})))
	if err != nil {
		return preparedFileEntries{}, err
	}
	order := make([]orderedEntry, count)
	defer func() { order = nil; orderCredit.Release() }()
	for key, index := range g.bySource {
		order[index] = orderedEntry{g.entries[index], key}
	}
	head = g.entrySnapshot.Load()
	for _, entry := range entries {
		var owned Entry
		if err = b.CloneInto(entry, &owned); err != nil {
			return preparedFileEntries{}, err
		}
		key, e := dictionaryKeyBounded(b, owned)
		if e != nil {
			return preparedFileEntries{}, e
		}
		if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(EntrySnapshot{}))); err != nil {
			return preparedFileEntries{}, err
		}
		next = append(next, owned)
		keys[key] = len(next) - 1
		order[len(next)-1] = orderedEntry{owned, key}
		head = &EntrySnapshot{Entry: owned, Order: len(next), Previous: head}
	}
	slices.SortStableFunc(order, func(a, c orderedEntry) int { return len(c.entry.Source) - len(a.entry.Source) })
	// Reuse immutable keys; rebuilding index values adds no string backing.
	for i, entry := range order {
		next[i] = entry.entry
		keys[entry.key] = i
	}
	oldEntries := g.entryAllocation
	nextAllocation, err := backend.AllocationBytes(int64(cap(next)), int64(unsafe.Sizeof(Entry{})))
	if err != nil {
		return preparedFileEntries{}, err
	}
	// Retire the sort scratch before identifying the actual persistent backing.
	order = nil
	orderCredit.Release()
	var transfer *backend.PreparedAllocationTransfer
	var receipt *backend.OwnedAllocation
	if fromSource {
		receipt, err = b.RetainedAllocation(b.Snapshot().LiveBytes)
		if err != nil {
			return preparedFileEntries{}, err
		}
		transfer, err = backend.PrepareBuildAllocationTransfer(ctx, receipt, g.allocationOwner)
		if err != nil {
			receipt.Release()
			return preparedFileEntries{}, err
		}
	}
	prepared := preparedFileEntries{
		install: func() {
			g.entries, g.bySource = next, keys
			g.entrySnapshot.Store(head)
			g.entryAllocation = nextAllocation
			g.dirty = true
		},
		rollback: func() error {
			next, keys, head = nil, nil, nil
			if transfer != nil {
				if err := transfer.Rollback(); err != nil {
					return err
				}
				receipt.Release()
				b.CloseRetired()
			} else if g.allocationAdmission != nil {
				return g.allocationAdmission.ResizeBuild(g.allocationOwner, before)
			}
			return nil
		},
		retire: func() error {
			if transfer != nil {
				transfer.Commit()
				b.CloseRetired()
			}
			if g.allocationAdmission != nil {
				return g.allocationAdmission.ReleaseBuildBytes(g.allocationOwner, oldEntries)
			}
			return nil
		},
	}
	installed = true // Ownership belongs to prepared until Install or Rollback.
	return prepared, nil
}
func (g *FileGlossary) Close() error {
	g.retirement.Lock()
	defer g.retirement.Unlock()
	g.mutation.Lock()
	g.mu.Lock()
	g.closed = true
	g.entries = nil
	g.bySource = nil
	g.entrySnapshot.Store(nil)
	g.mu.Unlock()
	g.mutation.Unlock()
	if g.allocationAdmission == nil {
		return nil
	}
	result := g.allocationAdmission.ReleaseBuild(g.allocationOwner)
	if result == nil {
		g.allocationAdmission = nil
		g.allocationOwner = ""
	}
	return result
}
