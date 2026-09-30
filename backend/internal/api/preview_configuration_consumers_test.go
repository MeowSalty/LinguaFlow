package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// This fixture retains NewServer's real services, runners, credentials, HTTP
// clients and router. Only the external model provider is replaced.
func previewConfigurationFixture(t *testing.T, endpoint string, timeout, tokenTTL time.Duration) (*config.ServerConfig, func(string) *httptest.ResponseRecorder) {
	t.Helper()
	s, cfg, user := configurationConsumerServer(t, config.ModeLocal, func(cfg *config.ServerConfig) {
		cfg.Preview.MaxConcurrency = 1
		cfg.Preview.Timeout = timeout
		cfg.Preview.ApplyTokenTTL = tokenTTL
	})
	ctx := context.Background()
	secret := "preview-consumer-secret"
	provider, err := s.backendSvc.Create(ctx, service.CreateBackendInput{Scope: service.ScopeUser, OwnerUserID: &user.ID, BackendInput: service.BackendInput{
		Name: "preview-provider", Type: "openai", Secret: &secret, Options: map[string]any{"base_url": endpoint, "model": "test", "timeout": 10},
	}})
	if err != nil {
		t.Fatal(err)
	}
	profileConfig := schema.DefaultProfileConfig()
	profileConfig.Protect.Enabled, profileConfig.Ruby.Enabled, profileConfig.Repair.Enabled, profileConfig.QA.Enabled = false, false, false, false
	profile, err := s.executionProfileSvc.Create(ctx, user.ID, service.CreateExecutionProfileInput{Name: "preview-profile", Config: &profileConfig})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.executionPlanSvc.Create(ctx, user.ID, service.CreateExecutionPlanTemplateInput{Name: "preview-plan", ProfileID: profile.ID, Rounds: []schema.ExecutionRoundConfig{
		{Mode: "translate", BackendID: provider.ID, Translate: &schema.TranslateRoundConfig{PromptTemplateID: -1, BatchSize: 1, Concurrency: 1, FallbackShrink: 1}},
		{Mode: "revise", BackendID: provider.ID, Revise: &schema.ReviseRoundConfig{BatchSize: 1, Concurrency: 1, SegmentScope: "with_issues"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	project := s.entClient.Project.Create().SetName("preview-project").SetSourceLang("en").SetTargetLang("zh").SetOwnerUserID(user.ID).SaveX(ctx)
	resource := s.entClient.Resource.Create().SetProjectID(project.ID).SetPath("source.txt").SetFormat("txt").SetStoragePath("source.txt").SaveX(ctx)
	row := s.entClient.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("Hello").SetTargetText("错误译文").SetStatus(segment.StatusTranslated).
		SetQualityIssues([]qa.QualityIssue{{Code: qa.IssueCodeMistranslation, Message: "Restore the greeting meaning.", Disposition: qa.DispositionPending}}).SaveX(ctx)
	return cfg, func(kind string) *httptest.ResponseRecorder {
		path := fmt.Sprintf("/api/v1/projects/%d/resources/%d/segments/%d/%s-preview", project.ID, resource.ID, row.ID, kind)
		body := fmt.Sprintf(`{"execution_plan_id":%d}`, plan.ID)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(response, req)
		return response
	}
}

func TestConfigurationConsumersPreviewSharedConcurrencyAndDeadline(t *testing.T) {
	for _, activeKind := range []string{"translation", "revision"} {
		t.Run(activeKind, func(t *testing.T) {
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer upstream.Close()
			defer close(release)
			cfg, request := previewConfigurationFixture(t, upstream.URL, 800*time.Millisecond, 47*time.Second)
			finished := make(chan *httptest.ResponseRecorder, 1)
			started := time.Now()
			go func() { finished <- request(activeKind) }()
			select {
			case <-entered:
			case response := <-finished:
				t.Fatalf("preview did not reach provider: %d %s", response.Code, response.Body.String())
			case <-time.After(5 * time.Second):
				t.Fatal("preview did not reach provider")
			}
			otherKind := "revision"
			if activeKind == "revision" {
				otherKind = "translation"
			}
			busy := request(otherKind)
			if busy.Code != http.StatusTooManyRequests || busy.Header().Get("Retry-After") != "1" {
				t.Fatalf("preview services did not share configured capacity: %d %s", busy.Code, busy.Body.String())
			}
			select {
			case response := <-finished:
				if response.Code != http.StatusGatewayTimeout {
					t.Fatalf("configured preview deadline: %d %s", response.Code, response.Body.String())
				}
				if elapsed := time.Since(started); elapsed < cfg.Preview.Timeout || elapsed > 5*time.Second {
					t.Fatalf("preview deadline %s returned after %s", cfg.Preview.Timeout, elapsed)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("preview ignored its configured deadline")
			}
			select {
			case <-entered:
				t.Fatal("blocked preview still reached provider")
			default:
			}
		})
	}
}

func TestConfigurationConsumersPreviewApplyTokenTTL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer preview-consumer-secret" {
			t.Error("provider credential was not injected")
		}
		var request struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var raw string
		for _, message := range request.Messages {
			if message.Role == "user" {
				raw = message.Content
			}
		}
		var header struct {
			Task string `json:"task"`
		}
		if err := json.Unmarshal([]byte(raw), &header); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var reply any
		if header.Task == "revise_translation" {
			var input struct {
				Segments []struct{ ID string } `json:"segments"`
			}
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			revisions := []any{}
			for _, segment := range input.Segments {
				revisions = append(revisions, map[string]any{"id": segment.ID, "target": "你好"})
			}
			reply = map[string]any{"revisions": revisions}
		} else {
			var input struct {
				Segments map[string]struct{ Translate bool } `json:"segments"`
			}
			if err := json.Unmarshal([]byte(raw), &input); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			translations := map[string]string{}
			for id, segment := range input.Segments {
				if segment.Translate {
					translations[id] = "你好"
				}
			}
			reply = map[string]any{"translations": translations}
		}
		body, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(body)}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer upstream.Close()
	cfg, request := previewConfigurationFixture(t, upstream.URL, 5*time.Second, 47*time.Second)
	for _, kind := range []string{"translation", "revision"} {
		t.Run(kind, func(t *testing.T) {
			started := time.Now()
			response := request(kind)
			finished := time.Now()
			if response.Code != http.StatusOK {
				t.Fatalf("preview failed: %d %s", response.Code, response.Body.String())
			}
			var output struct {
				TargetText     string    `json:"target_text"`
				ApplyToken     string    `json:"apply_token"`
				ApplyExpiresAt time.Time `json:"apply_expires_at"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &output); err != nil {
				t.Fatal(err)
			}
			if output.TargetText != "你好" || output.ApplyToken == "" {
				t.Fatalf("successful preview did not produce an applicable target: %s", response.Body.String())
			}
			// JWT expiry has second precision; allow only that rounding difference.
			if output.ApplyExpiresAt.Before(started.Add(cfg.Preview.ApplyTokenTTL-time.Second)) || output.ApplyExpiresAt.After(finished.Add(cfg.Preview.ApplyTokenTTL+time.Second)) {
				t.Fatalf("configured apply TTL %s was ignored: %s", cfg.Preview.ApplyTokenTTL, output.ApplyExpiresAt)
			}
		})
	}
}
