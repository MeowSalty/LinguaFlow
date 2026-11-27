package workstate

import (
	"context"
	"crypto/sha256"
	"entgo.io/ent/dialect/sql"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequestinputmember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

type MemberKey = responsecontract.MemberKey

func memberPayloadDigest(ctx context.Context, payload []byte) string {
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "hash", Bytes: int64(len(payload))})
	return payloadDigest(payload)
}

func memberPayloadBound(field string, limit int64) func(*sql.Selector) {
	return func(s *sql.Selector) {
		s.Where(sql.P(func(b *sql.Builder) {
			expression := "COALESCE(LENGTH(CAST(" + s.C(field) + " AS BLOB)),0)"
			if s.Dialect() == "postgres" {
				expression = "COALESCE(OCTET_LENGTH(" + s.C(field) + "),0)"
			}
			b.WriteString(expression + " <= ").Arg(limit)
		}))
	}
}

type MemberContext struct {
	MemberPool, MemberAttempt                        int
	MemberMode, MemberFormat, SourceLang, TargetLang string
	TextMode                                         bool
}
type InputMember struct {
	Key MemberKey
	PromotionMember
	PromptID string
	Index    int
	Payload  []byte
	Bytes    int64
	Digest   string
	Release  func()
}
type OutputMember struct {
	StoredBytes int64
	Key         MemberKey
	Payload     []byte
	Bytes       int64
	Digest      string
	Release     func()
}
type CapturedResponse struct {
	ResponseHeader
	Payload []byte
	Result  RequestResult
	Release func()
}
type OutputPublication struct {
	Key             MemberKey
	Members         []OutputMember
	Next            int
	Digest          string
	Glossary        []byte
	Complete        bool
	ParseDiagnostic string
}

func memberHeaderQuery(q *ent.WorkRequestQuery) *ent.WorkRequestQuery {
	return q.Select(workrequest.FieldID, workrequest.FieldIdentity, workrequest.FieldJobID, workrequest.FieldResourceID, workrequest.FieldJobRoundID, workrequest.FieldRetryEpoch,
		workrequest.FieldRequestMetadataBytes, workrequest.FieldState, workrequest.FieldResponseFormatVersion, workrequest.FieldSnapshotDigest, workrequest.FieldFreezeDigest, workrequest.FieldResponseDigest, workrequest.FieldPromotionCount, workrequest.FieldPromotionCursor, workrequest.FieldPromotionState,
		workrequest.FieldBusinessBound, workrequest.FieldResponseTruncated, workrequest.FieldArchiveCursor, workrequest.FieldArchiveDigest, workrequest.FieldCaptureBytes, workrequest.FieldModelOutputBytes, workrequest.FieldCorrectionBytes, workrequest.FieldPublicKind, workrequest.FieldInputMemberBytes, workrequest.FieldOutputMemberBytes, workrequest.FieldPublicBytes, workrequest.FieldPublicDigest, workrequest.FieldMemberStorageBytes, workrequest.FieldRemainingMembers,
		workrequest.FieldMemberMode, workrequest.FieldMemberFormat, workrequest.FieldSourceLang, workrequest.FieldTargetLang, workrequest.FieldTextMode, workrequest.FieldMemberPool, workrequest.FieldMemberAttempt).WorkRequestQuery
}
func responseContinuationQuery(q *ent.WorkRequestQuery) *ent.WorkRequestQuery {
	return memberHeaderQuery(q).Select(workrequest.FieldStage).WorkRequestQuery
}
func memberRequest(ctx context.Context, tx *ent.Client, key MemberKey) (*ent.WorkRequest, error) {
	return readMemberRequest(ctx, key, memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(key.RequestID))))
}

// Member payload readers use only the request key and phase. Their partial row
// stays local; public response headers continue to use memberHeaderQuery.
func memberAccessRequest(ctx context.Context, tx *ent.Client, key MemberKey) (*ent.WorkRequest, error) {
	q := tx.WorkRequest.Query().Where(workrequest.IdentityEQ(key.RequestID)).Select(workrequest.FieldID, workrequest.FieldRetryEpoch, workrequest.FieldResponseFormatVersion, workrequest.FieldPromotionCount, workrequest.FieldPromotionState)
	return readMemberRequest(ctx, key, q.WorkRequestQuery)
}

func readMemberRequest(ctx context.Context, key MemberKey, q *ent.WorkRequestQuery) (*ent.WorkRequest, error) {
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "header_read", Rows: 1})
	r, err := q.Only(ctx)
	if err != nil {
		return nil, err
	}
	if r.ResponseFormatVersion != execution.ResponseFormatVersion || r.RetryEpoch != key.CaptureEpoch || key.Ordinal < 0 || key.Ordinal >= r.PromotionCount {
		return nil, ErrManifest
	}
	return r, nil
}
func inputPromotion(r *ent.WorkRequestInputMember) PromotionMember {
	return PromotionMember{SegmentID: r.SegmentID, WorkID: r.WorkID, CandidateID: r.CandidateID, BaselineVersion: r.BaselineVersion, BaselineDigest: r.BaselineDigest, BaselineTargetIsNull: r.BaselineTargetIsNull, BaselineStatus: r.BaselineStatus, SourceGeneration: r.SourceGeneration, SourceRevisionID: r.SourceRevisionID}
}

type memberByteReservation struct {
	bounds      *backend.BuildBounds
	allocations [2]*backend.OwnedAllocation
	payload     []byte
	bytes       int64
	once        sync.Once
}

func reserveMemberBytes(ctx context.Context, n int64) (*memberByteReservation, error) {
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	a, err := b.OwnAllocation(n, 1)
	if err != nil {
		b.CloseRetired()
		return nil, err
	}
	return &memberByteReservation{bounds: b, allocations: [2]*backend.OwnedAllocation{a, nil}, bytes: n}, nil
}

func (r *memberByteReservation) complete(p []byte) error {
	if int64(len(p)) != r.bytes {
		return ErrManifest
	}
	r.payload = p
	if extra := int64(cap(p)) - r.bytes; extra > 0 {
		a, err := r.bounds.OwnAllocation(extra, 1)
		if err != nil {
			return err
		}
		r.allocations[1] = a
	}
	return nil
}
func (r *memberByteReservation) release() {
	r.once.Do(func() {
		r.payload = nil
		for _, a := range r.allocations {
			a.Release()
		}
		r.bounds.CloseRetired()
	})
}
func (s *Store) LoadInputMember(ctx context.Context, key MemberKey, limit int64) (InputMember, error) {
	r, err := memberAccessRequest(ctx, memberClient(ctx, s.client), key)
	if err != nil {
		return InputMember{}, err
	}
	if r.PromotionState != "inputs_ready" && r.PromotionState != "capture_pending" && r.PromotionState != "outputs_ready" && r.PromotionState != "business_ready" {
		return InputMember{}, ErrManifest
	}
	q := memberClient(ctx, s.client).WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalEQ(key.Ordinal))
	row, err := q.Clone().Select(workrequestinputmember.FieldID, workrequestinputmember.FieldSegmentID, workrequestinputmember.FieldSegmentIndex, workrequestinputmember.FieldWorkID, workrequestinputmember.FieldCandidateID, workrequestinputmember.FieldBaselineVersion, workrequestinputmember.FieldBaselineDigest, workrequestinputmember.FieldBaselineTargetIsNull, workrequestinputmember.FieldBaselineStatus, workrequestinputmember.FieldSourceGeneration, workrequestinputmember.FieldSourceRevisionID, workrequestinputmember.FieldPromptID, workrequestinputmember.FieldPayloadBytes, workrequestinputmember.FieldPayloadDigest).Only(ctx)
	if err != nil {
		return InputMember{}, err
	}
	if row.PayloadBytes <= 0 || row.PayloadBytes > limit {
		return InputMember{}, ErrManifest
	}
	reservation, err := reserveMemberBytes(ctx, row.PayloadBytes)
	if err != nil {
		return InputMember{}, err
	}
	installed := false
	defer func() {
		if !installed {
			reservation.release()
		}
	}()
	payload, err := q.Where(workrequestinputmember.IDEQ(row.ID), workrequestinputmember.PayloadBytesEQ(row.PayloadBytes), workrequestinputmember.PayloadDigestEQ(row.PayloadDigest), memberPayloadBound(workrequestinputmember.FieldPayload, row.PayloadBytes)).Select(workrequestinputmember.FieldID, workrequestinputmember.FieldPayload).Only(ctx)
	if err != nil {
		return InputMember{}, err
	}
	if int64(len(payload.Payload)) != row.PayloadBytes || memberPayloadDigest(ctx, payload.Payload) != row.PayloadDigest {
		return InputMember{}, ErrManifest
	}
	if err = reservation.complete(payload.Payload); err != nil {
		return InputMember{}, err
	}
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "input_read", Bytes: int64(len(payload.Payload)), Rows: 1})
	release := backend.TrackMemberFrame(ctx, int64(len(payload.Payload)), reservation.release)
	installed = true
	return InputMember{Key: key, PromotionMember: inputPromotion(row), PromptID: row.PromptID, Index: row.SegmentIndex, Payload: payload.Payload, Bytes: row.PayloadBytes, Digest: row.PayloadDigest, Release: release}, nil
}
func (s *Store) LoadOutputMember(ctx context.Context, key MemberKey, final bool, limit int64) (OutputMember, error) {
	r, err := memberAccessRequest(ctx, memberClient(ctx, s.client), key)
	if err != nil {
		return OutputMember{}, err
	}
	if r.PromotionState != "outputs_ready" && r.PromotionState != "business_ready" {
		return OutputMember{}, ErrManifest
	}
	if final && r.PromotionState != "business_ready" {
		return OutputMember{}, ErrManifest
	}
	q := memberClient(ctx, s.client).WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(key.Ordinal))
	row, err := q.Clone().Select(workresponsemember.FieldID, workresponsemember.FieldState, workresponsemember.FieldPayloadBytes, workresponsemember.FieldPayloadDigest, workresponsemember.FieldCorrectedBytes, workresponsemember.FieldCorrectedDigest).Only(ctx)
	if err != nil {
		return OutputMember{}, err
	}
	n, d, field := row.PayloadBytes, row.PayloadDigest, workresponsemember.FieldPayload
	if final && row.CorrectedDigest != "" {
		n, d, field = row.CorrectedBytes, row.CorrectedDigest, workresponsemember.FieldCorrectedPayload
	}
	if row.State != "pending" || n <= 0 || n > limit {
		return OutputMember{}, ErrManifest
	}
	reservation, err := reserveMemberBytes(ctx, n)
	if err != nil {
		return OutputMember{}, err
	}
	installed := false
	defer func() {
		if !installed {
			reservation.release()
		}
	}()
	payload, err := q.Where(workresponsemember.IDEQ(row.ID), workresponsemember.StateEQ("pending"), workresponsemember.PayloadDigestEQ(row.PayloadDigest), workresponsemember.CorrectedDigestEQ(row.CorrectedDigest), memberPayloadBound(field, n)).Select(workresponsemember.FieldID, field).Only(ctx)
	if err != nil {
		return OutputMember{}, err
	}
	p := payload.Payload
	if field == workresponsemember.FieldCorrectedPayload {
		p = payload.CorrectedPayload
	}
	if row.State != "pending" || n <= 0 || int64(len(p)) != n || memberPayloadDigest(ctx, p) != d {
		return OutputMember{}, ErrManifest
	}
	if err = reservation.complete(p); err != nil {
		return OutputMember{}, err
	}
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "output_read", Bytes: int64(len(p)), Rows: 1})
	release := backend.TrackMemberFrame(ctx, int64(len(p)), reservation.release)
	installed = true
	return OutputMember{StoredBytes: row.PayloadBytes + row.CorrectedBytes, Key: key, Payload: p, Bytes: n, Digest: d, Release: release}, nil
}
func (s *Store) captureResponse(ctx context.Context, in CapturedResponse) error {
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "capture", Operation: "capture_write", Bytes: int64(len(in.Payload)), Rows: 1})
	digest := memberPayloadDigest(ctx, in.Payload)
	key := MemberKey{RequestID: in.RequestID, CaptureEpoch: in.Scope.RetryEpoch}
	// Cancelled audit rows can predate the member format. Observe only the
	// active attempt coordinates before decoding that format, so their stopped
	// scope remains the rejection. Supported canonical requests still enter the
	// original transaction/proof path, including pending reliable ACK settlement.
	active, err := memberClient(ctx, s.client).Job.Query().Where(job.IDEQ(in.Scope.JobID)).Select(job.FieldStatus, job.FieldRetryEpoch).Only(ctx)
	if err != nil {
		return err
	}
	stopped := active.RetryEpoch != in.Scope.RetryEpoch || active.Status != "running" && active.Status != "pausing" && active.Status != "pending"
	current, err := memberRequest(ctx, s.client, key)
	if err != nil {
		if stopped && errors.Is(err, ErrManifest) {
			return errors.Join(ErrStopped, err)
		}
		return err
	}
	identity := ResponseIdentityFromRow(current)
	if identity.JobID != in.Scope.JobID || identity.ResourceID != in.Scope.ResourceID || identity.RoundID != in.Scope.RoundID {
		return ErrManifest
	}
	captureBytes := int64(len(in.Payload))
	check := func(factCtx context.Context, tx *ent.Client) (bool, error) {
		r, e := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(identity.RequestID))).Only(factCtx)
		if e != nil {
			return false, e
		}
		if !identity.Matches(r) {
			return false, ErrManifest
		}
		if r.ResponseDigest == "" && r.PromotionState == "inputs_ready" {
			return false, nil
		}
		if r.ResponseDigest != digest || r.State != "completed" || r.PromotionState == "capture_pending" && r.CaptureBytes != captureBytes {
			return false, ErrManifest
		}
		return true, nil
	}
	ctx = WithCommitConfirmation(ctx, s.client, identity.JobID, check)
	err = Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, in.Scope, false); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, in.Scope.JobID); err != nil {
			return err
		}
		r, err := memberRequest(ctx, tx, key)
		if err != nil {
			return err
		}
		if !identity.Matches(r) {
			return ErrManifest
		}
		if r.ResponseDigest != "" {
			if r.ResponseDigest != digest {
				return ErrManifest
			}
			return nil
		}
		if r.PromotionState != "inputs_ready" {
			return ErrManifest
		}
		for start := 0; start < r.PromotionCount; start += 16 {
			rows, err := tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalGTE(start), workrequestinputmember.OrdinalLT(min(start+16, r.PromotionCount))).Select(workrequestinputmember.FieldID, workrequestinputmember.FieldSegmentID).All(ctx)
			if err != nil {
				return err
			}
			if len(rows) != min(start+16, r.PromotionCount)-start {
				return ErrManifest
			}
			ids := make([]int, 0, len(rows))
			for _, m := range rows {
				ids = append(ids, m.SegmentID)
			}
			members, err := tx.WorkItem.Query().Where(workitem.JobRoundIDEQ(in.Scope.RoundID), workitem.SegmentIDIn(ids...), workitem.JobIDEQ(in.Scope.JobID), workitem.ResourceIDEQ(in.Scope.ResourceID)).Select(workitem.FieldID, workitem.FieldState, workitem.FieldRetryEpoch, workitem.FieldCandidateID, workitem.FieldResponseRequestID).All(ctx)
			if err != nil {
				return err
			}
			if len(members) != len(ids) {
				return ErrManifest
			}
			groups := make(map[string][]int)
			for _, w := range members {
				if w.RetryEpoch != in.Scope.RetryEpoch {
					return ErrStopped
				}
				if w.State == "resolved" || w.CandidateID != "" || w.ResponseRequestID != "" && w.ResponseRequestID != in.RequestID {
					return ErrStale
				}
				groups[w.State] = append(groups[w.State], w.ID)
			}
			for state, ids := range groups {
				n, err := tx.WorkItem.Update().Where(workitem.IDIn(ids...), workitem.JobIDEQ(in.Scope.JobID), workitem.ResourceIDEQ(in.Scope.ResourceID), workitem.JobRoundIDEQ(in.Scope.RoundID), workitem.RetryEpochEQ(in.Scope.RetryEpoch), workitem.StateEQ(state), workitem.CandidateIDEQ(""), workitem.Or(workitem.ResponseRequestIDEQ(""), workitem.ResponseRequestIDEQ(in.RequestID))).SetMemberOwned(true).SetState("response_pending").SetResponseRequestID(in.RequestID).SetLastError("").Save(ctx)
				if err != nil {
					return err
				}
				if n != len(ids) {
					return ErrStale
				}
			}
		}
		accounting, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IDEQ(r.ID))).Only(ctx)
		if err != nil {
			return err
		}
		result := in.Result
		result.State = "completed"
		if _, err := recordRequestAccountingTx(ctx, tx, accounting, result); err != nil {
			return err
		}
		return tx.WorkRequest.UpdateOneID(r.ID).SetCapturePayload(in.Payload).SetCaptureBytes(int64(len(in.Payload))).SetResponseDigest(digest).SetResponseTruncated(in.Truncated).SetPromotionState("capture_pending").SetHandoffAt(time.Now().UTC()).AddMemberStorageBytes(int64(len(in.Payload))).Select("id").Exec(ctx)
	})
	return s.finishUnknown(ctx, identity.JobID, err, check)
}
func (s *Store) LoadCapture(ctx context.Context, id string, limit int64) (CapturedResponse, error) {
	h, err := responseContinuationQuery(s.client.WorkRequest.Query().Where(workrequest.IdentityEQ(id), func(sel *sql.Selector) { sel.Where(requestMetadataBounds(sel)) })).Select(workrequest.FieldUsageKnown, workrequest.FieldInputTokens, workrequest.FieldOutputTokens, workrequest.FieldDurationMs).Only(ctx)
	if err != nil {
		return CapturedResponse{}, err
	}
	if h.ResponseFormatVersion != execution.ResponseFormatVersion || h.PromotionState != "capture_pending" || h.CaptureBytes < 0 || h.CaptureBytes > limit {
		return CapturedResponse{}, ErrManifest
	}
	reservation, err := reserveMemberBytes(ctx, h.CaptureBytes)
	if err != nil {
		return CapturedResponse{}, err
	}
	installed := false
	defer func() {
		if !installed {
			reservation.release()
		}
	}()
	row, err := s.client.WorkRequest.Query().Where(workrequest.IDEQ(h.ID), workrequest.ResponseDigestEQ(h.ResponseDigest), workrequest.CaptureBytesEQ(h.CaptureBytes), memberPayloadBound(workrequest.FieldCapturePayload, h.CaptureBytes)).Select(workrequest.FieldID, workrequest.FieldCapturePayload).Only(ctx)
	if err != nil {
		return CapturedResponse{}, err
	}
	if int64(len(row.CapturePayload)) != h.CaptureBytes || memberPayloadDigest(ctx, row.CapturePayload) != h.ResponseDigest {
		return CapturedResponse{}, ErrManifest
	}
	if err = reservation.complete(row.CapturePayload); err != nil {
		return CapturedResponse{}, err
	}
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "capture", Operation: "capture_read", Bytes: int64(len(row.CapturePayload)), Rows: 1})
	installed = true
	return CapturedResponse{ResponseHeader: responseHeader(h), Payload: row.CapturePayload, Result: RequestResult{State: h.State, UsageKnown: h.UsageKnown, InputTokens: h.InputTokens, OutputTokens: h.OutputTokens, DurationMS: h.DurationMs}, Release: reservation.release}, nil
}
func (s *Store) publishOutputs(ctx context.Context, in OutputPublication) error {
	if len(in.Members) > 16 || len(in.ParseDiagnostic) > responsecontract.MainParseDiagnosticLimit || in.ParseDiagnostic != "" && !in.Complete {
		return ErrManifest
	}
	current, err := memberRequest(ctx, s.client, in.Key)
	if err != nil {
		return err
	}
	identity := ResponseIdentityFromRow(current)
	next, archiveDigest, complete := in.Next, in.Digest, in.Complete
	parseDiagnosticBytes := int64(len(in.ParseDiagnostic))
	parseDiagnosticDigest := sha256.Sum256([]byte(in.ParseDiagnostic))
	responseDigest := current.ResponseDigest
	publicBytes, publicDigest := int64(len(in.Glossary)), memberPayloadDigest(ctx, in.Glossary)
	check := func(factCtx context.Context, tx *ent.Client) (bool, error) {
		r, e := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(identity.RequestID))).Only(factCtx)
		if e != nil {
			return false, e
		}
		if !identity.Matches(r) || r.ResponseDigest != responseDigest {
			return false, ErrManifest
		}
		if r.ArchiveCursor < next && r.PromotionState == "capture_pending" {
			return false, nil
		}
		if r.ArchiveCursor != next || r.ArchiveDigest != archiveDigest {
			if r.ArchiveCursor > next {
				return false, errUnverifiable
			}
			return false, ErrManifest
		}
		if complete && (r.CaptureBytes != 0 || r.PromotionState != "outputs_ready" && r.PromotionState != "business_ready" && r.PromotionState != "promoted") {
			return false, ErrManifest
		}
		if complete && r.PromotionState == "outputs_ready" && (r.PublicKind != workrequest.PublicKindEntries || r.PublicBytes != publicBytes || r.PublicDigest != publicDigest) {
			return false, ErrManifest
		}
		if complete && r.PromotionState == "promoted" && (r.PublicKind != workrequest.PublicKindNone || r.PublicBytes != 0) {
			return false, ErrManifest
		}
		if parseDiagnosticBytes > 0 {
			read, err := reserveMemberBytes(factCtx, parseDiagnosticBytes)
			if err != nil {
				return false, err
			}
			defer read.release()
			stored, err := tx.WorkRequest.Query().Where(workrequest.IDEQ(r.ID), memberPayloadBound(workrequest.FieldLastError, parseDiagnosticBytes)).Select(workrequest.FieldID, workrequest.FieldLastError).Only(factCtx)
			if err != nil {
				return false, err
			}
			if int64(len(stored.LastError)) != parseDiagnosticBytes || sha256.Sum256([]byte(stored.LastError)) != parseDiagnosticDigest {
				return false, ErrManifest
			}
		}
		return true, nil
	}
	ctx = WithCommitConfirmation(ctx, s.client, identity.JobID, check)
	err = Transaction(ctx, s.client, func(tx *ent.Client) error {
		r, err := memberRequest(ctx, tx, in.Key)
		if err != nil {
			return err
		}
		if !identity.Matches(r) || r.ResponseDigest != responseDigest {
			return ErrManifest
		}
		if err := LockJob(ctx, tx, r.JobID); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, r.JobID); err != nil {
			return err
		}
		if r.PromotionState == "outputs_ready" || r.PromotionState == "business_ready" || r.PromotionState == "promoted" {
			if !in.Complete || r.ArchiveDigest != in.Digest {
				return ErrManifest
			}
			if in.ParseDiagnostic != "" {
				stored, err := tx.WorkRequest.Query().Where(workrequest.IDEQ(r.ID), workrequest.LastErrorEQ(in.ParseDiagnostic)).Exist(ctx)
				if err != nil || !stored {
					return errors.Join(ErrManifest, err)
				}
			}
			return nil
		}
		if r.PromotionState != "capture_pending" || in.Next > r.PromotionCount {
			return ErrManifest
		}
		if r.ArchiveCursor >= in.Next {
			if r.ArchiveCursor == in.Next && r.ArchiveDigest == in.Digest {
				return nil
			}
			return ErrManifest
		}
		if len(in.Members) != in.Next-r.ArchiveCursor {
			return ErrManifest
		}
		var added, storage int64
		builders := make([]*ent.WorkResponseMemberCreate, 0, len(in.Members))
		var pageBytes int64
		flush := func() error {
			if len(builders) == 0 {
				return nil
			}
			err := tx.WorkResponseMember.CreateBulk(builders...).Exec(ctx)
			builders = builders[:0]
			pageBytes = 0
			return err
		}
		for i, m := range in.Members {
			if m.Key.Ordinal != r.ArchiveCursor+i || m.Key.RequestID != r.Identity || m.Key.CaptureEpoch != r.RetryEpoch || int64(len(m.Payload)) != m.Bytes || memberPayloadDigest(ctx, m.Payload) != m.Digest {
				return ErrManifest
			}
			if len(builders) > 0 && pageBytes+m.Bytes > bulkJSONBytes {
				if err := flush(); err != nil {
					return err
				}
			}
			builders = append(builders, tx.WorkResponseMember.Create().SetRequestID(r.ID).SetOrdinal(m.Key.Ordinal).SetPayload(m.Payload).SetPayloadBytes(m.Bytes).SetPayloadDigest(m.Digest))
			pageBytes += m.Bytes
			added += m.Bytes
			backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "output_write", Bytes: m.Bytes, Rows: 1})
			storage += m.Bytes + int64(len(m.Digest))
		}
		if err := flush(); err != nil {
			return err
		}
		u := tx.WorkRequest.UpdateOneID(r.ID).SetArchiveCursor(in.Next).SetArchiveDigest(in.Digest).AddOutputMemberBytes(added).AddModelOutputBytes(added).AddMemberStorageBytes(storage)
		if in.Complete {
			if in.Next != r.PromotionCount {
				return ErrManifest
			}
			u.SetPromotionState("outputs_ready").SetPublicKind(workrequest.PublicKindEntries).ClearCapturePayload().SetCaptureBytes(0).SetResponseGlossary(in.Glossary).SetPublicDigest(memberPayloadDigest(ctx, in.Glossary)).AddPublicBytes(int64(len(in.Glossary))).AddMemberStorageBytes(int64(len(in.Glossary)) - r.CaptureBytes)
			if in.ParseDiagnostic != "" {
				u.SetLastError(in.ParseDiagnostic).AddMemberStorageBytes(int64(len(in.ParseDiagnostic)))
			}
		}
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "checkpoint_write", Rows: 1})
		return u.Select(workrequest.FieldID).Exec(ctx)
	})
	return s.finishUnknown(ctx, identity.JobID, err, check)
}

// LoadMainParseDiagnostic reads the bounded reason already recorded by archive.
// The caller reserves the retained text before entering the store.
func (s *Store) LoadMainParseDiagnostic(ctx context.Context, scope Scope, requestID string) (string, error) {
	r, err := s.client.WorkRequest.Query().Where(workrequest.IdentityEQ(requestID), workrequest.JobIDEQ(scope.JobID), workrequest.ResourceIDEQ(scope.ResourceID), workrequest.JobRoundIDEQ(scope.RoundID), workrequest.StageEQ("main"), workrequest.PromotionStateEQ("promoted"), memberPayloadBound(workrequest.FieldLastError, responsecontract.MainParseDiagnosticLimit)).Select(workrequest.FieldID, workrequest.FieldLastError).Only(ctx)
	if err != nil {
		return "", err
	}
	return r.LastError, nil
}
func (s *Store) PublishMemberBusiness(ctx context.Context, id string, receipt []byte) error {
	current, err := memberHeaderQuery(memberClient(ctx, s.client).WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
	if err != nil {
		return err
	}
	identity := ResponseIdentityFromRow(current)
	responseDigest := current.ResponseDigest
	receiptBytes, receiptDigest := int64(len(receipt)), memberPayloadDigest(ctx, receipt)
	check := func(factCtx context.Context, tx *ent.Client) (bool, error) {
		r, e := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(identity.RequestID))).Only(factCtx)
		if e != nil {
			return false, e
		}
		if !identity.Matches(r) || r.ResponseDigest != responseDigest {
			return false, ErrManifest
		}
		if r.PromotionState == "outputs_ready" {
			return false, nil
		}
		if r.PromotionState == "promoted" && r.PublicKind == workrequest.PublicKindNone && r.PublicBytes == 0 {
			row, e := tx.WorkRequest.Query().Where(workrequest.IDEQ(r.ID), memberPayloadBound(workrequest.FieldGlossaryReceipt, responseReceiptReadBytes)).Select(workrequest.FieldID, workrequest.FieldGlossaryReceipt).Only(factCtx)
			if e != nil {
				return false, e
			}
			if int64(len(row.GlossaryReceipt)) != receiptBytes || memberPayloadDigest(factCtx, row.GlossaryReceipt) != receiptDigest || r.PublicDigest != receiptDigest {
				return false, ErrManifest
			}
			return true, nil
		}
		if r.PromotionState != "business_ready" || r.PublicKind != workrequest.PublicKindReceipt || r.PublicBytes != receiptBytes || r.PublicDigest != receiptDigest {
			return false, ErrManifest
		}
		return true, nil
	}
	ctx = WithCommitConfirmation(ctx, s.client, identity.JobID, check)
	err = memberTransaction(ctx, s.client, func(tx *ent.Client) error {
		r, e := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
		if e != nil {
			return e
		}
		if !identity.Matches(r) || r.ResponseDigest != responseDigest {
			return ErrManifest
		}
		if e = guardExecution(ctx, tx, r.JobID); e != nil {
			return e
		}
		if r.PromotionState == "promoted" {
			confirmed, e := check(ctx, tx)
			if e != nil {
				return e
			}
			if !confirmed {
				return ErrManifest
			}
			return nil
		}
		if r.PromotionState == "business_ready" {
			if r.PublicBytes == receiptBytes && r.PublicDigest == receiptDigest && r.PublicKind == workrequest.PublicKindReceipt {
				return nil
			}
			return ErrManifest
		}
		if r.PromotionState != "outputs_ready" {
			return ErrManifest
		}
		public, e := s.LoadResponsePublic(WithBusinessTransaction(ctx, tx), id, backend.DefaultMaxResponseBytes)
		if e != nil {
			return e
		}
		public.Release()
		if len(receipt) > 0 {
			if e = validateResponseReceipt(receipt, id); e != nil {
				return e
			}
		}
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "business_write", Bytes: int64(len(receipt)), Rows: 1})
		update := tx.WorkRequest.UpdateOneID(r.ID).SetPromotionState("business_ready").SetPublicKind(workrequest.PublicKindReceipt).ClearResponseGlossary().SetPublicDigest(memberPayloadDigest(ctx, receipt)).SetPublicBytes(int64(len(receipt))).AddMemberStorageBytes(int64(len(receipt)) - r.PublicBytes)
		if len(receipt) == 0 {
			update.ClearGlossaryReceipt()
		} else {
			update.SetGlossaryReceipt(receipt)
		}
		return update.Select(workrequest.FieldID).Exec(ctx)
	})
	if _, inside := ctx.Value(businessTransactionKey{}).(*ent.Client); inside {
		return err
	}
	return s.finishUnknown(ctx, identity.JobID, err, check)
}
func saveInputMembersTx(ctx context.Context, tx *ent.Client, r *ent.WorkRequest, in Request) error {
	if len(in.InputMembers) == 0 {
		return nil
	}
	var n, storage int64
	if len(in.InputMembers) != len(in.SegmentIDs) {
		return ErrManifest
	}
	expected := make(map[int]bool, len(in.SegmentIDs))
	for _, id := range in.SegmentIDs {
		expected[id] = true
	}
	promptIDs, candidateIDs := map[string]bool{}, map[string]bool{}
	metadataBytes, err := loadRequestMetadata(ctx, tx, r.Identity)
	if err != nil {
		return err
	}
	builders := make([]*ent.WorkRequestInputMemberCreate, 0, 16)
	flush := func() error {
		if len(builders) == 0 {
			return nil
		}
		err := tx.WorkRequestInputMember.CreateBulk(builders...).Exec(ctx)
		builders = builders[:0]
		return err
	}
	var pageBytes int64
	for i, m := range in.InputMembers {
		if !expected[m.SegmentID] || promptIDs[m.PromptID] || candidateIDs[m.CandidateID] || m.Key.CaptureEpoch != in.Scope.RetryEpoch || m.Key.Ordinal != i || m.Key.RequestID != r.Identity || m.PromptID == "" || m.CandidateID == "" || len(m.Payload) == 0 || m.Digest != memberPayloadDigest(ctx, m.Payload) {
			return ErrManifest
		}
		delete(expected, m.SegmentID)
		promptIDs[m.PromptID] = true
		candidateIDs[m.CandidateID] = true
		pd := m.BaselineDigest
		if pd == "" {
			pd = stringDigest(m.BaselineTarget)
		}
		// Preserve valid oversized singleton inputs while keeping multi-member
		// SQL submissions within the existing payload window.
		if len(builders) > 0 && pageBytes+int64(len(m.Payload)) > bulkJSONBytes {
			if err := flush(); err != nil {
				return err
			}
			pageBytes = 0
		}
		builders = append(builders, tx.WorkRequestInputMember.Create().SetRequestID(r.ID).SetOrdinal(i).SetPromptID(m.PromptID).SetSegmentID(m.SegmentID).SetSegmentIndex(m.Index).SetWorkID(m.WorkID).SetCandidateID(m.CandidateID).SetBaselineVersion(m.BaselineVersion).SetBaselineDigest(pd).SetBaselineTargetIsNull(m.BaselineTarget == nil).SetBaselineStatus(m.BaselineStatus).SetSourceGeneration(in.Scope.SourceGeneration).SetNillableSourceRevisionID(in.Scope.SourceRevisionID).SetPayload(m.Payload).SetPayloadBytes(int64(len(m.Payload))).SetPayloadDigest(m.Digest))
		pageBytes += int64(len(m.Payload))
		if len(builders) == 16 {
			if err := flush(); err != nil {
				return err
			}
			pageBytes = 0
		}
		n += int64(len(m.Payload))
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "freeze", Operation: "input_write", Bytes: int64(len(m.Payload)), Rows: 1})
		storage += int64(len(m.Payload) + len(m.PromptID) + len(m.WorkID) + len(m.CandidateID) + len(pd) + len(m.BaselineStatus) + len(m.Digest))
	}
	if err := flush(); err != nil {
		return err
	}
	return tx.WorkRequest.UpdateOneID(r.ID).SetRequestMetadataBytes(metadataBytes).SetMemberMode(in.MemberMode).SetMemberFormat(in.MemberFormat).SetSourceLang(in.SourceLang).SetTargetLang(in.TargetLang).SetTextMode(in.TextMode).SetMemberPool(in.MemberPool).SetMemberAttempt(in.MemberAttempt).SetSnapshotDigest(in.SnapshotDigest).SetFreezeDigest(in.InputDigest).SetResponseFormatVersion(execution.ResponseFormatVersion).SetPromotionCount(len(in.InputMembers)).SetRemainingMembers(len(in.InputMembers)).SetBusinessBound(in.BusinessBound).SetInputMemberBytes(n).SetMemberStorageBytes(storage).SetPromotionState("inputs_ready").Select("id").Exec(ctx)
}

func memberStateError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrManifest, fmt.Sprintf(format, args...))
}
