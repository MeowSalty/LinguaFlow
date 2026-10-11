package pipeline

import (
	"context"
	"encoding/json"
	"html"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestRubyCandidateMalformedMappingsRetainMainTranslation(t *testing.T) {
	for _, output := range []string{
		`{"1":[{"base":"alpha","text":"a","kind":"creative"}]}`,
		`{"1":[{"id":"6","base":"alpha","text":17,"kind":"creative"}]}`,
		`{"1":[{"id":"6","base":"alpha","text":"a","kind":"creative"},{"id":"6","base":"alpha","text":false,"kind":"creative"}]}`,
		`{"1":[{"id":"other","id":"6","base":"alpha","text":"a","kind":"creative"}]}`,
		`{"1":"malformed-mapping"}`,
	} {
		body := `{"translations":{"1":"alpha beta"},"ruby_output":` + output + `}`
		translations, _, _, err := parseBatchResponse(body, []string{"1"})
		if err != nil || translations["1"] != "alpha beta" {
			t.Fatalf("strict parser lost valid main translation: %v %v", translations, err)
		}
		seg := newRubyTestSeg("source", "old", []ruby.Item{{ID: "6", SourceBase: "source"}})
		seg.Translate = true
		doc := &Document{Format: "text", SourceLang: "en", TargetLang: "zh", Segments: []Segment{*seg}}
		main := &fakeBackend{name: "main", responses: []string{body}}
		alignment := &fakeBackend{name: "alignment"}
		h := &TranslateHandler{Backend: main, Renderer: newTestRenderer(t), RubyEnabled: true, RubyProtocolVersion: 2, RubyRetryBackends: []backend.Backend{alignment}, RubyRetryAttempts: 2, Repair: defaultRepairOpts()}
		result := h.PrepareBatch(context.Background(), doc, []int{0}, 0, quietLogger())
		if result.err != nil || len(result.candidates) != 1 || len(result.unresolved) != 0 {
			t.Fatalf("main response requeued because of ruby mapping: %+v", result)
		}
		candidate := result.candidates[0]
		if candidate.Segment.Target != "alpha beta" || candidate.Ready || len(candidate.Alignment.Missing()) != 1 || len(main.requests) != 1 || len(alignment.requests) != 0 {
			t.Fatalf("incorrect handoff: %+v", candidate)
		}
		if doc.Segments[0].Target != "old" {
			t.Fatal("private main candidate mutated accepted document")
		}
	}
}

func TestRubyCandidateClassificationAndBaselineAreFrozen(t *testing.T) {
	seg := newRubyTestSeg("source", "old", []ruby.Item{{ID: "6"}, {ID: "9"}})
	seg.Status = "edited"
	seg.Meta["nested"] = map[string]any{"values": []string{"original"}}
	doc := &Document{Format: "text", Segments: []Segment{*seg}}
	h := &TranslateHandler{RubyEnabled: true, RubyProtocolVersion: 2, RubyPreserveKinds: []string{"creative"}}
	result := h.prepareTranslatedCandidates(doc, []int{0}, []string{"1"}, map[string]string{"1": "alpha beta"}, map[string][]ruby.OutputEntry{"1": {
		{ID: "6", Base: "alpha", Text: "a", Kind: "phonetic"},
		{ID: "9", Base: "beta", Text: "b", Kind: "creative"},
	}}, nil, quietLogger())
	if result.err != nil || len(result.candidates) != 1 {
		t.Fatalf("prepare=%+v", result)
	}
	c := result.candidates[0]
	if len(c.Alignment.Verified) != 1 || c.Alignment.Verified[0].ID != "9" || len(c.Alignment.Missing()) != 0 {
		t.Fatalf("main classification did not freeze preserve policy: %+v", c.Alignment)
	}
	c.Segment.Meta["nested"].(map[string]any)["values"].([]string)[0] = "candidate"
	if seg.Meta["nested"].(map[string]any)["values"].([]string)[0] != "original" || extractRubyItemsFromSeg(seg)[0].Kind != "" {
		t.Fatal("candidate metadata aliases source document")
	}
	data, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := DecodeCandidate(data)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.BaselineTarget != "old" || reloaded.BaselineStatus != "edited" || !reflect.DeepEqual(c.Alignment.Verified, reloaded.Alignment.Verified) || reloaded.Alignment.Digest != c.Alignment.Digest {
		t.Fatalf("recovery changed candidate facts: %+v", reloaded)
	}
}

func TestRubyCandidateIncompleteTranslateWarnsAndReviseRejects(t *testing.T) {
	for _, mode := range []string{RoundModeTranslate, RoundModeRevise} {
		seg := *newRubyTestSeg("source", "accepted", nil)
		seg.Issues = []qa.QualityIssue{{Code: "calque", Message: "existing"}}
		c := newCandidate(seg, 0, mode, "text")
		c.Segment.Target = "alpha beta"
		state, err := ruby.NewAlignmentState(c.Segment.Target, "text", []ruby.Item{{ID: "6"}, {ID: "9"}}, nil, ruby.ProtocolV2)
		if err != nil {
			t.Fatal(err)
		}
		state.ApplyOutput([]string{"6"}, []ruby.OutputEntry{{ID: "6", Base: "alpha", Text: "a", Kind: "creative", Occurrence: 1}}, true)
		c.Alignment = state
		out, accepted, err := FinalizeCandidate(c)
		if err != nil || accepted != (mode == RoundModeTranslate) {
			t.Fatalf("mode=%s accepted=%t error=%v", mode, accepted, err)
		}
		if accepted && (len(out.Issues) != 2 || out.Issues[1].Code != qa.CodeRubyRestoreIncomplete || !strings.Contains(out.TargetText, "<ruby>alpha")) {
			t.Fatalf("incomplete translate lost valid mapping or warning: %+v", out)
		}
		if seg.Target != "accepted" || len(seg.Issues) != 1 || c.Segment.Target != "alpha beta" {
			t.Fatal("finalization mutated baseline or frozen candidate")
		}
	}
}

func TestRubyCandidateLegacyInlineKeepsBodyWithoutManufacturingIdentity(t *testing.T) {
	for _, withMapping := range []bool{false, true} {
		seg := newRubyTestSeg("source", "old", []ruby.Item{{ID: "6", SourceBase: "source"}})
		doc := &Document{Format: "text", Segments: []Segment{*seg}}
		h := &TranslateHandler{RubyEnabled: true, RubyProtocolVersion: ruby.ProtocolLegacy}
		var entries []ruby.OutputEntry
		if withMapping {
			entries = []ruby.OutputEntry{{ID: "6", Base: "alpha", Text: "explicit", Kind: "creative"}}
		}
		result := h.prepareTranslatedCandidates(doc, []int{0}, []string{"1"}, map[string]string{"1": "a ⟦ruby:alpha/untrusted/creative⟧ body"}, map[string][]ruby.OutputEntry{"1": entries}, nil, quietLogger())
		if result.err != nil || len(result.candidates) != 1 || len(result.unresolved) != 0 {
			t.Fatalf("legacy main text was discarded: %+v", result)
		}
		c := result.candidates[0]
		if c.Alignment.Translation != "a alpha body" || (len(c.Alignment.Verified) == 1) != withMapping {
			t.Fatalf("legacy marker supplied unsupported alignment facts: %+v", c)
		}
		out, accepted, err := FinalizeCandidate(c)
		if err != nil || !accepted {
			t.Fatalf("legacy candidate=%+v %t %v", out, accepted, err)
		}
		if withMapping {
			if out.TargetText != "a <ruby>alpha<rt>explicit</rt></ruby> body" {
				t.Fatalf("marker overrode explicit mapping: %s", out.TargetText)
			}
		} else if len(out.Issues) != 1 || out.Issues[0].Code != qa.CodeRubyRestoreIncomplete {
			t.Fatalf("source-item gap was hidden: %+v", out)
		}
	}
}

type rubyRecoveryStore struct {
	*MemoryRoundStore
	payload []byte
}

func (s *rubyRecoveryStore) Candidates(_ context.Context, after, _ int) ([]*Candidate, int, error) {
	if after > 0 {
		return nil, after, nil
	}
	c, err := DecodeCandidate(s.payload)
	if err != nil {
		return nil, 0, err
	}
	return []*Candidate{c}, 1, nil
}

func TestStagedRubyRecoverySendsOnlyMissingIDs(t *testing.T) {
	doc := newTestDoc(1)
	doc.Format = "text"
	c := newCandidate(doc.Segments[0], 0, RoundModeTranslate, doc.Format)
	c.Segment.Target = "alpha beta"
	c.ParentRequestID = "saved-main"
	c.LogicalAttempt = 1
	state, err := ruby.NewAlignmentState(c.Segment.Target, "text", []ruby.Item{{ID: "6"}, {ID: "9"}}, nil, ruby.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	state.ApplyOutput([]string{"6"}, []ruby.OutputEntry{{ID: "6", Base: "alpha", Text: "a", Kind: "creative", Occurrence: 1}}, true)
	c.Alignment = state
	payload, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	store := &rubyRecoveryStore{MemoryRoundStore: NewMemoryRoundStore(nil), payload: payload}
	if _, err := store.Seal(context.Background(), []int{0}); err != nil {
		t.Fatal(err)
	}
	main := &fakeBackend{name: "main"}
	align := &fakeBackend{name: "alignment", responses: []string{`{"ruby_output":[{"id":"9","base":"beta","text":"b","kind":"creative","occurrence":1}]}`}}
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	runtime := NewExecutionRuntime(admission, DefaultCandidateLimits(), nil)
	h := &TranslateHandler{Backend: BindRequestBackend(main, 1, 0, runtime), Renderer: newTestRenderer(t), RubyEnabled: true, RubyProtocolVersion: 2,
		RubyRetryBackends: []backend.Backend{BindRequestBackend(align, 2, 0, runtime)}, RubyRetryAttempts: 3, RubyTemplates: testRubyTemplates()}
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h}, doc, store, runtime, quietLogger(), progress.Nop{})
	if err != nil || len(result.Unresolved) != 0 {
		t.Fatalf("resume=%+v error=%v", result, err)
	}
	if len(main.requests) != 0 || len(align.requests) != 1 {
		t.Fatalf("resume retried main or complete item: main=%d alignment=%d", len(main.requests), len(align.requests))
	}
	var request struct {
		Missing []ruby.Item `json:"missing"`
	}
	if err := json.Unmarshal([]byte(align.requests[0].User), &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Missing) != 1 || request.Missing[0].ID != "9" {
		t.Fatalf("resumed request did not conserve missing subset: %s", align.requests[0].User)
	}
	if doc.Segments[0].Target != "<ruby>alpha<rt>a</rt></ruby> <ruby>beta<rt>b</rt></ruby>" {
		t.Fatalf("saved valid alignment was lost: %s", doc.Segments[0].Target)
	}
}

func TestStagedRubyTextV2PreservesEscapedFields(t *testing.T) {
	base := " A|B \"quoted\"\\\n "
	annotation := " read | \"quoted\" \\ \n "
	translation := base + " / " + base
	c := newCandidate(Segment{ID: "s1", Source: "source"}, 0, RoundModeTranslate, "text")
	c.Segment.Target = translation
	state, err := ruby.NewAlignmentState(translation, "text", []ruby.Item{{ID: "6"}}, nil, ruby.ProtocolV2)
	if err != nil {
		t.Fatal(err)
	}
	c.Alignment = state
	baseJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	annotationJSON, err := json.Marshal(annotation)
	if err != nil {
		t.Fatal(err)
	}
	response := string(baseJSON) + " | " + string(annotationJSON) + ` | "creative" | "6" | 2`
	align := &fakeBackend{name: "alignment", responses: []string{response}}
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	runtime := NewExecutionRuntime(admission, DefaultCandidateLimits(), nil)
	payload, err := c.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Window.Restore(c.ID, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	h := &TranslateHandler{RubyRetryBackends: []backend.Backend{BindRequestBackend(align, 1, 0, runtime)}, RubyRetryAttempts: 1, RubyTemplates: testRubyTemplates(), ResponseMode: "text"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := processCandidate(ctx, Round{Handler: h}, c, NewMemoryRoundStore(nil), runtime, quietLogger(), progress.Nop{})
	if result.err != nil || result.outcome != CommitAccepted || result.completed == nil {
		t.Fatalf("text candidate=%+v", result)
	}
	expected := base + " / <ruby>" + base + "<rt>" + html.EscapeString(annotation) + "</rt></ruby>"
	if result.completed.TargetText != expected || c.Alignment.Translation != translation {
		t.Fatalf("text protocol changed whitespace or escapes: %q", result.completed.TargetText)
	}
	if len(align.requests) != 1 || align.requests[0].ResponseFormat != "none" || align.requests[0].JSONSchema != nil {
		t.Fatalf("text request sent a JSON response schema: %+v", align.requests)
	}
}
