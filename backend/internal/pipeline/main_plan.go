package pipeline

import (
	"context"
	"strconv"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// Identity appenders write into caller-owned, exactly measured backing. The
// optional contract avoids allocating identity strings before plan admission.
type mainWorkIdentity interface {
	WorkIdentityLength(int) int
	AppendWorkIdentity([]byte, int) []byte
}
type deferredMainPlan struct {
	indices       []int
	keys          []string
	keyBacking    []byte
	pool, attempt int
	retry         mainRetryKey
	version       uint64
	dispatchGroup int
	dispatchEmpty bool
	wait          backend.AdmissionWait
	owner         string
	bounds        *backend.BuildBounds
	runtime       *ExecutionRuntime
}

func decimalIdentityLength(n int) int {
	var data [24]byte
	return len(strconv.AppendInt(data[:0], int64(n), 10))
}
func (s *MemoryRoundStore) WorkIdentityLength(index int) int {
	return len("call:") + len(s.identity) + len("/segment:") + decimalIdentityLength(index)
}
func (s *MemoryRoundStore) AppendWorkIdentity(dst []byte, index int) []byte {
	dst = append(dst, "call:"...)
	dst = append(dst, s.identity...)
	dst = append(dst, "/segment:"...)
	return strconv.AppendInt(dst, int64(index), 10)
}

func (s *mainScheduler) buildPlan(ctx context.Context, g int, store RoundStore) (plan *deferredMainPlan, err error) {
	p := &deferredMainPlan{pool: s.groups[g].pool, attempt: s.groups[g].attempt, retry: s.groups[g].retry, version: s.groups[g].version, owner: "main-plan:" + NewWorkID(), runtime: s.runtime}
	buildCtx := s.runtime.BuildContext(ctx, p.owner)
	b, err := backend.NewBuildBoundsFromContext(buildCtx, 0, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			p.close()
		}
		b.CloseRetired()
	}()
	s.observe("plan_build", 0, 1)
	batch, err := planNextMainBatchBounded(b, s.h, s.doc, func(yield func(int) bool) { s.visit(g, yield) }, p.pool, s.runtime.Window.limits.Segments, s.words, s.contextCount)
	if err != nil {
		return nil, err
	}
	if len(batch) == 0 {
		return nil, backend.ErrBuildCapacity
	}
	// Keep only the selected indices. The old planner's run scratch ends here;
	// its backing and this exact output are both charged during the copy.
	if err = b.ReserveAllocation(int64(len(batch)), int64(unsafe.Sizeof(int(0)))); err != nil {
		return nil, err
	}
	p.indices = make([]int, len(batch))
	copy(p.indices, batch)
	indexBytes := int64(cap(p.indices)) * int64(unsafe.Sizeof(int(0)))
	credit, err := b.RetainedAllocation(indexBytes)
	if err != nil {
		return nil, err
	}
	p.bounds, err = credit.DetachToBuild()
	if err != nil {
		return nil, err
	}
	batch = nil
	b.CloseRetired()
	if err = p.bounds.ReserveAllocation(int64(len(p.indices)), int64(unsafe.Sizeof(string("")))); err != nil {
		return nil, err
	}
	length := 0
	identity, hasIdentity := store.(mainWorkIdentity)
	for _, idx := range p.indices {
		n := len(s.h.ModeName()) + 1 + decimalIdentityLength(idx)
		if hasIdentity {
			n = identity.WorkIdentityLength(idx)
		}
		if n < 0 || length > int(^uint(0)>>1)-n-len("member:") {
			return nil, backend.ErrBuildCapacity
		}
		length += n + len("member:")
	}
	if err = p.bounds.ReserveAllocation(int64(length), 1); err != nil {
		return nil, err
	}
	p.keys = make([]string, len(p.indices))
	p.keyBacking = make([]byte, 0, length)
	for i, idx := range p.indices {
		start := len(p.keyBacking)
		p.keyBacking = append(p.keyBacking, "member:"...)
		if hasIdentity {
			p.keyBacking = identity.AppendWorkIdentity(p.keyBacking, idx)
		} else {
			p.keyBacking = append(p.keyBacking, s.h.ModeName()...)
			p.keyBacking = append(p.keyBacking, ':')
			p.keyBacking = strconv.AppendInt(p.keyBacking, int64(idx), 10)
		}
		part := p.keyBacking[start:]
		p.keys[i] = unsafe.String(unsafe.SliceData(part), len(part))
	}
	if len(p.keyBacking) != length || cap(p.keyBacking) != length {
		return nil, backend.ErrBuildCapacity
	}
	p.runtime.Admission.ObserveOwned("main_plan", p.owner, p.backingBytes())
	return p, nil
}
func (p *deferredMainPlan) backingBytes() int64 {
	return int64(cap(p.indices))*int64(unsafe.Sizeof(int(0))) + int64(cap(p.keys))*int64(unsafe.Sizeof(string(""))) + int64(cap(p.keyBacking))
}
func (p *deferredMainPlan) close() {
	if p == nil {
		return
	}
	p.indices, p.keys, p.keyBacking = nil, nil, nil
	p.wait = backend.AdmissionWait{}
	if p.runtime != nil {
		p.runtime.Admission.ObserveOwned("main_plan", p.owner, 0)
	}
	if p.bounds != nil {
		p.bounds.CloseRetired()
		p.bounds = nil
	}
}
func (p *deferredMainPlan) deferFor(wait backend.AdmissionWait, policy <-chan struct{}) {
	if wait.PolicyChanged == nil {
		wait.PolicyChanged = policy
	}
	p.wait = wait
}
func mainWaitSignalReady(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}
func (p *deferredMainPlan) valid(s *mainScheduler) bool {
	g := s.selectedGroup()
	return g >= 0 && p.matches(s.groups[g]) && s.groups[g].version == p.version
}

func (p *deferredMainPlan) matches(g mainScheduleGroup) bool {
	return g.pool == p.pool && g.attempt == p.attempt && g.retry == p.retry
}

// The slot is a generation handle even when its group has become empty. Its
// tuple/version survives freeing, so a rebuilt-and-emptied group cannot look
// like a quiet dispatch merely because both observations found no ready node.
func (p *deferredMainPlan) captureDispatch(s *mainScheduler, g int) {
	p.dispatchGroup = g
	p.dispatchEmpty = s.groups[g].root < 0
	p.version = s.groups[g].version
}
func (p *deferredMainPlan) canRestoreWait(s *mainScheduler) bool {
	if p.dispatchGroup < 0 || p.dispatchGroup >= len(s.groups) {
		return false
	}
	g := s.groups[p.dispatchGroup]
	if !p.matches(g) || g.version != p.version {
		return false
	}
	if p.dispatchEmpty {
		return g.root < 0 && s.findGroup(p.pool, p.attempt, p.retry) < 0
	}
	return g.root >= 0 && s.findGroup(p.pool, p.attempt, p.retry) == p.dispatchGroup
}
