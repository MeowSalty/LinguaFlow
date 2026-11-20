package pipeline

import (
	"context"
	"math"
	"sort"
	"time"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// mainScheduler is owned and mutated only by the round coordinator. Nodes hold
// scalar facts, not a second document or cursor. Both intrusive trees and the
// removable deadline heap use preadmitted typed backing.
type mainScheduler struct {
	doc                             *Document
	h                               stagedHandler
	maxPools, base, contextBase     int
	nodes                           []mainScheduleNode
	groups                          []mainScheduleGroup
	tree                            []mainScheduleChoice
	heap                            []int
	groupRoot, freeGroup, nextGroup int
	contextWords                    []int // Conditional range tree of immutable raw-source words.
	bounds                          *backend.BuildBounds
	ctx                             context.Context
	owner                           string
	runtime                         *ExecutionRuntime
	version                         uint64
}

type scheduleRun struct {
	first, last, count, words int
	overflow                  bool
}
type scheduleRuns struct{ first, last, best, split scheduleRun }
type mainScheduleNode struct {
	index, group, left, right, heapPos int
	priority                           uint64
	sourceWords, targetWords           int
	runs                               scheduleRuns
	at                                 time.Time
}
type mainScheduleGroup struct {
	pool, attempt, root, left, right, free int
	priority, version                      uint64
}
type mainScheduleChoice struct{ pool, ordinal, barrier, pending, mainPending int }

func schedulePriority(n uint64) uint64 {
	n += 0x9e3779b97f4a7c15
	n = (n ^ (n >> 30)) * 0xbf58476d1ce4e5b9
	n = (n ^ (n >> 27)) * 0x94d049bb133111eb
	return n ^ (n >> 31)
}
func scheduleSum(a, b int) int {
	if a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
func scheduleTreeBase(n int) (int, error) {
	base := 1
	for base < n {
		if base > math.MaxInt/2 {
			return 0, backend.ErrBuildCapacity
		}
		base *= 2
	}
	if base > math.MaxInt/2 {
		return 0, backend.ErrBuildCapacity
	}
	return base, nil
}
func (s *mainScheduler) observe(op string, bytes, rows int64) {
	backend.ObserveMemberWork(s.ctx, backend.MemberWorkEvent{Phase: "scheduler", Operation: op, Bytes: bytes, Rows: rows})
}
func newMainScheduler(ctx context.Context, doc *Document, h stagedHandler, runtime *ExecutionRuntime, maxPools int, owner string) (_ *mainScheduler, err error) {
	s := &mainScheduler{doc: doc, h: h, runtime: runtime, maxPools: maxPools, owner: owner, freeGroup: -1, groupRoot: -1}
	s.ctx = runtime.BuildContext(ctx, owner)
	s.bounds, err = backend.NewBuildBoundsFromContext(s.ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.close()
		}
	}()
	n := 0
	for i := range doc.Segments {
		s.observe("doc_member_visit", 0, 1)
		if doc.Segments[i].RoundWork.Member {
			n++
		}
	}
	s.base, err = scheduleTreeBase(n)
	if err != nil {
		return nil, err
	}
	for _, a := range []struct{ n, unit int64 }{{int64(n), int64(unsafe.Sizeof(mainScheduleNode{}))}, {int64(n), int64(unsafe.Sizeof(mainScheduleGroup{}))}, {int64(2 * s.base), int64(unsafe.Sizeof(mainScheduleChoice{}))}, {int64(n), int64(unsafe.Sizeof(int(0)))}} {
		if err = s.bounds.ReserveAllocation(a.n, a.unit); err != nil {
			return nil, err
		}
	}
	s.nodes = make([]mainScheduleNode, n)
	s.groups = make([]mainScheduleGroup, n)
	s.tree = make([]mainScheduleChoice, 2*s.base)
	s.heap = make([]int, 0, n)
	for i := range s.tree {
		s.observe("index_build_visit", 0, 1)
		s.tree[i] = mainScheduleChoice{pool: math.MaxInt, ordinal: -1, barrier: math.MaxInt}
	}
	at := 0
	for i := range doc.Segments {
		s.observe("doc_member_visit", 0, 1)
		if !doc.Segments[i].RoundWork.Member {
			continue
		}
		seg := &doc.Segments[i]
		s.nodes[at] = mainScheduleNode{index: i, group: -1, left: -1, right: -1, heapPos: -1, priority: schedulePriority(uint64(at)), sourceWords: CountWords(seg.Source)}
		s.observe("word_input", int64(len(seg.Source)), 1)
		if _, ok := h.(*ReviseHandler); ok {
			s.nodes[at].targetWords = CountWords(seg.Target)
			s.observe("word_input", int64(len(seg.Target)), 1)
		}
		at++
	}
	if h, ok := h.(*TranslateHandler); ok && h.Context.Enabled && max(h.Context.Before, h.Context.After) > 0 {
		s.contextBase, err = scheduleTreeBase(len(doc.Segments))
		if err != nil {
			return nil, err
		}
		if err = s.bounds.ReserveAllocation(int64(2*s.contextBase), int64(unsafe.Sizeof(int(0)))); err != nil {
			return nil, err
		}
		s.contextWords = make([]int, 2*s.contextBase)
		// Canonical preprocessing is complete before this index is created.
		// Within a round, confirmed installs change target/protection/metadata,
		// never Source, OriginalSource, Skip or StructuralOnly.
		for i := range doc.Segments {
			s.observe("doc_member_visit", 0, 1)
			if isContextEligible(&doc.Segments[i]) {
				text := rawSource(&doc.Segments[i])
				s.contextWords[s.contextBase+i] = CountWords(text)
				s.observe("word_input", int64(len(text)), 1)
			}
		}
		for i := s.contextBase - 1; i > 0; i-- {
			s.observe("context_build_visit", 0, 1)
			s.contextWords[i] = scheduleSum(s.contextWords[2*i], s.contextWords[2*i+1])
		}
	}
	for i := range s.nodes {
		s.refreshOrdinal(i, false, nil, time.Now())
	}
	runtime.Admission.ObserveOwned("scheduler", owner, s.backingBytes())
	return s, nil
}
func (s *mainScheduler) backingBytes() int64 {
	return int64(cap(s.nodes))*int64(unsafe.Sizeof(mainScheduleNode{})) + int64(cap(s.groups))*int64(unsafe.Sizeof(mainScheduleGroup{})) + int64(cap(s.tree))*int64(unsafe.Sizeof(mainScheduleChoice{})) + int64(cap(s.heap)+cap(s.contextWords))*int64(unsafe.Sizeof(int(0)))
}
func (s *mainScheduler) close() {
	if s == nil {
		return
	}
	s.nodes, s.groups, s.tree, s.heap, s.contextWords = nil, nil, nil, nil, nil
	if s.runtime != nil {
		s.runtime.Admission.ObserveOwned("scheduler", s.owner, 0)
	}
	if s.bounds != nil {
		s.bounds.CloseRetired()
		s.bounds = nil
	}
}
func (s *mainScheduler) ordinal(index int) int {
	i := sort.Search(len(s.nodes), func(i int) bool { s.observe("index_visit", 0, 1); return s.nodes[i].index >= index })
	if i < len(s.nodes) {
		s.observe("index_visit", 0, 1)
	}
	if i == len(s.nodes) || s.nodes[i].index != index {
		return -1
	}
	return i
}
func scheduleChoice(a, b mainScheduleChoice) mainScheduleChoice {
	r := a
	if b.pool < a.pool || b.pool == a.pool && (a.ordinal < 0 || b.ordinal >= 0 && b.ordinal < a.ordinal) {
		r.pool, r.ordinal = b.pool, b.ordinal
	}
	r.barrier = min(a.barrier, b.barrier)
	r.pending = a.pending + b.pending
	r.mainPending = a.mainPending + b.mainPending
	return r
}
func (s *mainScheduler) refresh(index int, active bool, candidate *Candidate, now time.Time) {
	if i := s.ordinal(index); i >= 0 {
		s.refreshOrdinal(i, active, candidate, now)
	}
}
func (s *mainScheduler) refreshOrdinal(i int, active bool, candidate *Candidate, now time.Time) {
	s.observe("ready_update", 0, 1)
	n := &s.nodes[i]
	s.observe("doc_member_visit", 0, 1)
	seg := &s.doc.Segments[n.index]
	w := seg.RoundWork
	cur := w.Cursor
	ready := !w.Completed && !active && candidate == nil && cur.Pool < s.maxPools && cur.State != "unresolved" && cur.State != "candidate" && cur.State != "response_pending" && !now.Before(cur.NextAttemptAt)
	g := n.group
	if g >= 0 && (!ready || s.groups[g].pool != cur.Pool || s.groups[g].attempt != cur.Attempt) {
		s.groups[g].root = s.eraseReady(s.groups[g].root, i, g)
		s.version++
		s.groups[g].version = s.version
		n.group = -1
		if s.groups[g].root < 0 {
			s.groupRoot = s.eraseGroup(s.groupRoot, g)
			s.groups[g].free = s.freeGroup
			s.freeGroup = g
		}
	}
	if ready && n.group < 0 {
		g = s.findGroup(cur.Pool, cur.Attempt)
		if g < 0 {
			if s.freeGroup >= 0 {
				g = s.freeGroup
				s.freeGroup = s.groups[g].free
			} else {
				g = s.nextGroup
				s.nextGroup++
			}
			s.groups[g] = mainScheduleGroup{pool: cur.Pool, attempt: cur.Attempt, root: -1, left: -1, right: -1, priority: schedulePriority(uint64(cur.Pool) ^ uint64(cur.Attempt)<<32)}
			s.groupRoot = s.insertGroup(s.groupRoot, g)
		}
		n.group, n.left, n.right = g, -1, -1
		s.groups[g].root = s.insertReady(s.groups[g].root, i, g)
		s.version++
		s.groups[g].version = s.version
	}
	choice := mainScheduleChoice{pool: math.MaxInt, ordinal: -1, barrier: math.MaxInt}
	if !w.Completed {
		if cur.State != "unresolved" {
			choice.barrier = cur.Pool
		}
		if candidate != nil || cur.Pool < s.maxPools && cur.State != "unresolved" {
			choice.pending = 1
		}
		if candidate == nil && cur.Pool < s.maxPools && cur.State != "unresolved" {
			choice.mainPending = 1
		}
		if ready {
			choice.pool, choice.ordinal = cur.Pool, i
		}
	}
	p := s.base + i
	s.tree[p] = choice
	for p /= 2; p > 0; p /= 2 {
		s.observe("index_visit", 0, 1)
		s.tree[p] = scheduleChoice(s.tree[2*p], s.tree[2*p+1])
	}
	at := cur.NextAttemptAt
	if candidate != nil {
		at = candidate.NextAttemptAt
	}
	if active || w.Completed || !at.After(now) {
		at = time.Time{}
	}
	s.setDeadline(i, at)
}
func (s *mainScheduler) selectedGroup() int {
	s.observe("index_visit", 0, 1)
	c := s.tree[1]
	if c.ordinal < 0 || c.barrier < c.pool {
		return -1
	}
	s.observe("index_visit", 0, 1)
	return s.nodes[c.ordinal].group
}
func (s *mainScheduler) findGroup(pool, attempt int) int {
	for g := s.groupRoot; g >= 0; {
		s.observe("index_visit", 0, 1)
		n := s.groups[g]
		if n.pool == pool && n.attempt == attempt {
			return g
		}
		if pool < n.pool || pool == n.pool && attempt < n.attempt {
			g = n.left
		} else {
			g = n.right
		}
	}
	return -1
}
func (s *mainScheduler) groupLess(a, b int) bool {
	x, y := s.groups[a], s.groups[b]
	return x.pool < y.pool || x.pool == y.pool && x.attempt < y.attempt
}
func (s *mainScheduler) mergeGroup(a, b int) int {
	if a < 0 {
		return b
	}
	if b < 0 {
		return a
	}
	s.observe("index_visit", 0, 2)
	if s.groups[a].priority < s.groups[b].priority {
		s.groups[a].right = s.mergeGroup(s.groups[a].right, b)
		return a
	}
	s.groups[b].left = s.mergeGroup(a, s.groups[b].left)
	return b
}
func (s *mainScheduler) insertGroup(root, g int) int {
	if root < 0 {
		return g
	}
	s.observe("index_visit", 0, 1)
	if s.groupLess(g, root) {
		s.groups[root].left = s.insertGroup(s.groups[root].left, g)
		l := s.groups[root].left
		if s.groups[l].priority < s.groups[root].priority {
			s.groups[root].left = s.groups[l].right
			s.groups[l].right = root
			return l
		}
	} else {
		s.groups[root].right = s.insertGroup(s.groups[root].right, g)
		r := s.groups[root].right
		if s.groups[r].priority < s.groups[root].priority {
			s.groups[root].right = s.groups[r].left
			s.groups[r].left = root
			return r
		}
	}
	return root
}
func (s *mainScheduler) eraseGroup(root, g int) int {
	s.observe("index_visit", 0, 1)
	if root == g {
		return s.mergeGroup(s.groups[root].left, s.groups[root].right)
	}
	if s.groupLess(g, root) {
		s.groups[root].left = s.eraseGroup(s.groups[root].left, g)
	} else {
		s.groups[root].right = s.eraseGroup(s.groups[root].right, g)
	}
	return root
}
func (s *mainScheduler) constraint(g int) BatchConstraint {
	switch h := s.h.(type) {
	case *TranslateHandler:
		c := BatchConstraint{MaxSegments: h.BatchSize, MaxWords: h.MaxWordsPerBatch}
		if c.MaxSegments <= 0 && c.MaxWords <= 0 {
			c.MaxSegments = 1
		}
		return shrinkConstraint(c, h.FallbackShrink, s.groups[g].pool)
	case *ReviseHandler:
		return BatchConstraint{MaxSegments: h.BatchSize, MaxWords: h.MaxWordsPerBatch}
	}
	return BatchConstraint{}
}
func (s *mainScheduler) splitRun(r scheduleRun, g int) bool {
	c := s.constraint(g)
	return r.count > 1 && (c.MaxSegments > 0 && r.count > c.MaxSegments || c.MaxWords > 0 && (r.overflow || r.words > c.MaxWords))
}
func betterScheduleRun(a, b scheduleRun) scheduleRun {
	if b.count > a.count || b.count == a.count && b.count > 0 && b.first < a.first {
		return b
	}
	return a
}
func firstSplitRun(a, b scheduleRun) scheduleRun {
	if a.count == 0 || b.count > 0 && b.first < a.first {
		return b
	}
	return a
}
func (s *mainScheduler) mergeRuns(a, b scheduleRuns, g int) scheduleRuns {
	if a.first.count == 0 {
		return b
	}
	if b.first.count == 0 {
		return a
	}
	r := scheduleRuns{first: a.first, last: b.last, best: betterScheduleRun(a.best, b.best), split: firstSplitRun(a.split, b.split)}
	if a.last.last+1 == b.first.first {
		joined := scheduleRun{first: a.last.first, last: b.first.last, count: a.last.count + b.first.count, words: scheduleSum(a.last.words, b.first.words), overflow: a.last.overflow || b.first.overflow || a.last.words > math.MaxInt-b.first.words}
		r.best = betterScheduleRun(r.best, joined)
		if s.splitRun(joined, g) {
			r.split = firstSplitRun(r.split, joined)
		}
		if a.first.first == a.last.first {
			r.first = joined
		}
		if b.first.first == b.last.first {
			r.last = joined
		}
	}
	return r
}
func (s *mainScheduler) pullReady(i, g int) {
	n := &s.nodes[i]
	r := scheduleRun{first: n.index, last: n.index, count: 1, words: scheduleSum(n.sourceWords, n.targetWords)}
	a := scheduleRuns{first: r, last: r, best: r}
	if n.left >= 0 {
		a = s.mergeRuns(s.nodes[n.left].runs, a, g)
	}
	if n.right >= 0 {
		a = s.mergeRuns(a, s.nodes[n.right].runs, g)
	}
	n.runs = a
}
func (s *mainScheduler) insertReady(root, i, g int) int {
	if root < 0 {
		s.pullReady(i, g)
		return i
	}
	s.observe("index_visit", 0, 1)
	if s.nodes[i].index < s.nodes[root].index {
		s.nodes[root].left = s.insertReady(s.nodes[root].left, i, g)
		l := s.nodes[root].left
		if s.nodes[l].priority < s.nodes[root].priority {
			s.nodes[root].left = s.nodes[l].right
			s.nodes[l].right = root
			s.pullReady(root, g)
			s.pullReady(l, g)
			return l
		}
	} else {
		s.nodes[root].right = s.insertReady(s.nodes[root].right, i, g)
		r := s.nodes[root].right
		if s.nodes[r].priority < s.nodes[root].priority {
			s.nodes[root].right = s.nodes[r].left
			s.nodes[r].left = root
			s.pullReady(root, g)
			s.pullReady(r, g)
			return r
		}
	}
	s.pullReady(root, g)
	return root
}
func (s *mainScheduler) mergeReady(a, b, g int) int {
	if a < 0 {
		return b
	}
	if b < 0 {
		return a
	}
	s.observe("index_visit", 0, 1)
	if s.nodes[a].priority < s.nodes[b].priority {
		s.nodes[a].right = s.mergeReady(s.nodes[a].right, b, g)
		s.pullReady(a, g)
		return a
	}
	s.nodes[b].left = s.mergeReady(a, s.nodes[b].left, g)
	s.pullReady(b, g)
	return b
}
func (s *mainScheduler) eraseReady(root, i, g int) int {
	s.observe("index_visit", 0, 1)
	if root == i {
		return s.mergeReady(s.nodes[root].left, s.nodes[root].right, g)
	}
	if s.nodes[i].index < s.nodes[root].index {
		s.nodes[root].left = s.eraseReady(s.nodes[root].left, i, g)
	} else {
		s.nodes[root].right = s.eraseReady(s.nodes[root].right, i, g)
	}
	s.pullReady(root, g)
	return root
}
func (s *mainScheduler) visitReady(root, lo, hi int, yield func(int) bool) bool {
	if root < 0 {
		return true
	}
	s.observe("index_visit", 0, 1)
	n := s.nodes[root]
	if n.index > lo && !s.visitReady(n.left, lo, hi, yield) {
		return false
	}
	if n.index >= lo && n.index <= hi && !yield(n.index) {
		return false
	}
	return n.index >= hi || s.visitReady(n.right, lo, hi, yield)
}
func (s *mainScheduler) visit(g int, yield func(int) bool) {
	root := s.groups[g].root
	lo, hi := 0, math.MaxInt
	if h, ok := s.h.(*TranslateHandler); ok && (!h.Context.Enabled || max(h.Context.Before, h.Context.After) == 0) {
		a := s.nodes[root].runs
		r := a.split
		if r.count == 0 {
			r = a.best
		}
		lo, hi = r.first, r.last
	}
	s.visitReady(root, lo, hi, yield)
}
func (s *mainScheduler) words(index int, target bool) int {
	i := s.ordinal(index)
	if i < 0 {
		return 0
	}
	if target {
		return s.nodes[i].targetWords
	}
	return s.nodes[i].sourceWords
}
func (s *mainScheduler) contextCount(indices []int, window int) int {
	lo, hi := indices[0], indices[len(indices)-1]
	if window >= lo {
		lo = 0
	} else {
		lo -= window
	}
	if window >= len(s.doc.Segments)-1-hi {
		hi = len(s.doc.Segments) - 1
	} else {
		hi += window
	}
	// Sum disjoint gaps directly. Saturated prefixes or total-minus-main
	// subtraction would lose the answer when an excluded main cell is huge.
	words := 0
	for _, idx := range indices {
		words = scheduleSum(words, s.contextRange(lo, idx))
		lo = idx + 1
	}
	return scheduleSum(words, s.contextRange(lo, hi+1))
}
func (s *mainScheduler) contextRange(lo, hi int) int {
	words := 0
	for lo, hi = lo+s.contextBase, hi+s.contextBase; lo < hi; lo, hi = lo/2, hi/2 {
		if lo&1 != 0 {
			s.observe("context_visit", 0, 1)
			words = scheduleSum(words, s.contextWords[lo])
			lo++
		}
		if hi&1 != 0 {
			hi--
			s.observe("context_visit", 0, 1)
			words = scheduleSum(words, s.contextWords[hi])
		}
	}
	return words
}
func (s *mainScheduler) lessDeadline(a, b int) bool {
	s.observe("index_visit", 0, 1)
	x, y := s.nodes[s.heap[a]], s.nodes[s.heap[b]]
	return x.at.Before(y.at) || x.at.Equal(y.at) && x.index < y.index
}
func (s *mainScheduler) swapDeadline(a, b int) {
	s.heap[a], s.heap[b] = s.heap[b], s.heap[a]
	s.nodes[s.heap[a]].heapPos = a
	s.nodes[s.heap[b]].heapPos = b
}
func (s *mainScheduler) fixDeadline(p int) {
	for p > 0 && s.lessDeadline(p, (p-1)/2) {
		q := (p - 1) / 2
		s.swapDeadline(p, q)
		p = q
	}
	for {
		q := 2*p + 1
		if q >= len(s.heap) {
			return
		}
		if q+1 < len(s.heap) && s.lessDeadline(q+1, q) {
			q++
		}
		if !s.lessDeadline(q, p) {
			return
		}
		s.swapDeadline(q, p)
		p = q
	}
}
func (s *mainScheduler) setDeadline(i int, at time.Time) {
	n := &s.nodes[i]
	if n.at.Equal(at) {
		return
	}
	p := n.heapPos
	n.at = at
	if at.IsZero() {
		if p < 0 {
			return
		}
		last := len(s.heap) - 1
		s.swapDeadline(p, last)
		s.heap = s.heap[:last]
		n.heapPos = -1
		if p < len(s.heap) {
			s.fixDeadline(p)
		}
		return
	}
	if p < 0 {
		n.heapPos = len(s.heap)
		s.heap = append(s.heap, i)
		p = n.heapPos
	}
	s.fixDeadline(p)
}
func (s *mainScheduler) deadline() time.Time {
	s.observe("deadline_probe", 0, 1)
	if len(s.heap) == 0 {
		return time.Time{}
	}
	return s.nodes[s.heap[0]].at
}
func (s *mainScheduler) expire(now time.Time, active map[int]bool, candidates map[int]*Candidate) {
	for len(s.heap) > 0 && !s.nodes[s.heap[0]].at.After(now) {
		i := s.heap[0]
		s.refreshOrdinal(i, active[s.nodes[i].index], candidates[s.nodes[i].index], now)
	}
}
