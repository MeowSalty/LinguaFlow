package workstate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestResponseOwnershipRecoveryFromCanonicalFacts(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		name := "nonrewrite"
		if rewrite {
			name = "rewrite"
		}
		t.Run("sqlite/"+name, func(t *testing.T) {
			ctx, client, store, scope, first, _ := memberDeltaFixture(t)
			req, capture, candidates := integrityMemberPair(t, ctx, client, store, scope, first)
			req.BusinessBound = 4096
			budget, owned := memberDeltaBudget(t, ctx, client, scope)
			if err := store.ReserveRequest(owned, req); err != nil {
				t.Fatal(err)
			}
			admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
			if err != nil {
				t.Fatal(err)
			}
			defer admission.Close()
			if err = admission.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 8, Candidates: 1 << 20, Item: 4096, Responses: 1 << 20, Handoff: 16 << 20}); err != nil {
				t.Fatal(err)
			}
			if err = admission.AttachRootBudget(budget.Root, resourcebudget.Scope{ActorID: budget.Scope.ActorID, CallID: "canonical-recovery"}); err != nil {
				t.Fatal(err)
			}
			check := func(stage string, h ResponseHeader) {
				t.Helper()
				var input, model, correction int64
				for _, m := range client.WorkRequestInputMember.Query().AllX(ctx) {
					input += int64(len(m.Payload))
				}
				for _, m := range client.WorkResponseMember.Query().AllX(ctx) {
					model += int64(len(m.Payload))
					correction += int64(len(m.CorrectedPayload))
				}
				row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(req.ID)).OnlyX(ctx)
				scalar := func(query string) int64 {
					rows, e := client.QueryContext(ctx, query, row.ID)
					if e != nil {
						t.Fatal(e)
					}
					var n int64
					if e = (inventoryRow{rows: rows}).Scan(&n); e != nil {
						t.Fatal(e)
					}
					return n
				}
				metadata := scalar("SELECT COALESCE(LENGTH(CAST(members AS BLOB)),0)+COALESCE(LENGTH(CAST(debits AS BLOB)),0)+COALESCE(LENGTH(CAST(segment_ids AS BLOB)),0) FROM work_requests WHERE id=?")
				metadata += scalar("SELECT COALESCE(SUM(LENGTH(CAST(prompt_id AS BLOB))+LENGTH(CAST(work_id AS BLOB))+LENGTH(CAST(candidate_id AS BLOB))+LENGTH(CAST(baseline_digest AS BLOB))+LENGTH(CAST(baseline_status AS BLOB))+LENGTH(CAST(payload_digest AS BLOB))),0) FROM work_request_input_members WHERE request_id=?")
				metadata += scalar("SELECT COALESCE(SUM(LENGTH(CAST(payload_digest AS BLOB))+LENGTH(CAST(corrected_digest AS BLOB))+LENGTH(CAST(failure AS BLOB))+LENGTH(CAST(candidate_digest AS BLOB))),0) FROM work_response_members WHERE request_id=?")
				if h.Phase.Terminal() {
					metadata += int64(len(row.GlossaryReceipt))
				}
				public := int64(len(row.ResponseGlossary))
				if h.Phase == responsecontract.BusinessReady {
					public = int64(len(row.GlossaryReceipt))
				}
				if h.Phase.Terminal() {
					public = 0
				}
				if h.InputMemberBytes != input || h.ModelOutputBytes != model || h.CorrectionBytes != correction || h.OutputMemberBytes != model+correction || h.CaptureBytes != int64(len(row.CapturePayload)) || h.PublicBytes != public || h.MetadataBytes != metadata {
					t.Fatalf("%s canonical partitions disagree with physical bytes: %+v expected metadata=%d", stage, h, metadata)
				}
				if err := admission.RestoreResponseOwnership(req.ID, 0, scope.ResourceID, h.Facts(), h.Phase, true); err != nil {
					t.Fatal(stage, err)
				}
				expected := input + model + correction + public + h.CaptureBytes + backend.RequestMetadataBytes
				before := admission.PipelineSnapshot()
				if before.HandoffBytes-before.ProgressLane != expected {
					t.Fatalf("%s J/O ownership: got=%+v expected=%d", stage, before, expected)
				}
				if err := admission.RestoreResponseOwnership(req.ID, 0, scope.ResourceID, h.Facts(), h.Phase, false); err != nil {
					t.Fatal(err)
				}
				after := admission.PipelineSnapshot()
				if after.HandoffBytes != before.HandoffBytes || after.CandidateBytes != before.CandidateBytes {
					t.Fatal("same canonical facts charged twice")
				}
				memberDeltaAudit(t, ctx, budget)
			}
			h, err := store.CaptureResponse(owned, capture)
			if err != nil {
				t.Fatal(err)
			}
			check("capture", h)
			h, err = store.PublishOutputs(owned, integrityPublication(req, 0, 1, false))
			if err != nil {
				t.Fatal(err)
			}
			check("partial", h)
			publication := integrityPublication(req, 1, 2, true)
			publication.Glossary = []byte(`[{"Source":"SDK","Target":"规范"}]`)
			h, err = store.PublishOutputs(owned, publication)
			if err != nil {
				t.Fatal(err)
			}
			check("entries", h)
			if h.PublicKind != responsecontract.PublicEntries || h.ArchiveCursor != 2 {
				t.Fatal("entries phase facts lost")
			}
			if rewrite {
				for ordinal := range 2 {
					if err = store.StageCorrectedMember(owned, integrityOutput(req, ordinal, "corrected sibling")); err != nil {
						t.Fatal(err)
					}
				}
			}
			receipt, err := json.Marshal(glossary.ResponseReceipt{Version: glossary.ResponseReceiptVersion, RequestID: req.ID, InputDigest: req.InputDigest, OutcomeDigest: strings.Repeat("a", 64), Added: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err = store.PublishMemberBusiness(owned, req.ID, receipt); err != nil {
				t.Fatal(err)
			}
			h, err = store.ResponseHeader(ctx, req.ID)
			if err != nil {
				t.Fatal(err)
			}
			check("receipt", h)
			if h.PublicKind != responsecontract.PublicReceipt || h.PublicBytes == 0 || rewrite && h.CorrectionBytes == 0 || !rewrite && h.CorrectionBytes != 0 {
				t.Fatal("receipt/correction partitions lost")
			}
			for ordinal := range 2 {
				next, e := store.LoadCursor(ctx, scope, candidates[ordinal].SegmentID)
				if e != nil {
					t.Fatal(e)
				}
				h, err = store.PromoteMember(owned, fixturePromotion(MemberPromotion{Scope: scope, Key: requestMemberKey(req, ordinal), Candidate: &candidates[ordinal], Next: next}))
				if err != nil {
					t.Fatal(err)
				}
				check("promotion", h)
			}
			if h.PublicKind != responsecontract.PublicNone || h.BusinessBound != 0 || h.Phase != responsecontract.Promoted {
				t.Fatal("terminal public reservation retained")
			}
			admission.ReleaseDurableResponse(req.ID)
			if got := admission.PipelineSnapshot(); got.HandoffBytes != 0 || got.MainOwners[0] != 0 {
				t.Fatalf("released recovery still owned: %+v", got)
			}
			memberDeltaAudit(t, ctx, budget)
		})
	}
}
