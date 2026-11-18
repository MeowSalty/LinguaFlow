package service

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

var _ glossary.ResponseProcessor = (*executionGlossary)(nil)

// ProcessResponse preserves the database adapter's atomic receipt and rewrite
// transaction. Its actual added subset acquires private dictionary backing
// before commit, using the current response credit's formal ownership transfer.
func (g *executionGlossary) ProcessResponse(ctx context.Context, in glossary.ResponseProcess) (glossary.ResponseBusiness, error) {
	if g.database == nil {
		return glossary.ResponseBusiness{}, errors.New("memory execution glossary has no atomic response processor")
	}
	g.mutation.Lock()
	defer g.mutation.Unlock()
	g.lease.mu.Lock()
	closed := g.lease.closed
	g.lease.mu.Unlock()
	if closed {
		return glossary.ResponseBusiness{}, errors.New("execution glossary closed")
	}
	oldCount, oldBacking := len(g.entries), g.entryBacking
	result, processErr := g.database.ProcessResponse(g.allocationContext(ctx), in)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.database.mu.RLock()
	g.entries = g.database.entries
	g.head = g.database.entrySnapshot.Load()
	g.database.mu.RUnlock()
	var retained int64
	for _, entry := range g.entries[oldCount:] {
		for _, text := range []string{entry.Source, entry.Target, entry.Notes} {
			retained += int64(len(text))
		}
	}
	g.bytes += retained
	var settleErr error
	if len(g.entries) != oldCount {
		settleErr = g.retireReplacedBacking(oldBacking)
	}
	if err := errors.Join(processErr, settleErr); err != nil {
		return result, err
	}
	return result, nil
}
