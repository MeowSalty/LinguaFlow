package workstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequestinputmember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"io"
)

type PromotionMember struct {
	ReleaseOwned         func()  `json:"-"`
	SegmentID            int     `json:"segment_id"`
	WorkID               string  `json:"work_id"`
	CandidateID          string  `json:"candidate_id"`
	BaselineVersion      int64   `json:"baseline_version"`
	BaselineTarget       *string `json:"-"`
	BaselineDigest       string  `json:"baseline_digest"`
	BaselineTargetIsNull bool    `json:"baseline_target_is_null"`
	BaselineStatus       string  `json:"baseline_status"`
	SourceGeneration     int64   `json:"source_generation"`
	SourceRevisionID     *int    `json:"source_revision_id"`
}

type ResponseHeader struct {
	MemberContext
	FormatVersion                                    int
	ArchiveCursor                                    int
	ArchiveDigest                                    string
	InputMemberBytes, OutputMemberBytes, PublicBytes int64
	MetadataBytes                                    int64
	DatabaseID                                       int
	RequestID                                        string
	Scope                                            Scope
	SnapshotDigest                                   string
	FreezeDigest                                     string
	ResponseDigest                                   string
	CaptureBytes                                     int64
	ModelOutputBytes, CorrectionBytes                int64
	PublicKind                                       responsecontract.PublicKind
	BusinessBound                                    int64
	MemberCount                                      int
	Cursor                                           int
	Phase                                            responsecontract.ResponsePhase
	Truncated                                        bool
}

type MemberPromotion struct {
	Scope     Scope
	Key       MemberKey
	Candidate *Candidate
	Next      Cursor
	Outcome   responsecontract.MemberOutcome
}

func payloadDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func guardExecution(ctx context.Context, tx *ent.Client, jobID int) error {
	return CheckExecution(ctx, tx, jobID)
}

// Promotion retains every stable identity field, but neither preflight nor its
// locked proof consumes response accounting or business context. Only the
// locked transition and ACK verification additionally consume cursor and phase.
func memberPromotionIdentityQuery(q *ent.WorkRequestQuery) *ent.WorkRequestQuery {
	return q.Select(workrequest.FieldID, workrequest.FieldIdentity, workrequest.FieldJobID, workrequest.FieldResourceID, workrequest.FieldJobRoundID, workrequest.FieldRetryEpoch, workrequest.FieldResponseFormatVersion, workrequest.FieldPromotionCount, workrequest.FieldSnapshotDigest, workrequest.FieldFreezeDigest, workrequest.FieldResponseDigest).WorkRequestQuery
}

func memberPromotionStateQuery(q *ent.WorkRequestQuery) *ent.WorkRequestQuery {
	return memberPromotionIdentityQuery(q).Select(workrequest.FieldPromotionCursor, workrequest.FieldPromotionState).WorkRequestQuery
}

func responseHeader(row *ent.WorkRequest) ResponseHeader {
	return ResponseHeader{MemberContext: MemberContext{MemberPool: row.MemberPool, MemberAttempt: row.MemberAttempt, MemberMode: row.MemberMode, MemberFormat: row.MemberFormat, SourceLang: row.SourceLang, TargetLang: row.TargetLang, TextMode: row.TextMode}, FormatVersion: row.ResponseFormatVersion, ArchiveCursor: row.ArchiveCursor, ArchiveDigest: row.ArchiveDigest, InputMemberBytes: row.InputMemberBytes, OutputMemberBytes: row.OutputMemberBytes, PublicBytes: row.PublicBytes, MetadataBytes: row.RequestMetadataBytes + row.MemberStorageBytes - row.InputMemberBytes - row.OutputMemberBytes - row.CaptureBytes - row.PublicBytes, DatabaseID: row.ID, RequestID: row.Identity, Scope: Scope{JobID: row.JobID, ResourceID: row.ResourceID, RoundID: row.JobRoundID, RetryEpoch: row.RetryEpoch}, SnapshotDigest: row.SnapshotDigest, FreezeDigest: row.FreezeDigest, ResponseDigest: row.ResponseDigest, CaptureBytes: row.CaptureBytes, ModelOutputBytes: row.ModelOutputBytes, CorrectionBytes: row.CorrectionBytes, PublicKind: responsecontract.PublicKind(row.PublicKind), BusinessBound: row.BusinessBound, MemberCount: row.PromotionCount, Cursor: row.PromotionCursor, Phase: responsecontract.ResponsePhase(row.PromotionState), Truncated: row.ResponseTruncated}
}

// Headers deliberately include earlier retry epochs. Only reliable responses
// survive an epoch change; an uncertain invocation cannot enter this query.
func (s *Store) LoadResponseHeaders(ctx context.Context, jobID, after, limit int) ([]ResponseHeader, int, error) {
	if limit < 1 || limit > 256 {
		return nil, after, ErrManifest
	}
	rows, err := memberHeaderQuery(s.client.WorkRequest.Query().Where(workrequest.JobIDEQ(jobID), workrequest.IDGT(after), workrequest.StateEQ("completed"), workrequest.PromotionStateIn("capture_pending", "outputs_ready", "business_ready")).Order(ent.Asc(workrequest.FieldID)).Limit(limit)).All(ctx)
	if err != nil {
		return nil, after, err
	}
	out := make([]ResponseHeader, 0, len(rows))
	for _, r := range rows {
		if r.ResponseFormatVersion != execution.ResponseFormatVersion {
			return nil, after, ErrManifest
		}
		h := responseHeader(r)
		out = append(out, h)
		after = r.ID
	}
	return out, after, nil
}

func HasPendingResponse(ctx context.Context, tx *ent.Client, w *ent.WorkItem) (bool, error) {
	return pendingMemberResponse(ctx, tx, w)
}

func ExecutionSnapshotDigest(config map[string]any) (string, error) {
	if err := execution.ValidateExecutionConfigFormat(config); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	var spec execution.JobExecutionSnapshot
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return "", err
	}
	encoded, err = json.Marshal(&spec)
	if err != nil {
		return "", err
	}
	return payloadDigest(encoded), nil
}

// PromoteMember transfers one raw member to the existing candidate contract.
// The candidate, retry cursor, request cursor and target cleanup commit together.
func (s *Store) promoteMember(ctx context.Context, in MemberPromotion) error {
	if in.Key.Ordinal < 0 || in.Key.RequestID == "" {
		return ErrManifest
	}
	if _, err := in.Outcome.Resolve(in.Key, in.Candidate != nil); err != nil {
		return ErrManifest
	}
	failure := in.Outcome.String()
	current, err := readMemberRequest(ctx, in.Key, memberPromotionIdentityQuery(s.client.WorkRequest.Query().Where(workrequest.IdentityEQ(in.Key.RequestID))))
	if err != nil {
		return err
	}
	identity := ResponseIdentityFromRow(current)
	if identity.JobID != in.Scope.JobID || identity.ResourceID != in.Scope.ResourceID || identity.RoundID != in.Scope.RoundID {
		return ErrManifest
	}
	proof := memberPromotionProof{Identity: identity, Key: in.Key, ResponseDigest: current.ResponseDigest, Outcome: in.Outcome}
	if in.Candidate != nil {
		proof.CandidateID, proof.CandidateVersion, proof.CandidateDigest = in.Candidate.ID, in.Candidate.Version, memberPayloadDigest(ctx, in.Candidate.Payload)
	}
	// The pending callback captures scalar proof and the store, never the
	// candidate encoding or an expired transaction/build context.
	ctx = WithCommitConfirmation(ctx, s.client, proof.Identity.JobID, proof.exists)
	err = Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, in.Scope, false); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, in.Scope.JobID); err != nil {
			return err
		}
		r, err := memberPromotionStateQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.Key.RequestID))).Only(ctx)
		if err != nil {
			return err
		}
		if !proof.headerMatches(r) {
			return ErrManifest
		}
		input, err := tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalEQ(in.Key.Ordinal)).Select(workrequestinputmember.FieldID, workrequestinputmember.FieldSegmentID, workrequestinputmember.FieldWorkID, workrequestinputmember.FieldCandidateID, workrequestinputmember.FieldBaselineVersion, workrequestinputmember.FieldBaselineDigest, workrequestinputmember.FieldBaselineTargetIsNull, workrequestinputmember.FieldBaselineStatus, workrequestinputmember.FieldSourceGeneration, workrequestinputmember.FieldSourceRevisionID, workrequestinputmember.FieldPayloadBytes).Only(ctx)
		if err != nil {
			return err
		}
		output, err := tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(in.Key.Ordinal)).Select(workresponsemember.FieldID, workresponsemember.FieldState, workresponsemember.FieldPayloadBytes, workresponsemember.FieldCorrectedBytes, workresponsemember.FieldCandidateDigest, workresponsemember.FieldCandidateVersion, workresponsemember.FieldOutcomeKind, workresponsemember.FieldOutcomeLimit, workresponsemember.FieldOutcomeObserved).Only(ctx)
		if err != nil {
			return err
		}
		if output.State != "pending" {
			return proof.verify(ctx, tx, r, input, output)
		}
		if r.PromotionCursor != in.Key.Ordinal || r.PromotionState != "business_ready" {
			return ErrCandidateVersion
		}
		m := inputPromotion(input)
		w, err := member(ctx, tx, in.Scope, m.SegmentID)
		if err != nil {
			return err
		}
		if w.ResponseRequestID != in.Key.RequestID || w.State != "response_pending" {
			return ErrManifest
		}
		if m.SourceGeneration != in.Scope.SourceGeneration || !sameInt(m.SourceRevisionID, in.Scope.SourceRevisionID) {
			return fmt.Errorf("%w: promote_member source_scope_changed request=%s ordinal=%d stored_generation=%d active_generation=%d revision_match=%t", ErrStale, in.Key.RequestID, in.Key.Ordinal, m.SourceGeneration, in.Scope.SourceGeneration, sameInt(m.SourceRevisionID, in.Scope.SourceRevisionID))
		}
		state := "failed"
		candidateDigest := ""
		version := int64(0)
		if in.Candidate != nil {
			c := *in.Candidate
			if c.ID != m.CandidateID || c.WorkID != m.WorkID || c.SegmentID != m.SegmentID || c.ParentRequestID != r.Identity || c.SnapshotDigest != r.SnapshotDigest || c.Version != 1 || c.BaselineVersion != m.BaselineVersion || !promotionCandidateBaselineMatches(m, c) || c.BaselineStatus != m.BaselineStatus {
				return ErrManifest
			}
			c.Scope = in.Scope
			if err := saveCandidate(ctx, tx, c); err != nil {
				return err
			}
			if err := transferRuntimeBindingTx(ctx, tx, in.Scope.JobID, in.Key.RequestID, c.ID); err != nil {
				return err
			}
			state = "candidate"
			candidateDigest = memberPayloadDigest(ctx, c.Payload)
			version = c.Version
		}
		next := in.Next
		if in.Candidate != nil {
			next.State = "candidate"
		}
		if err := saveCursor(ctx, tx, in.Scope, m.SegmentID, next, make(map[int]attemptCounters)); err != nil {
			return fmt.Errorf("promote_member cursor_transition request=%s ordinal=%d next_main=%d next_alignment=%d next_network=%d: %w", in.Key.RequestID, in.Key.Ordinal, next.MainAttempts, next.AlignmentAttempts, next.NetworkAttempts, err)
		}
		if err := tx.WorkItem.UpdateOneID(w.ID).SetResponseRequestID("").Exec(ctx); err != nil {
			return err
		}
		var candidateBytes int64
		if in.Candidate != nil {
			candidateBytes = int64(len(in.Candidate.Payload))
		}
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "promotion_write", Bytes: candidateBytes, Rows: 3})
		if err := tx.WorkResponseMember.UpdateOneID(output.ID).SetState(state).SetFailure(failure).SetOutcomeKind(in.Outcome.Code()).SetOutcomeLimit(in.Outcome.Limit).SetOutcomeObserved(in.Outcome.Observed).SetCandidateDigest(candidateDigest).SetCandidateVersion(version).ClearPayload().SetPayloadBytes(0).ClearCorrectedPayload().SetCorrectedBytes(0).Exec(ctx); err != nil {
			return err
		}
		if err := tx.WorkRequestInputMember.UpdateOneID(input.ID).ClearPayload().SetPayloadBytes(0).Exec(ctx); err != nil {
			return err
		}
		retired := input.PayloadBytes + output.PayloadBytes + output.CorrectedBytes
		u := tx.WorkRequest.UpdateOneID(r.ID).SetPromotionCursor(in.Key.Ordinal + 1).AddRemainingMembers(-1).AddInputMemberBytes(-input.PayloadBytes).AddOutputMemberBytes(-output.PayloadBytes - output.CorrectedBytes).AddModelOutputBytes(-output.PayloadBytes).AddCorrectionBytes(-output.CorrectedBytes).AddMemberStorageBytes(-retired + int64(len(candidateDigest)+len(failure)))
		if in.Key.Ordinal+1 == r.PromotionCount {
			// Receipt is retained as audit data in member_storage_bytes.
			u.SetPromotionState("promoted").SetPublicKind(workrequest.PublicKindNone).ClearResponseGlossary().SetPublicBytes(0).SetBusinessBound(0)
		}
		if err := u.Exec(ctx); err != nil {
			return err
		}
		if in.Key.Ordinal+1 == r.PromotionCount {
			return clearRuntimeBindingTx(ctx, tx, r.JobID, r.Identity, true)
		}
		return nil
	})
	return s.finishUnknown(ctx, in.Scope.JobID, err, proof.exists)
}

type memberPromotionProof struct {
	Key                          MemberKey
	Identity                     ResponseIdentityProof
	ResponseDigest               string
	Outcome                      responsecontract.MemberOutcome
	CandidateID, CandidateDigest string
	CandidateVersion             int64
}

func (p memberPromotionProof) exists(ctx context.Context, tx *ent.Client) (bool, error) {
	r, e := memberPromotionStateQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(p.Key.RequestID))).Only(ctx)
	if e != nil {
		return false, e
	}
	if !p.headerMatches(r) {
		return false, ErrManifest
	}
	m, e := tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalEQ(p.Key.Ordinal)).Select(workrequestinputmember.FieldID, workrequestinputmember.FieldCandidateID).Only(ctx)
	if e != nil {
		return false, e
	}
	o, e := tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(p.Key.Ordinal)).Select(workresponsemember.FieldID, workresponsemember.FieldState, workresponsemember.FieldCandidateDigest, workresponsemember.FieldCandidateVersion, workresponsemember.FieldOutcomeKind, workresponsemember.FieldOutcomeLimit, workresponsemember.FieldOutcomeObserved).Only(ctx)
	if e != nil {
		return false, e
	}
	if o.State == "pending" {
		return false, nil
	}
	e = p.verify(ctx, tx, r, m, o)
	return e == nil, e
}
func (p memberPromotionProof) headerMatches(r *ent.WorkRequest) bool {
	return p.Identity.Matches(r) && r.Identity == p.Key.RequestID && r.RetryEpoch == p.Key.CaptureEpoch && r.ResponseDigest == p.ResponseDigest && p.Key.Ordinal >= 0 && p.Key.Ordinal < r.PromotionCount
}
func (p memberPromotionProof) verify(ctx context.Context, tx *ent.Client, r *ent.WorkRequest, m *ent.WorkRequestInputMember, o *ent.WorkResponseMember) error {
	if !p.headerMatches(r) {
		return ErrManifest
	}
	if o.OutcomeKind != p.Outcome.Code() || o.OutcomeLimit != p.Outcome.Limit || o.OutcomeObserved != p.Outcome.Observed {
		return ErrManifest
	}
	if r.PromotionCursor <= p.Key.Ordinal {
		return ErrCandidateVersion
	}
	if !p.Outcome.Accepted {
		if o.State != "failed" {
			return ErrManifest
		}
		return nil
	}
	if o.State != "candidate" || m.CandidateID != p.CandidateID || o.CandidateVersion != p.CandidateVersion || o.CandidateDigest != p.CandidateDigest {
		return ErrManifest
	}
	row, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(p.CandidateID)).Select(workcandidate.FieldID, workcandidate.FieldVersion, workcandidate.FieldState, workcandidate.FieldParentRequestID).Only(ctx)
	if err != nil {
		return err
	}
	if row.ParentRequestID != r.Identity || row.Version < p.CandidateVersion {
		return ErrManifest
	}
	return nil
}

func guardPromotedResponse(ctx context.Context, tx *ent.Client, c Candidate) (bool, error) {
	w, err := member(ctx, tx, c.Scope, c.SegmentID)
	if err != nil {
		return false, err
	}
	if w.ResponseRequestID == "" || w.ResponseRequestID != c.ParentRequestID {
		return false, nil
	}
	r, err := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(w.ResponseRequestID))).Only(ctx)
	if err != nil {
		return false, err
	}
	if r.ResponseFormatVersion != execution.ResponseFormatVersion || r.State != "completed" || r.SnapshotDigest != c.SnapshotDigest {
		return false, ErrManifest
	}
	m, err := inputMemberIdentityQuery(tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.CandidateIDEQ(c.ID), workrequestinputmember.SegmentIDEQ(c.SegmentID))).Only(ctx)
	if err != nil {
		return false, err
	}
	p := inputPromotion(m)
	if p.WorkID != c.WorkID || p.BaselineVersion != c.BaselineVersion || !promotionCandidateBaselineMatches(p, c) || p.BaselineStatus != c.BaselineStatus {
		return false, ErrManifest
	}
	return true, nil
}

func stringDigest(value *string) string {
	h := sha256.New()
	if value != nil {
		_, _ = io.WriteString(h, *value)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func samePromotionBaseline(m PromotionMember, value *string) bool {
	return m.BaselineTargetIsNull == (value == nil) && m.BaselineDigest == stringDigest(value)
}

var requestFactFields = func() []string {
	out := make([]string, 0, len(workrequest.Columns))
	for _, f := range workrequest.Columns {
		switch f {
		case workrequest.FieldCapturePayload, workrequest.FieldResponseGlossary:
			continue
		}
		out = append(out, f)
	}
	return out
}()

func projectedRequest(q *ent.WorkRequestQuery) *ent.WorkRequestQuery {
	return q.Where(func(sel *sql.Selector) { sel.Where(requestMetadataBounds(sel)) }).Select(requestFactFields...).WorkRequestQuery
}
