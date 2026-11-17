package pipeline

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"math"
)

type storedBatchPlan struct{ first, last, members, attempt int }

func storedPlannerConstraint(h RoundHandler) (BatchConstraint, int, bool, error) {
	c := BatchConstraint{}
	span, continuous := 0, false
	switch v := h.(type) {
	case *ExtractHandler:
		c = BatchConstraint{MaxSegments: v.BatchSize, MaxWords: v.MaxWordsPerBatch}
		continuous = true
	case *SemanticQAHandler:
		c = BatchConstraint{MaxSegments: v.BatchSize, MaxWords: v.MaxWordsPerBatch}
		c.WordCount = func(s Segment) int {
			a, z := CountWords(s.Source), CountWords(s.Target)
			if a > math.MaxInt-z {
				return math.MaxInt
			}
			return a + z
		}
		span = v.MaxBatchIndexSpan
	case *AdjudicateHandler:
		c = BatchConstraint{MaxSegments: v.BatchSize, MaxWords: v.MaxWordsPerBatch}
		span = v.MaxBatchIndexSpan
	default:
		return c, 0, false, backend.ErrBuildCapacity
	}
	if c.MaxSegments <= 0 && c.MaxWords <= 0 && span <= 0 {
		continuous = false
	}
	return c, span, continuous, nil
}

func storedPlannerEligible(segment Segment, pool, attempt int) bool {
	w := segment.RoundWork
	return w.Member && !w.Completed && w.Cursor.Pool == pool && w.Cursor.Attempt == attempt && w.Cursor.Phase != "terminal_failure" && w.Cursor.State != "unresolved" && w.Cursor.State != "stale" && w.Cursor.State != "rejected"
}

// planStoredCanonicalBatch borrows the canonical inventory and retains only
// scalar run coordinates. It follows the public stored planner before choosing
// any backing size; capacity policy never shortens a public batch.
func planStoredCanonicalBatch(doc *Document, h RoundHandler, pool, budget int) (storedBatchPlan, error) {
	return planStoredCanonicalBatchSkipping(doc, h, pool, budget, nil)
}

func planStoredCanonicalBatchSkipping(doc *Document, h RoundHandler, pool, budget int, skip func(int, int) bool) (storedBatchPlan, error) {
	c, span, continuous, err := storedPlannerConstraint(h)
	if err != nil {
		return storedBatchPlan{}, err
	}
	for attempt := 0; attempt < budget; attempt++ {
		current, best := storedBatchPlan{attempt: attempt}, storedBatchPlan{attempt: attempt}
		previous, words := -1, 0
		selected := false
		finish := func() {
			if current.members > best.members {
				best = current
			}
			current = storedBatchPlan{attempt: attempt}
			words = 0
		}
		for i := range doc.Segments {
			if !storedPlannerEligible(doc.Segments[i], pool, attempt) || skip != nil && skip(i, attempt) {
				continue
			}
			if continuous && current.members > 0 && i > previous+1 {
				finish()
			}
			next := CountWords(doc.Segments[i].Source)
			if c.WordCount != nil {
				next = c.WordCount(doc.Segments[i])
			}
			if current.members > 0 && (c.exceeds(current.members, words, next) || span > 0 && i-current.first > span) {
				selected = true
				break
			}
			if current.members == 0 {
				current.first = i
			}
			current.last = i
			current.members++
			if next > math.MaxInt-words {
				words = math.MaxInt
			} else {
				words += next
			}
			previous = i
		}
		if continuous && !selected {
			finish()
			current = best
		}
		if current.members > 0 {
			return current, nil
		}
	}
	return storedBatchPlan{}, nil
}

// Count completed natural batches without retaining indices. Their order does
// not affect their count: continuous split runs and sorted leftovers have the
// same boundaries. Only ready descriptor count, never member count, is capped
// by C, so a dense natural batch remains complete.
func storedCanonicalDescriptorCount(doc *Document, h RoundHandler, pool, budget, concurrency int) (int, error) {
	c, span, continuous, err := storedPlannerConstraint(h)
	if err != nil {
		return 0, err
	}
	limit := max(1, concurrency)
	count := 0
	for attempt := 0; attempt < budget; attempt++ {
		members, words, first, previous := 0, 0, 0, -1
		for i, seg := range doc.Segments {
			if !storedPlannerEligible(seg, pool, attempt) {
				continue
			}
			next := CountWords(seg.Source)
			if c.WordCount != nil {
				next = c.WordCount(seg)
			}
			if members > 0 && (continuous && i > previous+1 || c.exceeds(members, words, next) || span > 0 && i-first > span) {
				count++
				if count == limit {
					return count, nil
				}
				members, words = 0, 0
			}
			if members == 0 {
				first = i
			}
			members++
			if next > math.MaxInt-words {
				words = math.MaxInt
			} else {
				words += next
			}
			previous = i
		}
		if members > 0 {
			count++
			if count == limit {
				return count, nil
			}
		}
	}
	return count, nil
}

func planStoredCanonicalPage(b *backend.BuildBounds, doc *Document, h RoundHandler, pool, budget, concurrency int) ([]storedBatchPlan, int, error) {
	count, err := storedCanonicalDescriptorCount(doc, h, pool, budget, concurrency)
	if err != nil || count == 0 {
		return nil, 0, err
	}
	if err = backend.ReserveSlice[storedBatchPlan](b, int64(count)); err != nil {
		return nil, 0, err
	}
	plans := make([]storedBatchPlan, 0, count)
	members := 0
	for len(plans) < count {
		plan, err := planStoredCanonicalBatchSkipping(doc, h, pool, budget, func(index, attempt int) bool {
			for _, planned := range plans {
				if planned.attempt == attempt && index >= planned.first && index <= planned.last {
					return true
				}
			}
			return false
		})
		if err != nil {
			return nil, 0, err
		}
		if plan.members == 0 || plan.members > math.MaxInt-members {
			return nil, 0, backend.ErrBuildCapacity
		}
		plans = append(plans, plan)
		members += plan.members
	}
	return plans, members, nil
}
