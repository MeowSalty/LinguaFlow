package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestExecutionPlanInlineTermExtractionAPI(t *testing.T) {
	s, client, actor := sharedHTTPServer(t)
	backend, err := client.Backend.Create().SetName("inline-extraction-backend").
		SetScope("user").SetOwnerUserID(actor.ID).SetBackendType(entbackend.BackendTypeOpenai).Save(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	router := s.newRouter()
	token := authTestToken(t, actor, time.Now().Add(time.Hour), []byte(authTestSecret))
	const path = "/api/v1/execution-plan-templates"
	request := func(t *testing.T, method, target, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := sharedHTTPRequest(router, method, target, token, body)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, target, w.Code, status, w.Body.String())
		}
		return w
	}
	roundJSON := func(inline string) string {
		if inline != "" {
			inline = `,"inline_term_extraction":` + inline
		}
		return fmt.Sprintf(`{"mode":"translate","backend_id":%d,"concurrency":1,"translate":{"prompt_template_id":-1,"batch_size":10,"fallback_shrink":1%s}}`, backend.ID, inline)
	}
	readPlan := func(t *testing.T, w *httptest.ResponseRecorder, want *execution.InlineTermExtractionConfig) ExecutionPlanTemplate {
		t.Helper()
		var plan ExecutionPlanTemplate
		if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
			t.Fatal(err)
		}
		if len(plan.Rounds) != 1 || plan.Rounds[0].Translate == nil {
			t.Fatalf("missing translate round: %s", w.Body.String())
		}
		var wantAPI *InlineTermExtractionConfig
		if want != nil {
			strategy := InlineTermExtractionConfigConflictStrategy(want.ConflictStrategy)
			wantAPI = &InlineTermExtractionConfig{
				Enabled:              &want.Enabled,
				MaxTermsPer1000Words: &want.MaxTermsPer1000Words,
				MinSourceLen:         &want.MinSourceLen,
				ConflictStrategy:     &strategy,
			}
		}
		if got := plan.Rounds[0].Translate.InlineTermExtraction; !reflect.DeepEqual(got, wantAPI) {
			t.Fatalf("inline extraction config changed: got %+v, want %+v (%s)", got, wantAPI, w.Body.String())
		}
		return plan
	}

	for _, tt := range []struct {
		name   string
		inline string
		want   *execution.InlineTermExtractionConfig
	}{
		{name: "omitted"},
		{
			name: "empty uses disabled defaults", inline: `{}`,
			want: &execution.InlineTermExtractionConfig{Enabled: false, MaxTermsPer1000Words: 3, MinSourceLen: 2, ConflictStrategy: "rewrite-local"},
		},
		{
			name: "enabled only", inline: `{"enabled":true}`,
			want: &execution.InlineTermExtractionConfig{Enabled: true, MaxTermsPer1000Words: 3, MinSourceLen: 2, ConflictStrategy: "rewrite-local"},
		},
		{
			name: "disabled preserves options", inline: `{"enabled":false,"max_terms_per_1000_words":0.123456789,"min_source_len":1,"conflict_strategy":"off"}`,
			want: &execution.InlineTermExtractionConfig{Enabled: false, MaxTermsPer1000Words: 0.123456789, MinSourceLen: 1, ConflictStrategy: "off"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"name":%q,"profile_id":-1,"rounds":[%s]}`, tt.name, roundJSON(tt.inline))
			created := readPlan(t, request(t, http.MethodPost, path, body, http.StatusCreated), tt.want)
			detail := fmt.Sprintf("%s/%d", path, created.Id)
			rounds, err := json.Marshal(created.Rounds)
			if err != nil {
				t.Fatal(err)
			}
			readPlan(t, request(t, http.MethodPut, detail, `{"rounds":`+string(rounds)+`}`, http.StatusOK), tt.want)
			readPlan(t, request(t, http.MethodGet, detail, "", http.StatusOK), tt.want)
			stored, err := client.ExecutionPlanTemplate.Get(t.Context(), created.Id)
			if err != nil {
				t.Fatal(err)
			}
			if got := stored.Rounds[0].Translate.InlineTermExtraction; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stored inline extraction config changed: got %+v, want %+v", got, tt.want)
			}
		})
	}

	validBody := `{"name":"validation target","profile_id":-1,"rounds":[` + roundJSON("") + `]}`
	valid := readPlan(t, request(t, http.MethodPost, path, validBody, http.StatusCreated), nil)
	detail := fmt.Sprintf("%s/%d", path, valid.Id)
	for _, tt := range []struct {
		name, inline, field string
	}{
		{"zero term limit", `{"enabled":true,"max_terms_per_1000_words":0}`, "max_terms_per_1000_words"},
		{"negative term limit", `{"enabled":true,"max_terms_per_1000_words":-1}`, "max_terms_per_1000_words"},
		{"zero source length", `{"enabled":true,"min_source_len":0}`, "min_source_len"},
		{"unknown strategy", `{"enabled":true,"conflict_strategy":"merge"}`, "conflict_strategy"},
		{"empty strategy", `{"enabled":true,"conflict_strategy":""}`, "conflict_strategy"},
		{"disabled invalid config", `{"enabled":false,"max_terms_per_1000_words":0}`, "max_terms_per_1000_words"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rounds := `[` + roundJSON(tt.inline) + `]`
			create := `{"name":"invalid","profile_id":-1,"rounds":` + rounds + `}`
			update := `{"rounds":` + rounds + `}`
			for _, w := range []*httptest.ResponseRecorder{
				request(t, http.MethodPost, path, create, http.StatusBadRequest),
				request(t, http.MethodPut, detail, update, http.StatusBadRequest),
			} {
				if !strings.Contains(w.Body.String(), "inline_term_extraction."+tt.field) {
					t.Fatalf("response does not identify invalid inline extraction field: %s", w.Body.String())
				}
			}
		})
	}
	readPlan(t, request(t, http.MethodGet, detail, "", http.StatusOK), nil)
}

func TestExecutionProfileRejectsGlossaryExtraction(t *testing.T) {
	s, _, actor := sharedHTTPServer(t)
	router := s.newRouter()
	token := authTestToken(t, actor, time.Now().Add(time.Hour), []byte(authTestSecret))
	w := sharedHTTPRequest(router, http.MethodPost, "/api/v1/execution-profiles", token,
		`{"name":"legacy extraction","config":{"glossary":{"bootstrap":{"enabled":true}}}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("legacy profile extraction should be rejected: %d %s", w.Code, w.Body.String())
	}
}
