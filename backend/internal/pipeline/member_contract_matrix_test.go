package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

type memberMatrixStore struct {
	*MemoryRoundStore
	checkpoints []MainResponseHeader
	businessErr error
	archiveErr  error
}

func (s *memberMatrixStore) PublishOutputs(ctx context.Context, in OutputPublication) (MainResponseHeader, error) {
	if s.archiveErr != nil {
		return MainResponseHeader{}, s.archiveErr
	}
	h, err := s.MemoryRoundStore.PublishOutputs(ctx, in)
	if err == nil {
		s.checkpoints = append(s.checkpoints, h)
	}
	return h, err
}
func (s *memberMatrixStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) (MainResponseHeader, error) {
	if s.businessErr != nil {
		return MainResponseHeader{}, terminalRuntimeStoreError{s.businessErr}
	}
	return s.MemoryRoundStore.PublishBusiness(ctx, id, out, install)
}

func memberMatrixCapture(t *testing.T, r *ExecutionRuntime, s *MemoryRoundStore, id string, n int) MainResponseHeader {
	t.Helper()
	ctx := r.BuildContext(WithExecutionRuntime(t.Context(), r), "matrix-seed:"+id)
	h := memberFixtureReserve(t, ctx, s, id, 1<<20, memberFixtureInputs(n))
	translations := map[string]string{}
	for i := 0; i < n; i++ {
		translations[fmt.Sprint(i+1)] = "translation"
	}
	payload, allocation := memberFixturePayload(t, ctx, map[string]any{"translations": translations})
	var err error
	h, err = s.CaptureResponse(ctx, CapturedResponse{MainResponseHeader: h, Payload: payload, Allocation: allocation, Record: RequestRecord{UsageKnown: true, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Admission.RestoreResponseOwnership(id, 0, 0, h.Facts(), h.Phase, false); err != nil {
		t.Fatal(err)
	}
	if err = r.Window.RestoreRaw(id, n, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestResponseAdvanceDeferredPreservesPublishedHeader(t *testing.T) {
	for _, mode := range []string{"business_lane", "business_error", "archive_error"} {
		t.Run("memory/"+mode, func(t *testing.T) {
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			s := &memberMatrixStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
			defer s.Close()
			ctx := WithExecutionRuntime(t.Context(), r)
			h := memberMatrixCapture(t, r, s.MemoryRoundStore, "matrix", 17)
			handler := &TranslateHandler{Glossary: glossary.NewMemory(), InlineBootstrap: true, Repair: defaultRepairOpts()}
			fault := errors.New("system publication unavailable")
			if mode == "business_lane" {
				if err := r.Admission.RestoreResponseOwnership("other", 0, 0, responsecontract.ResponseFacts{PublicKind: responsecontract.PublicNone, BusinessReservation: 1024}, responsecontract.OutputsReady, false); err != nil {
					t.Fatal(err)
				}
				if ok, err := r.Admission.TryBusinessOwner("other"); err != nil || !ok {
					t.Fatalf("other owner %t/%v", ok, err)
				}
			} else if mode == "business_error" {
				s.businessErr = fault
			} else {
				s.archiveErr = fault
			}
			if mode == "archive_error" {
				r.Policy.Save.MaxAttempts = 1
			}
			out, err := AdvanceResponse(ctx, Round{Handler: handler}, handler, h, s, r, quietLogger())
			confirmed, e := s.ResponseHeader(t.Context(), h.ID)
			if e != nil || out.Header != confirmed {
				t.Fatalf("latest confirmed header lost: got=%+v stored=%+v err=%v/%v", out.Header, confirmed, err, e)
			}
			if mode == "archive_error" {
				if !errors.Is(err, fault) || confirmed.Phase != responsecontract.CapturePending || confirmed.ArchiveCursor != 0 {
					t.Fatalf("archive error became content: %+v %v", out, err)
				}
			} else {
				if len(s.checkpoints) != 2 || s.checkpoints[0].ArchiveCursor != 16 || s.checkpoints[1].ArchiveCursor != 17 || confirmed.Phase != responsecontract.OutputsReady || confirmed.CaptureBytes != 0 || confirmed.ModelOutputBytes <= 0 {
					t.Fatalf("checkpoint facts=%+v header=%+v err=%v", s.checkpoints, confirmed, err)
				}
				if mode == "business_lane" {
					if err != nil || out.Kind != ResponseDeferred || out.Wait.Reason != "business_lane" {
						t.Fatalf("deferred entered error: %+v %v", out, err)
					}
					changed := out.Wait.CapacityChanged
					r.Admission.ReleaseDurableResponse("other")
					select {
					case <-changed:
					default:
						t.Fatal("business owner release did not wake deferred response")
					}
					resumed, e := AdvanceResponse(ctx, Round{Handler: handler}, handler, out.Header, s, r, quietLogger())
					if e != nil || resumed.Header.Cursor != 1 || resumed.Header.Phase != responsecontract.BusinessReady || len(s.checkpoints) != 2 {
						t.Fatalf("resume failed: phase=%s cursor=%d kind=%d err=%v checkpoints=%d", resumed.Header.Phase, resumed.Header.Cursor, resumed.Kind, e, len(s.checkpoints))
					}
				} else if !errors.Is(err, fault) {
					t.Fatalf("system error became local/deferred: %v", err)
				}
			}
			if got := s.requests[h.ID].record; got.State != "completed" || got.Usage.PromptTokens != 17 || got.Usage.CompletionTokens != 3 {
				t.Fatalf("paid response changed: %+v", got)
			}
		})
	}
}

func TestMemberArchiveOversizeIsolation(t *testing.T) {
	for _, mode := range []string{"translate_json_first", "translate_text_last", "revise_json_first", "revise_text_last"} {
		t.Run("memory/"+mode, func(t *testing.T) {
			limits := DefaultCandidateLimits()
			limits.ItemBytes = 4096
			r := stagedTestRuntime(t, limits)
			doc := newTestDoc(17)
			defer r.CloseDocument(doc)
			text := strings.Contains(mode, "text")
			revise := strings.HasPrefix(mode, "revise")
			bad := 0
			if strings.HasSuffix(mode, "last") {
				bad = 16
			}
			target := map[string]string{}
			for i := 0; i < 17; i++ {
				target[fmt.Sprint(i+1)] = "legal"
			}
			target[fmt.Sprint(bad+1)] = strings.Repeat("x", 8192)
			var raw string
			if text {
				var out strings.Builder
				if revise {
					out.WriteString("[revisions]\n")
				}
				for i := 0; i < 17; i++ {
					if revise {
						fmt.Fprintf(&out, "%s | %s\n", doc.Segments[i].ID, target[fmt.Sprint(i+1)])
					} else {
						fmt.Fprintf(&out, "[%d] %s\n", i+1, target[fmt.Sprint(i+1)])
					}
				}
				raw = out.String()
			} else if revise {
				entries := []map[string]string{}
				for i := 0; i < 17; i++ {
					entries = append(entries, map[string]string{"id": doc.Segments[i].ID, "target": target[fmt.Sprint(i+1)]})
				}
				payload, _ := json.Marshal(map[string]any{"revisions": entries})
				raw = string(payload)
			} else {
				payload, _ := json.Marshal(map[string]any{"translations": target})
				raw = string(payload)
			}
			main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
				return &backend.Response{Text: raw, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
			}}
			var handler stagedHandler
			if revise {
				for i := range doc.Segments {
					doc.Segments[i].Target = "old"
					doc.Segments[i].TargetIsNull = false
					doc.Segments[i].Status = "translated"
					doc.Segments[i].Issues = []qa.QualityIssue{{Code: "calque", Severity: qa.SeverityWarning}}
				}
				h := &ReviseHandler{Backend: BindRequestBackend(main, 1, 0, r), Renderer: newReviseRenderer(t), IssueCodes: []string{"calque"}, BatchSize: 0, MaxWordsPerBatch: 4000, Logger: quietLogger()}
				if text {
					h.ResponseMode = "text"
				}
				handler = h
			} else {
				h := stagedTestHandler(t, r, main)
				h.BatchSize = 0
				h.MaxWordsPerBatch = 4000
				if text {
					h.ResponseMode = "text"
				}
				handler = h
			}
			s := &memberMatrixStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
			defer s.Close()
			result, err := RunStagedRound(t.Context(), Round{Concurrency: 1, Handler: handler}, doc, s, r, quietLogger(), progress.Nop{})
			if err != nil || len(result.Resolved) != 16 || main.calls.Load() != 1 {
				t.Fatalf("legal siblings did not finish: resolved=%d calls=%d err=%v", len(result.Resolved), main.calls.Load(), err)
			}
			for i := range doc.Segments {
				if i != bad && doc.Segments[i].Target != "legal" {
					t.Fatalf("legal sibling %d=%q", i, doc.Segments[i].Target)
				}
			}
			if len(s.checkpoints) != 2 || s.checkpoints[0].ArchiveCursor != 16 || s.checkpoints[1].ArchiveCursor != 17 {
				t.Fatalf("archive crossed16 incorrectly: %+v", s.checkpoints)
			}
			for id, request := range s.requests {
				rejected := request.memberOutcomes[bad].Outcome
				if rejected.Key != (MemberKey{RequestID: id, Ordinal: bad, CaptureEpoch: request.response.CaptureEpoch}) || rejected.Accepted || rejected.Reason != responsecontract.MemberTooLarge || rejected.Limit != 4096 || rejected.Observed <= 4096 {
					t.Fatalf("bad member outcome=%+v", rejected)
				}
				if request.response.Cursor != 17 || request.response.Phase != responsecontract.Promoted || request.response.OutputMemberBytes != 0 || request.response.PublicKind != responsecontract.PublicNone {
					t.Fatalf("terminal response=%+v", request.response)
				}
			}
			cur := s.state.Cursors[bad]
			if cur.Pool != 1 || cur.Attempt != 0 {
				t.Fatalf("rejected member cursor=%+v", cur)
			}
			if doc.InputTokens != 17 || doc.OutputTokens != 3 {
				t.Fatalf("usage counted twice: %d/%d", doc.InputTokens, doc.OutputTokens)
			}
		})
	}
}
