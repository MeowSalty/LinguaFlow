package workstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"fmt"

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
	FreezeBytes                                      int64
	ResponseBytes                                    int64
	CorrectedBytes                                   int64
	BusinessBound                                    int64
	MemberCount                                      int
	Cursor                                           int
	Phase                                            string
	Truncated                                        bool
}

type MemberPromotion struct {
	Scope     Scope
	Key       MemberKey
	Candidate *Candidate
	Next      Cursor
	Failure   string
}

func payloadDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func guardExecution(ctx context.Context, tx *ent.Client, jobID int) error {
	return CheckExecution(ctx, tx, jobID)
}

func responseHeader(row *ent.WorkRequest) ResponseHeader {
	return ResponseHeader{MemberContext: MemberContext{MemberPool: row.MemberPool, MemberAttempt: row.MemberAttempt, MemberMode: row.MemberMode, MemberFormat: row.MemberFormat, SourceLang: row.SourceLang, TargetLang: row.TargetLang, TextMode: row.TextMode}, FormatVersion: row.ResponseFormatVersion, ArchiveCursor: row.ArchiveCursor, ArchiveDigest: row.ArchiveDigest, InputMemberBytes: row.InputMemberBytes, OutputMemberBytes: row.OutputMemberBytes, PublicBytes: row.PublicBytes, MetadataBytes: row.RequestMetadataBytes + row.MemberStorageBytes - row.InputMemberBytes - row.OutputMemberBytes - row.CaptureBytes - row.PublicBytes, DatabaseID: row.ID, RequestID: row.Identity, Scope: Scope{JobID: row.JobID, ResourceID: row.ResourceID, RoundID: row.JobRoundID, RetryEpoch: row.RetryEpoch}, SnapshotDigest: row.SnapshotDigest, FreezeDigest: row.FreezeDigest, ResponseDigest: row.ResponseDigest, FreezeBytes: row.InputMemberBytes, ResponseBytes: row.CaptureBytes, CorrectedBytes: row.OutputMemberBytes, BusinessBound: row.BusinessBound, MemberCount: row.PromotionCount, Cursor: row.PromotionCursor, Phase: row.PromotionState, Truncated: row.ResponseTruncated}
}

// Headers deliberately include earlier retry epochs. Only reliable responses
// survive an epoch change; an uncertain invocation cannot enter this query.
func (s *Store) LoadResponseHeaders(ctx context.Context, jobID, after, limit int) ([]ResponseHeader, int, error) {
	if limit < 1 || limit > 256 {
		return nil, after, ErrManifest
	}
	rows, err := memberHeaderQuery(s.client.WorkRequest.Query().Where(workrequest.JobIDEQ(jobID), workrequest.IDGT(after), workrequest.StateEQ("completed"), workrequest.PromotionStateIn("capture_pending", "outputs_ready", "business_ready", "response_pending", "glossary_processed")).Order(ent.Asc(workrequest.FieldID)).Limit(limit)).All(ctx)
	if err != nil {
		return nil, after, err
	}
	out := make([]ResponseHeader, 0, len(rows))
	for _, r := range rows {
		if r.ResponseFormatVersion != execution.ResponseFormatVersion {
			return nil, after, ErrManifest
		}
		h := responseHeader(r)
		h.FreezeBytes = r.InputMemberBytes
		h.ResponseBytes = r.CaptureBytes
		h.CorrectedBytes = r.OutputMemberBytes
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
func (s *Store) PromoteMember(ctx context.Context, in MemberPromotion) error {
	if in.Key.Ordinal < 0 || in.Key.RequestID == "" {
		return ErrManifest
	}
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, in.Scope, false); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, in.Scope.JobID); err != nil {
			return err
		}
		r, err := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.Key.RequestID))).Only(ctx)
		if err != nil {
			return err
		}
		if r.ResponseFormatVersion != execution.ResponseFormatVersion || r.RetryEpoch != in.Key.CaptureEpoch || r.JobID != in.Scope.JobID || r.ResourceID != in.Scope.ResourceID || r.JobRoundID != in.Scope.RoundID || in.Key.Ordinal >= r.PromotionCount {
			return ErrManifest
		}
		input, err := tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalEQ(in.Key.Ordinal)).Select(workrequestinputmember.FieldID, workrequestinputmember.FieldSegmentID, workrequestinputmember.FieldWorkID, workrequestinputmember.FieldCandidateID, workrequestinputmember.FieldBaselineVersion, workrequestinputmember.FieldBaselineDigest, workrequestinputmember.FieldBaselineTargetIsNull, workrequestinputmember.FieldBaselineStatus, workrequestinputmember.FieldSourceGeneration, workrequestinputmember.FieldSourceRevisionID, workrequestinputmember.FieldPayloadBytes).Only(ctx)
		if err != nil {
			return err
		}
		output, err := tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(in.Key.Ordinal)).Select(workresponsemember.FieldID, workresponsemember.FieldState, workresponsemember.FieldPayloadBytes, workresponsemember.FieldCorrectedBytes, workresponsemember.FieldCandidateDigest, workresponsemember.FieldCandidateVersion).Only(ctx)
		if err != nil {
			return err
		}
		if output.State != "pending" {
			return verifyMemberOutcome(ctx, tx, r, input, output, in)
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
		if err := tx.WorkResponseMember.UpdateOneID(output.ID).SetState(state).SetFailure(in.Failure).SetCandidateDigest(candidateDigest).SetCandidateVersion(version).ClearPayload().SetPayloadBytes(0).ClearCorrectedPayload().SetCorrectedBytes(0).Exec(ctx); err != nil {
			return err
		}
		if err := tx.WorkRequestInputMember.UpdateOneID(input.ID).ClearPayload().SetPayloadBytes(0).Exec(ctx); err != nil {
			return err
		}
		retired := input.PayloadBytes + output.PayloadBytes + output.CorrectedBytes
		u := tx.WorkRequest.UpdateOneID(r.ID).SetPromotionCursor(in.Key.Ordinal + 1).AddRemainingMembers(-1).AddInputMemberBytes(-input.PayloadBytes).AddOutputMemberBytes(-output.PayloadBytes - output.CorrectedBytes).AddMemberStorageBytes(-retired + int64(len(candidateDigest)+len(in.Failure)))
		if in.Key.Ordinal+1 == r.PromotionCount {
			u.SetPromotionState("promoted").ClearResponseGlossary().SetPublicBytes(0).AddMemberStorageBytes(-r.PublicBytes).SetBusinessBound(0)
		}
		if err := u.Exec(ctx); err != nil {
			return err
		}
		if in.Key.Ordinal+1 == r.PromotionCount {
			return clearRuntimeBindingTx(ctx, tx, r.JobID, r.Identity, true)
		}
		return nil
	})
	return s.finishUnknown(ctx, in.Scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		r, e := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.Key.RequestID))).Only(ctx)
		if e != nil {
			return false, e
		}
		m, e := tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalEQ(in.Key.Ordinal)).Select(workrequestinputmember.FieldID, workrequestinputmember.FieldCandidateID).Only(ctx)
		if e != nil {
			return false, e
		}
		o, e := tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(in.Key.Ordinal)).Select(workresponsemember.FieldID, workresponsemember.FieldState, workresponsemember.FieldCandidateDigest, workresponsemember.FieldCandidateVersion).Only(ctx)
		if e != nil {
			return false, e
		}
		if o.State == "pending" {
			return false, nil
		}
		e = verifyMemberOutcome(ctx, tx, r, m, o, in)
		return e == nil, e
	})
}
func verifyMemberOutcome(ctx context.Context, tx *ent.Client, r *ent.WorkRequest, m *ent.WorkRequestInputMember, o *ent.WorkResponseMember, in MemberPromotion) error {
	if r.PromotionCursor <= in.Key.Ordinal {
		return ErrCandidateVersion
	}
	if in.Candidate == nil {
		if o.State != "failed" {
			return ErrManifest
		}
		return nil
	}
	c := in.Candidate
	if o.State != "candidate" || m.CandidateID != c.ID || o.CandidateVersion != c.Version || o.CandidateDigest != memberPayloadDigest(ctx, c.Payload) {
		return ErrManifest
	}
	row, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(c.ID)).Select(workcandidate.FieldID, workcandidate.FieldVersion, workcandidate.FieldState, workcandidate.FieldParentRequestID).Only(ctx)
	if err != nil {
		return err
	}
	if row.ParentRequestID != r.Identity || row.Version < c.Version {
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
