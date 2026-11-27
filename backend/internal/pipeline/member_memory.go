package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"strings"
	"sync"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type memberBacking struct {
	mu            sync.Mutex
	refs          int
	storeRetired  bool
	refundPending bool
	releasing     bool
	payload       []byte
	release       func() error
	retiredNext   *memberBacking
	prepared      *backend.PreparedAllocationTransfer
	source        *backend.OwnedAllocation
	clearObserved func()
}
type memoryMemberOutcome struct {
	CandidateID, Digest string
	Failed              bool
	Outcome             responsecontract.MemberOutcome
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
	if b.refs == 0 {
		b.mu.Unlock()
		return nil
	}
	b.refs--
	if b.refs > 0 {
		b.mu.Unlock()
		return nil
	}
	prepared, source := b.prepared, b.source
	release := b.release
	clearObserved := b.clearObserved
	b.clearObserved = nil
	b.refundPending = false
	b.releasing = true
	// The final borrower has joined. Retire physical backing before any refund
	// can wake another allocator; failures retain only conservative refund rights.
	b.payload = nil
	b.mu.Unlock()
	if clearObserved != nil {
		clearObserved()
	}
	if prepared != nil {
		if err := prepared.Rollback(); err != nil {
			b.mu.Lock()
			b.refs = 1
			b.refundPending = true
			b.releasing = false
			b.mu.Unlock()
			return err
		}
		source.Release()
	} else if release != nil {
		if err := release(); err != nil {
			b.mu.Lock()
			b.refs = 1
			b.refundPending = true
			b.releasing = false
			b.mu.Unlock()
			return err
		}
	}
	b.mu.Lock()
	b.payload = nil
	b.prepared = nil
	b.source = nil
	b.release = nil
	b.releasing = false
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
	return &memberBacking{refs: 1, payload: p, source: a, prepared: plan, clearObserved: func() { admission.ObserveOwned("durable_member", owner, 0) }, release: func() error { return admission.ReleaseBuild(owner) }}, nil
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
		return &memberBacking{refs: 1, payload: p, clearObserved: func() { admission.ObserveOwned("durable_member", owner, 0) }, release: func() error { return admission.ReleaseBuild(owner) }}, nil
	}
	transferred := a.Transfer()
	if transferred == nil {
		return nil, backend.ErrBuildCapacity
	}
	return &memberBacking{refs: 1, payload: p, release: func() error { transferred.Release(); return nil }}, nil
}

// Retired store ownership stays reachable until every borrower has joined and
// its refund succeeds. A failed retirement never undoes published facts.
func (r *memoryResponse) retireBacking(b *memberBacking) {
	if b != nil {
		b.retiredNext = r.retiredBackings
		r.retiredBackings = b
	}
}
func (r *memoryResponse) retryRetiredBackings() error {
	var result error
	for link := &r.retiredBackings; *link != nil; {
		b := *link
		b.mu.Lock()
		retireStore := !b.storeRetired
		if retireStore {
			b.storeRetired = true
		}
		retryRefund := b.refundPending && !b.releasing
		b.mu.Unlock()
		if retireStore || retryRefund {
			if err := b.drop(); err != nil {
				result = errors.Join(result, err)
				link = &b.retiredNext
				continue
			}
		}
		b.mu.Lock()
		done := b.refs == 0 && !b.releasing && !b.refundPending
		b.mu.Unlock()
		if !done {
			link = &b.retiredNext
			continue
		}
		*link = b.retiredNext
		b.retiredNext = nil
	}
	return result
}

func (s *MemoryRoundStore) retryFailedReservations() error {
	s.retirement.Lock()
	defer s.retirement.Unlock()
	s.mu.Lock()
	failed := s.retiredResponses
	s.retiredResponses = nil
	s.mu.Unlock()
	var result error
	for link := &failed; *link != nil; {
		r := *link
		err := r.retryRetiredBackings()
		if err == nil && r.retiredBackings == nil && r.historyAdmission != nil {
			err = r.historyAdmission.ReleaseBuild(r.historyOwner)
		}
		result = errors.Join(result, err)
		if err != nil || r.retiredBackings != nil {
			link = &r.retiredNext
			continue
		}
		*link = r.retiredNext
		*r = memoryResponse{}
	}
	s.mu.Lock()
	if failed != nil {
		tail := failed
		for tail.retiredNext != nil {
			tail = tail.retiredNext
		}
		tail.retiredNext = s.retiredResponses
		s.retiredResponses = failed
	}
	s.mu.Unlock()
	return result
}
func (s *MemoryRoundStore) reserveMembers(ctx context.Context, in RequestIntent) error {
	if s.closed {
		return backend.ErrAdmissionClosed
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
			for _, b := range r.memberBackings {
				r.retireBacking(b)
			}
			r.inputs, r.memberBackings = nil, nil
			r.intent = RequestIntent{}
			r.record = RequestRecord{}
			r.retiredNext = s.retiredResponses
			s.retiredResponses = r
		}
	}()
	var n int64
	if len(in.InputMembers) != len(in.Indices) {
		return fmt.Errorf("invalid input member count")
	}
	if in.PromptUpgrade {
		for _, idx := range in.Indices {
			cursor := s.state.Cursors[idx]
			if !IsPendingPromptUpgrade(cursor.Phase) || cursor.NetworkAttempt != 1 {
				return fmt.Errorf("prompt upgrade budget exhausted")
			}
		}
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
	r.response.MainResponseHeader = MainResponseHeader{CaptureEpoch: epoch, FrozenContext: in.FrozenContext, ID: in.ID, DatabaseID: s.nextRequest, MemberCount: len(in.InputMembers), PublicKind: responsecontract.PublicNone, FormatVersion: execution.ResponseFormatVersion, SnapshotDigest: in.SnapshotDigest, FreezeDigest: in.InputDigest, InputMemberBytes: n, Phase: "inputs_ready", BusinessBound: in.BusinessBound}
	s.requests[in.ID] = r
	for _, backing := range r.memberBackings {
		backing.commit()
	}
	installed = true
	for _, idx := range in.Indices {
		cur := s.state.Cursors[idx]
		if in.Stage == backend.RequestStageMain {
			if in.Phase == promptUpgradePhase {
				cur.NetworkAttempt++
			} else {
				cur.Attempt++
			}
		}
		s.state.Cursors[idx] = cur
	}
	return nil
}
func (s *MemoryRoundStore) memberLocked(key MemberKey) (*memoryResponse, error) {
	if s.closed {
		return nil, backend.ErrAdmissionClosed
	}
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
func (s *MemoryRoundStore) captureResponse(ctx context.Context, in CapturedResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return backend.ErrAdmissionClosed
	}
	r := s.requests[in.ID]
	if r == nil {
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
	r.response.CaptureBytes = int64(len(in.Payload))
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
	if s.closed {
		return CapturedResponse{}, backend.ErrAdmissionClosed
	}
	r := s.requests[id]
	if r == nil || r.response.Phase != "capture_pending" || r.response.CaptureBytes > limit {
		return CapturedResponse{}, fmt.Errorf("capture unavailable")
	}
	p, release := r.capture.borrow()
	if int64(len(p)) != r.response.CaptureBytes || memberResponseDigest(ctx, p) != r.response.ResponseDigest {
		release()
		return CapturedResponse{}, fmt.Errorf("capture integrity mismatch")
	}
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "capture", Operation: "capture_read", Bytes: int64(len(p)), Rows: 1})
	return CapturedResponse{MainResponseHeader: r.response.MainResponseHeader, Payload: p, Record: r.record, Release: release}, nil
}
func (s *MemoryRoundStore) publishOutputs(ctx context.Context, in OutputPublication) (result error) {
	s.mu.Lock()
	r, err := s.memberLocked(in.Key)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	defer func() {
		s.mu.Unlock()
	}()
	if len(in.Members) > 16 {
		return fmt.Errorf("archive page exceeds bound")
	}
	if len(in.ParseDiagnostic) > responsecontract.MainParseDiagnosticLimit || in.ParseDiagnostic != "" && !in.Complete {
		return fmt.Errorf("invalid main parse diagnostic")
	}
	for ordinal, backing := range r.archiveStaging {
		r.retireBacking(backing)
		delete(r.archiveStaging, ordinal)
	}
	if r.response.ArchiveCursor == in.Next && r.response.ArchiveDigest == in.Digest {
		if in.ParseDiagnostic != "" && r.record.Error != in.ParseDiagnostic {
			return fmt.Errorf("main parse diagnostic changed")
		}
		return nil
	}
	if r.response.Phase != "capture_pending" || len(in.Members) != in.Next-r.response.ArchiveCursor {
		return fmt.Errorf("archive checkpoint mismatch")
	}
	if in.Complete && in.Next != r.response.MemberCount {
		return fmt.Errorf("incomplete archive")
	}
	staged := map[int]*memberBacking{}
	var public, parseDiagnostic *memberBacking
	installed := false
	defer func() {
		if !installed {
			for _, b := range staged {
				r.retireBacking(b)
			}
			r.retireBacking(public)
			r.retireBacking(parseDiagnostic)
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
	if in.Complete && len(in.Glossary) > 0 {
		public, err = prepareMemberBacking(ctx, in.Glossary, in.PublicAllocation, "public:"+in.Key.RequestID)
		if err != nil {
			return err
		}
	}
	if in.ParseDiagnostic != "" {
		// Recovery can publish new facts after the runtime that originally
		// reserved this request has closed. Charge the archive's current owner.
		if admission, _ := backend.BuildOwnership(ctx); admission != nil {
			owner := "parse-diagnostic:" + s.identity + ":" + in.Key.RequestID
			bytes := int64(len(in.ParseDiagnostic)+len(owner)) + int64(unsafe.Sizeof(memberBacking{}))
			if err := admission.ClaimBuildBounded(owner, bytes, backend.BuildAllocationLimit(ctx)); err != nil {
				return err
			}
			parseDiagnostic = &memberBacking{refs: 1, release: func() error { return admission.ReleaseBuild(owner) }}
		}
		r.record.Error = strings.Clone(in.ParseDiagnostic)
	}
	for _, m := range in.Members {
		staged[m.Key.Ordinal].commit()
		m.Allocation = nil
		m.Release = nil
		r.outputs[m.Key.Ordinal] = m
		r.outputBackings[m.Key.Ordinal] = staged[m.Key.Ordinal]
		r.response.OutputMemberBytes += m.Bytes
		r.response.ModelOutputBytes += m.Bytes
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
		r.response.CaptureBytes = 0
		r.response.Receipt = in.Glossary
		r.publicBacking = public
		r.parseDiagnosticBacking = parseDiagnostic
		r.response.PublicBytes = int64(len(in.Glossary))
		r.response.PublicKind = responsecontract.PublicEntries
		r.publicDigest = sha256.Sum256(in.Glossary)
		r.retireBacking(r.capture)
		r.capture = nil
	}
	installed = true
	return nil
}

func (s *MemoryRoundStore) LoadMainParseDiagnostic(_ context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if s.closed || r == nil || r.response.Phase != responsecontract.Promoted {
		return "", fmt.Errorf("main parse diagnostic unavailable")
	}
	return r.record.Error, nil
}
func (s *MemoryRoundStore) publishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) (result error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return backend.ErrAdmissionClosed
	}
	r := s.requests[id]
	if r == nil {
		s.mu.Unlock()
		return fmt.Errorf("missing response")
	}
	defer func() {
		s.mu.Unlock()
	}()
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
		r.response.CorrectionBytes += m.Bytes
	}
	r.stagedCorrections = map[int]OutputMemberFrame{}
	r.stagedCorrectionBackings = map[int]*memberBacking{}
	old := r.publicBacking
	r.publicBacking = receipt
	receipt.commit()
	r.response.Receipt = out.Receipt
	r.response.PublicBytes = int64(len(out.Receipt))
	r.response.PublicKind = responsecontract.PublicReceipt
	r.publicDigest = sha256.Sum256(out.Receipt)
	r.response.Phase = "business_ready"
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "business_write", Bytes: int64(len(out.Receipt)), Rows: 1})
	if install != nil {
		install()
	}
	r.retireBacking(old)
	return nil
}
func (s *MemoryRoundStore) LoadResponsePublic(ctx context.Context, id string, limit int64) (OutputMemberFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return OutputMemberFrame{}, backend.ErrAdmissionClosed
	}
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
	s.mutation.Lock()
	defer s.mutation.Unlock()
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
func (s *MemoryRoundStore) AbortMemberBusiness(ctx context.Context, id string) (result error) {
	s.mutation.Lock()
	s.mu.Lock()
	r := s.requests[id]
	defer func() {
		s.mu.Unlock()
		s.mutation.Unlock()
		if !errors.Is(result, backend.ErrAdmissionClosed) {
			result = errors.Join(result, s.RetireResponse(ctx, id))
		}
	}()
	if s.closed {
		return backend.ErrAdmissionClosed
	}
	if r != nil && r.response.Phase != "business_ready" {
		for ordinal, b := range r.stagedCorrectionBackings {
			r.retireBacking(b)
			delete(r.stagedCorrectionBackings, ordinal)
			delete(r.stagedCorrections, ordinal)
		}
		return nil
	}
	return nil
}

func (s *MemoryRoundStore) promoteMemberLocked(ctx context.Context, r *memoryResponse, cursor int, c *Candidate, next WorkCursor, failure MemberOutcome, payload []byte) error {
	if cursor < 0 || cursor >= r.response.MemberCount {
		return fmt.Errorf("invalid promotion")
	}
	if _, err := failure.Resolve(MemberKey{RequestID: r.response.ID, Ordinal: cursor, CaptureEpoch: r.response.CaptureEpoch}, c != nil); err != nil {
		return err
	}
	if outcome, ok := r.memberOutcomes[cursor]; ok {
		if outcome.Outcome != failure {
			return fmt.Errorf("promotion outcome identity reused")
		}
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
	outcome := memoryMemberOutcome{Failed: c == nil, Outcome: failure}
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
	r.response.ModelOutputBytes -= r.outputs[cursor].Bytes
	r.response.CorrectionBytes -= r.corrections[cursor].Bytes
	r.retireBacking(r.correctionBackings[cursor])
	delete(r.correctionBackings, cursor)
	delete(r.corrections, cursor)
	delete(r.inputs, cursor)
	delete(r.outputs, cursor)
	r.retireBacking(r.memberBackings[cursor])
	r.retireBacking(r.outputBackings[cursor])
	delete(r.memberBackings, cursor)
	delete(r.outputBackings, cursor)
	r.response.Cursor++
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "promotion_write", Bytes: int64(len(payload)), Rows: 3})
	if r.response.Cursor == r.response.MemberCount {
		r.response.Phase = "promoted"
		// Receipt remains an audit borrower until store retirement; processing
		// ownership no longer includes public payload or business reservation.
		r.response.MetadataBytes += r.response.PublicBytes
		r.response.PublicBytes = 0
		r.response.PublicKind = responsecontract.PublicNone
		r.response.BusinessBound = 0
		r.intent.Indices = nil
	}
	return nil
}
