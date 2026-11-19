package glossary

import (
	"context"
	"errors"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// ProcessResponse prepares the dictionary, member sink and receipt before the
// sink publishes both endpoints under the dictionary/store lock order.
func (g *FileGlossary) ProcessResponse(ctx context.Context, in ResponseProcess) (out ResponseBusiness, err error) {
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
	g.mu.RLock()
	closed := g.closed
	g.mu.RUnlock()
	if closed {
		return out, errors.New("glossary: closed")
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, int64(len(in.EntriesPayload)))
	if err != nil {
		return out, err
	}
	defer func() { unlockPublication(); b.CloseRetired() }()
	var result AddResult
	pending := map[string]Entry{}
	defer func() { result = AddResult{}; pending = nil }()
	err = VisitResponseEntries(ctx, in, func(_ int, entry Entry) error {
		var owned Entry
		if e := b.CloneInto(entry, &owned); e != nil {
			return e
		}
		if entry.Source == "" || entry.Target == "" {
			var e error
			result.Skipped, e = backend.AppendNode(b, result.Skipped, SkippedEntry{Proposed: owned, Reason: SkipReasonEmpty})
			return e
		}
		key, e := dictionaryKeyBounded(b, entry)
		if e != nil {
			return e
		}
		existing, ok := pending[key]
		if !ok {
			if index, found := g.bySource[key]; found {
				existing, ok = g.entries[index], true
			}
		}
		if ok {
			if existing.Target != entry.Target {
				result.Skipped, e = backend.AppendNode(b, result.Skipped, SkippedEntry{Proposed: owned, Existing: Entry{Target: existing.Target}, Reason: SkipReasonExists})
			}
			return e
		}
		pending[key] = owned
		result.Added, e = backend.AppendNode(b, result.Added, owned)
		return e
	})
	if err != nil {
		return out, err
	}
	prepared, err := g.prepareOwnedEntriesLocked(ctx, result.Added)
	if err != nil {
		return out, err
	}
	published := false
	defer func() {
		if !published {
			unlockPublication()
			err = errors.Join(err, prepared.rollback())
			out.Release()
		}
	}()
	out, err = BuildResponseBusiness(ctx, in, result)
	if err != nil {
		return out, err
	}
	if in.Publish != nil {
		install := &responsecontract.PreparedPublication{Install: func() { prepared.install(); published = true }, Rollback: prepared.rollback, Retire: prepared.retire}
		g.mu.Lock()
		err = in.Publish(ctx, out, install)
		g.mu.Unlock()
		if err != nil && !published {
			return out, err
		}
		if !published {
			return out, errors.New("response publisher omitted dictionary installation")
		}
	} else {
		g.mu.Lock()
		prepared.install()
		g.mu.Unlock()
	}
	published = true
	// The authoritative publication already happened. Retiring replaced backing
	// cannot turn that fact into a rollback or release the published receipt.
	unlockPublication()
	err = errors.Join(err, prepared.retire())
	return out, err
}
