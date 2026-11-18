package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type memberBacking struct {
	mu            sync.Mutex
	refs          int
	payload       []byte
	release       func()
	prepared      *backend.PreparedAllocationTransfer
	source        *backend.OwnedAllocation
	clearObserved func()
}
type memoryMemberOutcome struct {
	CandidateID, Digest string
	Failed              bool
}

func (b *memberBacking) borrow() ([]byte, func()) {
	b.mu.Lock()
	b.refs++
	p := b.payload
	b.mu.Unlock()
	return p, onceRelease(func() { b.drop() })
}
func (b *memberBacking) drop() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.refs--
	if b.refs > 0 {
		b.mu.Unlock()
		return nil
	}
	prepared, source := b.prepared, b.source
	release := b.release
	b.mu.Unlock()
	if prepared != nil {
		if err := prepared.Rollback(); err != nil {
			b.mu.Lock()
			b.refs = 1
			b.mu.Unlock()
			return err
		}
		b.mu.Lock()
		b.payload = nil
		b.mu.Unlock()
		source.Release()
	} else if release != nil {
		b.mu.Lock()
		b.payload = nil
		b.mu.Unlock()
		release()
	}
	if b.clearObserved != nil {
		b.clearObserved()
	}
	b.mu.Lock()
	b.payload = nil
	b.prepared = nil
	b.source = nil
	b.release = nil
	b.mu.Unlock()
	return nil
}

func (b *memberBacking) commit() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.prepared != nil {
		b.prepared.Commit()
		b.prepared = nil
		b.source = nil
	}
}
func prepareMemberBacking(ctx context.Context, p []byte, a *backend.OwnedAllocation, owner string) (*memberBacking, error) {
	admission, _ := backend.BuildOwnership(ctx)

	if admission == nil {
		return adoptMemberBacking(ctx, p, a, owner)
	}
	plan, err := backend.PrepareBuildAllocationTransfer(ctx, a, owner)
	if err != nil {
		return nil, err
	}
	admission.ObserveOwned("durable_member", owner, int64(cap(p)))
	return &memberBacking{refs: 1, payload: p, source: a, prepared: plan, clearObserved: func() { admission.ObserveOwned("durable_member", owner, 0) }, release: func() { admission.ObserveOwned("durable_member", owner, 0); _ = admission.ReleaseBuild(owner) }}, nil
}
func adoptMemberBacking(ctx context.Context, p []byte, a *backend.OwnedAllocation, owner string) (*memberBacking, error) {
	if a == nil {
		return nil, fmt.Errorf("%w: missing member backing receipt", backend.ErrBuildCapacity)
	}
	admission, _ := backend.BuildOwnership(ctx)
	if admission != nil {
		if err := backend.TransferBuildAllocationToBuild(ctx, a, owner); err != nil {
			return nil, err
		}
		admission.ObserveOwned("durable_member", owner, int64(cap(p)))
		return &memberBacking{refs: 1, payload: p, release: func() { admission.ObserveOwned("durable_member", owner, 0); _ = admission.ReleaseBuild(owner) }}, nil
	}
	transferred := a.Transfer()
	if transferred == nil {
		return nil, backend.ErrBuildCapacity
	}
	return &memberBacking{refs: 1, payload: p, release: transferred.Release}, nil
}
func (s *MemoryRoundStore) reserveMembers(ctx context.Context, in RequestIntent) error {
	if s.closed {
		return fmt.Errorf("round store closed")
	}
	if old := s.requests[in.ID]; old != nil {
		if old.intent.InputDigest != in.InputDigest {
			return fmt.Errorf("request identity reused")
		}
		return nil
	}
	r := &memoryResponse{intent: in, inputs: map[int]InputMemberFrame{}, outputs: map[int]OutputMemberFrame{}, memberBackings: map[int]*memberBacking{}, outputBackings: map[int]*memberBacking{}, memberOutcomes: map[int]memoryMemberOutcome{}, corrections: map[int]OutputMemberFrame{}, correctionBackings: map[int]*memberBacking{}, stagedCorrections: map[int]OutputMemberFrame{}, stagedCorrectionBackings: map[int]*memberBacking{}, record: RequestRecord{State: "reserved"}}
	installed := false
	admission, _ := backend.BuildOwnership(ctx)
	if admission != nil {
		admission.ObserveOwned("freeze", in.ID, 0)
	}
	r.historyAdmission = admission
	r.historyOwner = "history:" + s.identity + ":" + in.ID
	if admission != nil {
		history := backend.WithBuildOwnership(ctx, admission, r.historyOwner)
		history = backend.WithBuildReservation(history, func(n int64) error {
			return admission.ClaimBuildBounded(r.historyOwner, n, backend.BuildAllocationLimit(ctx))
		}, func(n int64) { _ = admission.ReleaseBuildBytes(r.historyOwner, n) })
		hb, err := backend.NewBuildBoundsFromContext(history, 0, 0)
		if err != nil {
			return err
		}
		if err = hb.ReserveAllocation(1, int64(unsafe.Sizeof(memoryResponse{}))); err != nil {
			return err
		}
	}
	defer func() {
		if !installed {
			if admission != nil {
				_ = admission.ReleaseBuild(r.historyOwner)
			}
			for _, b := range r.memberBackings {
				b.drop()
			}
		}
	}()
	var n int64
	if len(in.InputMembers) != len(in.Indices) {
		return fmt.Errorf("invalid input member count")
	}
	promptIDs, candidateIDs := map[string]bool{}, map[string]bool{}
	epoch := int64(0)
	if len(in.InputMembers) > 0 {
		epoch = in.InputMembers[0].Key.CaptureEpoch
	}
	for i, m := range in.InputMembers {
		if m.Key.RequestID != in.ID || m.Key.Ordinal != i || m.Key.CaptureEpoch != epoch || m.Index != in.Indices[i] || m.PromptID == "" || m.CandidateID == "" || promptIDs[m.PromptID] || candidateIDs[m.CandidateID] || m.Bytes != int64(len(m.Payload)) || m.Digest != memberResponseDigest(ctx, m.Payload) {
			return fmt.Errorf("invalid input member")
		}
		promptIDs[m.PromptID], candidateIDs[m.CandidateID] = true, true
		backing, err := prepareMemberBacking(ctx, m.Payload, m.Allocation, fmt.Sprintf("input:%s:%d", in.ID, i))
		if err != nil {
			return err
		}
		m.Allocation = nil
		m.Release = nil
		r.inputs[i] = m
		r.memberBackings[i] = backing
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "freeze", Operation: "input_write", Bytes: m.Bytes, Rows: 1})
		n += m.Bytes
	}
	s.nextRequest++
	r.intent.InputMembers = nil
	r.response.MainResponseHeader = MainResponseHeader{CaptureEpoch: epoch, FrozenContext: in.FrozenContext, ID: in.ID, DatabaseID: s.nextRequest, MemberCount: len(in.InputMembers), FormatVersion: execution.ResponseFormatVersion, SnapshotDigest: in.SnapshotDigest, FreezeDigest: in.InputDigest, InputMemberBytes: n, FreezeBytes: n, Phase: "inputs_ready", BusinessBound: in.BusinessBound}
	s.requests[in.ID] = r
	for _, backing := range r.memberBackings {
		backing.commit()
	}
	installed = true
	for _, idx := range in.Indices {
		cur := s.state.Cursors[idx]
		if in.Phase != "prompt_upgrade" && in.Stage == backend.RequestStageMain {
			cur.Attempt++
		}
		s.state.Cursors[idx] = cur
	}
	return nil
}
func (s *MemoryRoundStore) memberLocked(key MemberKey) (*memoryResponse, error) {
	r := s.requests[key.RequestID]
	if r == nil || r.response.FormatVersion != execution.ResponseFormatVersion || key.CaptureEpoch != r.response.CaptureEpoch || key.Ordinal < 0 || key.Ordinal >= r.response.MemberCount {
		return nil, fmt.Errorf("invalid response member")
	}
	return r, nil
}
func (s *MemoryRoundStore) LoadInputMember(ctx context.Context, key MemberKey, limit int64) (InputMemberFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.memberLocked(key)
	if err != nil {
		return InputMemberFrame{}, err
	}
	if r.response.Phase != "inputs_ready" && r.response.Phase != "capture_pending" && r.response.Phase != "outputs_ready" && r.response.Phase != "business_ready" {
		return InputMemberFrame{}, fmt.Errorf("input collection unpublished")
	}
	m, ok := r.inputs[key.Ordinal]
	if !ok || m.Bytes <= 0 || int64(len(m.Payload)) != m.Bytes || m.Bytes > limit || m.Digest != memberResponseDigest(ctx, m.Payload) {
		return InputMemberFrame{}, fmt.Errorf("invalid input member")
	}
	m.Payload, m.Release = r.memberBackings[key.Ordinal].borrow()
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "input_read", Bytes: m.Bytes, Rows: 1})
	m.Release = backend.TrackMemberFrame(ctx, m.Bytes, m.Release)
	return m, nil
}
func (s *MemoryRoundStore) LoadOutputMember(ctx context.Context, key MemberKey, final bool, limit int64) (OutputMemberFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.memberLocked(key)
	if err != nil {
		return OutputMemberFrame{}, err
	}
	if r.response.Phase != "outputs_ready" && r.response.Phase != "business_ready" || final && r.response.Phase != "business_ready" {
		return OutputMemberFrame{}, fmt.Errorf("output collection unpublished")
	}
	m, ok := r.outputs[key.Ordinal]
	if !ok || m.Bytes <= 0 || int64(len(m.Payload)) != m.Bytes || m.Bytes > limit || m.Digest != memberResponseDigest(ctx, m.Payload) {
		return OutputMemberFrame{}, fmt.Errorf("invalid output member")
	}
	if final {
		if corrected, ok := r.corrections[key.Ordinal]; ok {
			m = corrected
			m.StoredBytes = r.outputs[key.Ordinal].Bytes + corrected.Bytes
			if m.Bytes > limit || int64(len(m.Payload)) != m.Bytes || memberResponseDigest(ctx, m.Payload) != m.Digest {
				return OutputMemberFrame{}, fmt.Errorf("invalid corrected member")
			}
			m.Payload, m.Release = r.correctionBackings[key.Ordinal].borrow()
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "output_read", Bytes: m.Bytes, Rows: 1})
			m.Release = backend.TrackMemberFrame(ctx, m.Bytes, m.Release)
			return m, nil
		}
	}
	m.StoredBytes = m.Bytes
	m.Payload, m.Release = r.outputBackings[key.Ordinal].borrow()
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "output_read", Bytes: m.Bytes, Rows: 1})
	m.Release = backend.TrackMemberFrame(ctx, m.Bytes, m.Release)
	return m, nil
}
func (s *MemoryRoundStore) CaptureResponse(ctx context.Context, in CapturedResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[in.ID]
	if r == nil || len(in.Payload) == 0 {
		return fmt.Errorf("invalid capture")
	}
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "capture", Operation: "capture_write", Bytes: int64(len(in.Payload)), Rows: 1})
	digest := memberResponseDigest(ctx, in.Payload)
	if r.response.ResponseDigest != "" {
		if r.response.ResponseDigest != digest {
			return fmt.Errorf("capture identity reused")
		}
		return nil
	}
	if admission, _ := backend.BuildOwnership(ctx); admission != nil {
		admission.ObserveOwned("raw", in.ID, 0)
	}
	backing, err := adoptMemberBacking(ctx, in.Payload, in.Allocation, "capture:"+in.ID)
	if err != nil {
		return err
	}
	r.capture = backing
	r.response.ResponseDigest = digest
	r.response.ResponseBytes = int64(len(in.Payload))
	r.response.Phase = "capture_pending"
	r.response.Truncated = in.Truncated
	r.record = in.Record
	r.record.State = "completed"
	for _, m := range r.inputs {
		cur := s.state.Cursors[m.Index]
		cur.State = "response_pending"
		s.state.Cursors[m.Index] = cur
	}
	return nil
}
func (s *MemoryRoundStore) LoadCapture(ctx context.Context, id string, limit int64) (CapturedResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || r.response.Phase != "capture_pending" || r.response.ResponseBytes > limit {
		return CapturedResponse{}, fmt.Errorf("capture unavailable")
	}
	p, release := r.capture.borrow()
	if int64(len(p)) != r.response.ResponseBytes || memberResponseDigest(ctx, p) != r.response.ResponseDigest {
		release()
		return CapturedResponse{}, fmt.Errorf("capture integrity mismatch")
	}
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "capture", Operation: "capture_read", Bytes: int64(len(p)), Rows: 1})
	return CapturedResponse{MainResponseHeader: r.response.MainResponseHeader, Payload: p, Record: r.record, Release: release}, nil
}
func (s *MemoryRoundStore) PublishOutputs(ctx context.Context, in OutputPublication) (result error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.memberLocked(in.Key)
	if err != nil {
		return err
	}
	if len(in.Members) > 16 {
		return fmt.Errorf("archive page exceeds bound")
	}
	for ordinal, backing := range r.archiveStaging {
		if err := backing.drop(); err != nil {
			return err
		}
		delete(r.archiveStaging, ordinal)
	}
	if r.response.ArchiveCursor == in.Next && r.response.ArchiveDigest == in.Digest {
		return nil
	}
	if r.response.Phase != "capture_pending" || len(in.Members) != in.Next-r.response.ArchiveCursor {
		return fmt.Errorf("archive checkpoint mismatch")
	}
	if in.Complete && in.Next != r.response.MemberCount {
		return fmt.Errorf("incomplete archive")
	}
	staged := map[int]*memberBacking{}
	installed := false
	defer func() {
		if !installed {
			for ordinal, b := range staged {
				if err := b.drop(); err != nil {
					result = errors.Join(result, err)
				} else {
					delete(staged, ordinal)
				}
			}
			r.archiveStaging = staged
		}
	}()
	for i, m := range in.Members {
		if m.Key.RequestID != in.Key.RequestID || m.Key.CaptureEpoch != in.Key.CaptureEpoch || m.Key.Ordinal != r.response.ArchiveCursor+i || m.Bytes != int64(len(m.Payload)) || m.Digest != memberResponseDigest(ctx, m.Payload) {
			return fmt.Errorf("archive member identity mismatch")
		}
		b, err := prepareMemberBacking(ctx, m.Payload, m.Allocation, fmt.Sprintf("output:%s:%d", in.Key.RequestID, m.Key.Ordinal))
		if err != nil {
			return err
		}
		staged[m.Key.Ordinal] = b
	}
	var public *memberBacking
	if in.Complete && len(in.Glossary) > 0 {
		public, err = prepareMemberBacking(ctx, in.Glossary, in.PublicAllocation, "public:"+in.Key.RequestID)
		if err != nil {
			return err
		}
	}
	for _, m := range in.Members {
		staged[m.Key.Ordinal].commit()
		m.Allocation = nil
		m.Release = nil
		r.outputs[m.Key.Ordinal] = m
		r.outputBackings[m.Key.Ordinal] = staged[m.Key.Ordinal]
		r.response.OutputMemberBytes += m.Bytes
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "output_write", Bytes: m.Bytes, Rows: 1})
	}
	r.response.ArchiveCursor, r.response.ArchiveDigest = in.Next, in.Digest
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "checkpoint_write", Rows: 1})
	if in.Complete {
		public.commit()
		if in.Next != r.response.MemberCount {
			return fmt.Errorf("incomplete archive")
		}
		r.response.Phase = "outputs_ready"
		r.response.ResponseBytes = 0
		r.response.Receipt = in.Glossary
		r.publicBacking = public
		r.response.PublicBytes = int64(len(in.Glossary))
		r.publicDigest = sha256.Sum256(in.Glossary)
		r.capture.drop()
		r.capture = nil
	}
	installed = true
	return nil
}
func (s *MemoryRoundStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil {
		return fmt.Errorf("missing response")
	}
	if r.response.Phase == "business_ready" {
		return nil
	}
	if r.response.Phase != "outputs_ready" {
		return fmt.Errorf("outputs unpublished")
	}
	if int64(len(r.response.Receipt)) != r.response.PublicBytes || sha256.Sum256(r.response.Receipt) != r.publicDigest {
		return fmt.Errorf("invalid response public facts")
	}
	var receipt *memberBacking
	var err error
	if len(out.Receipt) > 0 {
		receipt, err = prepareMemberBacking(ctx, out.Receipt, out.ReceiptAllocation, "receipt:"+id)
		if err != nil {
			return err
		}
	}
	for ordinal, m := range r.stagedCorrections {
		r.stagedCorrectionBackings[ordinal].commit()
		r.corrections[ordinal] = m
		r.correctionBackings[ordinal] = r.stagedCorrectionBackings[ordinal]
		r.response.OutputMemberBytes += m.Bytes
	}
	r.stagedCorrections = map[int]OutputMemberFrame{}
	r.stagedCorrectionBackings = map[int]*memberBacking{}
	old := r.publicBacking
	r.publicBacking = receipt
	receipt.commit()
	r.response.Receipt = out.Receipt
	r.response.PublicBytes = int64(len(out.Receipt))
	r.publicDigest = sha256.Sum256(out.Receipt)
	r.response.Phase = "business_ready"
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "business_write", Bytes: int64(len(out.Receipt)), Rows: 1})
	if install != nil {
		install()
	}
	old.drop()
	return nil
}
func (s *MemoryRoundStore) LoadResponsePublic(ctx context.Context, id string, limit int64) (OutputMemberFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || r.response.PublicBytes > limit {
		return OutputMemberFrame{}, fmt.Errorf("invalid response public facts")
	}
	if r.publicBacking == nil {
		if r.response.PublicBytes != 0 || r.publicDigest != sha256.Sum256(nil) {
			return OutputMemberFrame{}, fmt.Errorf("invalid response public facts")
		}
		return OutputMemberFrame{Release: onceRelease(nil)}, nil
	}
	p, release := r.publicBacking.borrow()
	if int64(len(p)) != r.response.PublicBytes || sha256.Sum256(p) != r.publicDigest {
		release()
		return OutputMemberFrame{}, fmt.Errorf("invalid response public facts")
	}
	return OutputMemberFrame{Payload: p, Bytes: int64(len(p)), Release: release}, nil
}
func (s *MemoryRoundStore) StageCorrectedMember(ctx context.Context, in OutputMemberFrame) error {
	if in.Bytes <= 0 || int64(len(in.Payload)) != in.Bytes || memberResponseDigest(ctx, in.Payload) != in.Digest {
		return fmt.Errorf("invalid corrected member")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.memberLocked(in.Key)
	if err != nil {
		return err
	}
	if r.response.Phase != "outputs_ready" {
		return fmt.Errorf("invalid correction stage")
	}
	if old, ok := r.stagedCorrections[in.Key.Ordinal]; ok {
		if old.Digest == in.Digest {
			return nil
		}
		return fmt.Errorf("correction identity reused")
	}
	b, err := prepareMemberBacking(ctx, in.Payload, in.Allocation, fmt.Sprintf("corrected:%s:%d", in.Key.RequestID, in.Key.Ordinal))
	if err != nil {
		return err
	}
	in.Allocation = nil
	r.stagedCorrections[in.Key.Ordinal] = in
	r.stagedCorrectionBackings[in.Key.Ordinal] = b
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "correction_write", Bytes: in.Bytes, Rows: 1})
	return nil
}
func (s *MemoryRoundStore) AbortMemberBusiness(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.requests[id]; r != nil && r.response.Phase != "business_ready" {
		var result error
		for ordinal, b := range r.stagedCorrectionBackings {
			if err := b.drop(); err != nil {
				result = errors.Join(result, err)
				continue
			}
			delete(r.stagedCorrectionBackings, ordinal)
			delete(r.stagedCorrections, ordinal)
		}
		return result
	}
	return nil
}

func (s *MemoryRoundStore) promoteMemberLocked(ctx context.Context, r *memoryResponse, cursor int, c *Candidate, next WorkCursor, failure string, payload []byte) error {
	if cursor < 0 || cursor >= r.response.MemberCount {
		return fmt.Errorf("invalid promotion")
	}
	if outcome, ok := r.memberOutcomes[cursor]; ok {
		if c == nil && outcome.Failed {
			return nil
		}
		if c != nil && outcome.CandidateID == c.ID && outcome.Digest == memberResponseDigest(ctx, payload) {
			return nil
		}
		return fmt.Errorf("promotion identity reused")
	}
	if r.response.Cursor != cursor || r.response.Phase != "business_ready" {
		return fmt.Errorf("promotion cursor mismatch")
	}
	m := r.inputs[cursor]
	outcome := memoryMemberOutcome{Failed: c == nil}
	if c != nil {
		if c.ID != m.CandidateID || c.Index != m.Index || c.ParentRequestID != r.intent.ID {
			return fmt.Errorf("promotion member mismatch")
		}
		s.candidates[c.ID] = payload
		s.retainCandidateRuntime(ctx, c.ID)
		c.StoredBytes = int64(len(payload))
		s.headers[c.ID] = HeaderForCandidate(c)
		next.State = "candidate"
		outcome.CandidateID, outcome.Digest = c.ID, memberResponseDigest(ctx, payload)
	}
	r.memberOutcomes[cursor] = outcome
	s.state.Cursors[m.Index] = next
	r.response.InputMemberBytes -= m.Bytes
	r.response.OutputMemberBytes -= r.outputs[cursor].Bytes + r.corrections[cursor].Bytes
	r.correctionBackings[cursor].drop()
	delete(r.correctionBackings, cursor)
	delete(r.corrections, cursor)
	delete(r.inputs, cursor)
	delete(r.outputs, cursor)
	r.memberBackings[cursor].drop()
	r.outputBackings[cursor].drop()
	delete(r.memberBackings, cursor)
	delete(r.outputBackings, cursor)
	r.response.Cursor++
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "promotion_write", Bytes: int64(len(payload)), Rows: 3})
	r.response.FreezeBytes = r.response.InputMemberBytes
	r.response.CorrectedBytes = r.response.OutputMemberBytes
	if r.response.Cursor == r.response.MemberCount {
		r.response.Phase = "promoted"
		r.intent.Indices = nil
	}
	return nil
}
