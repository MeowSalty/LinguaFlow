package pipeline

import (
	"log/slog"
	"math"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type stagedPoolProgress struct {
	pool, maxPools, batches int
	shrink                  float64
}

func (p *stagedPoolProgress) start(s *mainScheduler, group int, reporter progress.Reporter, logger *slog.Logger) {
	pool := s.groups[group].pool
	if pool == p.pool {
		return
	}
	mode := s.h.ModeName()
	if observer, ok := reporter.(progress.PoolObserver); ok {
		pending, batches := s.poolPlanCounts(group)
		if p.pool >= 0 && pool == p.pool+1 {
			observer.OnPoolEvent(progress.PoolEvent{Mode: mode, PoolIndex: p.pool, MaxPools: p.maxPools, Batches: p.batches, Pending: pending, ShrinkRate: p.shrink, Phase: "pool_advance"})
		}
		observer.OnPoolEvent(progress.PoolEvent{Mode: mode, PoolIndex: pool, MaxPools: p.maxPools, Batches: batches, Pending: pending, ShrinkRate: p.shrink, Phase: "pool_start"})
		p.batches = batches
	}
	if p.pool >= 0 && pool == p.pool+1 {
		logger.Info("advancing to next shrink pool", "pool", pool, "shrink", p.shrink)
	}
	logger.Info("running shrink pool", "mode", mode, "pool", pool, "shrink", p.shrink)
	p.pool = pool
}

// poolPlanCounts counts the logical plan once as a pool starts. On resume it
// counts only main work still to be planned. Response and candidate work has
// a plan already, and runtime capacity can shorten later dispatches.
func (s *mainScheduler) poolPlanCounts(group int) (pending, batches int) {
	pool := s.groups[group].pool
	constraint := s.constraint(group)
	window, span := 0, 0
	translate := false
	switch h := s.h.(type) {
	case *TranslateHandler:
		translate = true
		if h.Context.Enabled {
			window = max(h.Context.Before, h.Context.After)
		}
	case *ReviseHandler:
		span = h.MaxBatchIndexSpan
	}
	useContextWords := window > 0 && constraint.MaxWords > 0
	count, words, first, previous, interior := 0, 0, 0, -1, 0
	for i := range s.nodes {
		n := &s.nodes[i]
		w := s.doc.Segments[n.index].RoundWork
		cur := w.Cursor
		if w.Completed || cur.Pool != pool || cur.State == "unresolved" || cur.State == "candidate" || cur.State == "response_pending" {
			continue
		}
		pending++
		idx := n.index
		next := scheduleSum(n.sourceWords, n.targetWords)
		gap := idx - previous
		split := count == 0 || constraint.exceeds(count, words, next) || span > 0 && idx-first > span ||
			translate && window < (math.MaxInt-1)/2 && gap > 2*window+1
		if !split && useContextWords {
			interior = scheduleSum(interior, s.contextRange(previous+1, idx))
			tail := idx + 1 + min(window, len(s.doc.Segments)-idx-1)
			contextWords := scheduleSum(interior, s.contextRange(idx+1, tail))
			split = contextWords > constraint.MaxWords || words > constraint.MaxWords-contextWords || next > constraint.MaxWords-contextWords-words
		}
		if split {
			batches++
			count, words, first, interior = 0, 0, idx, 0
			if useContextWords {
				interior = s.contextRange(max(0, idx-window), idx)
			}
		}
		count++
		words = scheduleSum(words, next)
		previous = idx
	}
	return pending, batches
}

// Track only live batches issued by this coordinator. A saved response can be
// recovered separately from its candidates, and recovered members continue to
// report their confirmed segment progress without replaying batch completion.
type stagedBatchProgress map[string]stagedBatchMembers

type stagedBatchMembers struct {
	remaining int
	retrying  bool
}

func (p *stagedBatchProgress) start(header MainResponseHeader) {
	if *p == nil {
		*p = make(stagedBatchProgress)
	}
	(*p)[header.ID] = stagedBatchMembers{remaining: header.MemberCount}
}

func (p stagedBatchProgress) finish(reporter progress.Reporter, id string, count int, retrying bool) {
	members, ok := p[id]
	if !ok {
		return
	}
	members.remaining -= count
	members.retrying = members.retrying || retrying
	if members.remaining == 0 {
		delete(p, id)
		if !members.retrying {
			reporter.BatchComplete()
		}
		return
	}
	p[id] = members
}
