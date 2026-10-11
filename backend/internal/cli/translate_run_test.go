package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

func newTestCLIConfig() *config.CLIConfig {
	return &config.CLIConfig{
		Kind: "translation", Version: 2, SourceLang: "en", TargetLang: "zh",
		Log: config.LogConfig{Level: "info", Format: "text"},
		Backends: map[string]config.CLIConfigBackend{
			"test": {Type: "openai", Enabled: true, Secret: "sk-cli-test-secret", Options: map[string]any{"base_url": "https://example.invalid/v1", "model": "test-model"}},
		},
		PromptTemplates:          map[string]config.CLIConfigPromptTemplate{"default": {Content: templates.EmbeddedPromptTemplate()}},
		BootstrapPromptTemplates: map[string]config.CLIConfigBootstrapTemplate{"default": {Content: templates.EmbeddedBootstrapTemplate()}},
		TranslationProfiles:      map[string]config.CLIConfigTranslationProfile{},
		Execution:                config.CLIConfigExecution{Rounds: []config.CLIConfigRound{translateRoundCfg()}},
	}
}

func translateRoundCfg() config.CLIConfigRound {
	return config.CLIConfigRound{Mode: "translate", Backend: "test", Translate: &config.CLIConfigTranslateRound{Prompt: "default", BatchSize: 1, Concurrency: 1, FallbackShrink: 0.5}}
}

func reviseRoundCfg() config.CLIConfigRound {
	return config.CLIConfigRound{Mode: "revise", Backend: "test", Revise: &config.CLIConfigReviseRound{BatchSize: 10, Concurrency: 1, SegmentScope: "with_issues"}}
}

func TestResolveCLIExecutionFreezesValuesWithoutSecrets(t *testing.T) {
	cfg := newTestCLIConfig()
	profile := execution.DefaultProfile()
	cfg.Execution.Profile = "strict"
	cfg.TranslationProfiles["strict"] = config.CLIConfigTranslationProfile{ProfileSpec: profile}
	cfg.Execution.Rounds = append(cfg.Execution.Rounds, reviseRoundCfg())
	cfg.Execution.RubyRetry = &config.CLIConfigRubyRetry{Enabled: true, Backend: "test", MaxAttempts: 1}
	resolved, err := resolveCLIExecution(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer resolved.Close()
	if resolved.Spec.SchemaVersion != 3 || resolved.Spec.DefaultsVersion != 3 || resolved.Spec.RubyProtocolVersion != 2 || resolved.Spec.RubyValidatorVersion != 1 || resolved.Spec.RubyBatchProtocolVersion != 1 || resolved.Spec.RubyRetry.Concurrency != 1 {
		t.Fatal("CLI did not freeze the new version and independent default concurrency")
	}
	if resolved.Spec.RubyTemplates.JSON != prompt.RubyAlignmentJSONTemplate || resolved.Spec.RubyTemplates.Text != prompt.RubyAlignmentTextTemplate || resolved.Spec.RubyTemplates.BatchJSON != prompt.RubyAlignmentBatchJSONTemplate || resolved.Spec.RubyTemplates.BatchText != prompt.RubyAlignmentBatchTextTemplate {
		t.Fatal("CLI alignment templates differ from the frozen protocol version")
	}
	if got := execution.EffectiveRubyRetryBatch(resolved.Spec); got != (execution.RubyRetryBatchConfig{BatchSize: 1, BatchWaitMS: 25}) {
		t.Fatalf("CLI batching defaults not frozen: %+v", got)
	}
	before, err := json.Marshal(resolved.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), cfg.Backends["test"].Secret) || strings.Contains(string(before), "api_key") {
		t.Fatal("execution snapshot contains plaintext credential material")
	}
	b := resolved.Spec.Rounds[0].Backend
	if resolved.Spec.RubyRetry.Backend.Credential != b.Credential || resolved.Spec.Rounds[1].Backend.ID != b.ID {
		t.Fatal("one CLI backend must share credential and capacity identities across rounds")
	}
	secret, err := resolved.Secrets.Resolve(context.Background(), b.Credential, b.Type, b.Options["base_url"].(string))
	if err != nil || secret != cfg.Backends["test"].Secret {
		t.Fatalf("credential resolution failed: %v", err)
	}
	first, err := resolved.Limiters.Lookup(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolved.Limiters.Lookup(resolved.Spec.RubyRetry.Backend.ID)
	if err != nil || first != second {
		t.Fatal("rounds did not share their rate limiter")
	}
	if resolved.Spec.Rounds[1].Revise.TemplateContent != templates.EmbeddedReviseTemplate() {
		t.Fatal("revision template was not frozen")
	}
	if resolved.Spec.RubyTemplates.JSON == "" || resolved.Spec.RubyTemplates.Text == "" {
		t.Fatal("ruby templates were not frozen")
	}
	cfg.Backends["test"].Options["model"] = "changed"
	cfg.TranslationProfiles["strict"].Protect.Rules[0] = "changed"
	cfg.PromptTemplates["default"] = config.CLIConfigPromptTemplate{Content: "changed"}
	cfg.Execution.Rounds[0].Translate.BatchSize = 999
	cfg.Execution.RubyRetry.Concurrency = new(9)
	after, err := json.Marshal(resolved.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("resolved execution changed after mutating its input")
	}
	resolved.Close()
	if _, err := resolved.Secrets.Resolve(context.Background(), b.Credential, b.Type, b.Options["base_url"].(string)); err == nil {
		t.Fatal("closed CLI registry still exposes its credential")
	}
}

func TestBuildEngineUsesExplicitProfileValues(t *testing.T) {
	for _, contextEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "zero_window"}[contextEnabled], func(t *testing.T) {
			cfg := newTestCLIConfig()
			profile := execution.DefaultProfile()
			profile.Context = execution.ProfileContextConfig{Enabled: contextEnabled}
			profile.Ruby.PreserveKinds = []string{}
			profile.Protect.Enabled = false
			cfg.Execution.Profile = "explicit"
			cfg.TranslationProfiles["explicit"] = config.CLIConfigTranslationProfile{ProfileSpec: profile}
			cfg.Execution.Rounds = append(cfg.Execution.Rounds, reviseRoundCfg())
			eng, resolved, err := buildEngineFromCLIConfig(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resolved.Close()
			defer eng.Close()
			tr := eng.Rounds()[0].Handler.(*pipeline.TranslateHandler)
			rv := eng.Rounds()[1].Handler.(*pipeline.ReviseHandler)
			if tr.Context.Enabled != contextEnabled || tr.Context.Before != 0 || tr.Context.After != 0 || tr.Context.MaxChars != 0 {
				t.Fatalf("explicit context was replaced: %+v", tr.Context)
			}
			if len(tr.RubyPreserveKinds) != 0 || len(rv.RubyPreserveKinds) != 0 {
				t.Fatal("explicit empty ruby kinds were replaced")
			}
			if !tr.RubyEnabled || !rv.RubyEnabled {
				t.Fatal("enabled ruby profile was lost")
			}
			for _, protector := range []protect.Protector{tr.Protector, rv.Protector} {
				original := "Keep `code` and <tag>text</tag>"
				actual, _, err := protect.ProtectText(protector, original)
				if err != nil || actual != original {
					t.Fatal("disabled protection changed source text")
				}
			}
			if tr.Retry.MaxAttempts != 0 || rv.Retry.MaxAttempts != 0 {
				t.Fatal("zero retry was replaced with defaults")
			}
			if !reflect.DeepEqual(rv.IssueCodes, qa.SemanticQACodes()) {
				t.Fatal("revision scope was not resolved")
			}
		})
	}
}

func TestResolveCLIExecutionRejectsInvalidBindings(t *testing.T) {
	cases := map[string]func(*config.CLIConfig){
		"unknown backend":  func(c *config.CLIConfig) { c.Execution.Rounds[0].Backend = "missing" },
		"disabled backend": func(c *config.CLIConfig) { b := c.Backends["test"]; b.Enabled = false; c.Backends["test"] = b },
		"missing secret":   func(c *config.CLIConfig) { b := c.Backends["test"]; b.Secret = ""; c.Backends["test"] = b },
		"unknown profile":  func(c *config.CLIConfig) { c.Execution.Profile = "missing" },
		"unknown prompt":   func(c *config.CLIConfig) { c.Execution.Rounds[0].Translate.Prompt = "missing" },
		"invalid issue code": func(c *config.CLIConfig) {
			r := reviseRoundCfg()
			r.Revise.SegmentScope = "with_issue_codes"
			r.Revise.IssueCodes = []string{"not-a-code"}
			c.Execution.Rounds = append(c.Execution.Rounds, r)
		},
		"unknown retry backend": func(c *config.CLIConfig) {
			c.Execution.RubyRetry = &config.CLIConfigRubyRetry{Enabled: true, Backend: "missing", MaxAttempts: 1}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := newTestCLIConfig()
			mutate(cfg)
			if resolved, err := resolveCLIExecution(cfg); err == nil {
				resolved.Close()
				t.Fatal("invalid execution accepted")
			}
		})
	}
}

func TestApplyTranslateFlagsChangeExecutableSemantics(t *testing.T) {
	for _, mode := range []string{"off", "pre", "inline"} {
		t.Run(mode, func(t *testing.T) {
			cfg := newTestCLIConfig()
			cfg.TranslationProfiles["alternate"] = config.CLIConfigTranslationProfile{ProfileSpec: execution.DefaultProfile()}
			cfg.PromptTemplates["alternate"] = config.CLIConfigPromptTemplate{Content: "custom prompt"}
			err := applyTranslateFlags(cfg, translateOptions{from: "ja", to: "en", profile: "alternate", prompt: "alternate", bootstrapMode: mode, changed: map[string]bool{"from": true, "to": true, "profile": true, "prompt": true, "bootstrap": true}})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := resolveCLIExecution(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer resolved.Close()
			if resolved.Spec.SourceLang != "ja" || resolved.Spec.TargetLang != "en" {
				t.Fatal("language flags were not resolved")
			}
			if resolved.Spec.Strategy.ProfileName != "alternate" {
				t.Fatal("profile flag was not resolved")
			}
			if resolved.Spec.GlossaryEnabled != (mode != "off") {
				t.Fatal("bootstrap flag did not explicitly enable the glossary when extracting terms")
			}
			if mode == "pre" && (len(resolved.Spec.Rounds) != 2 || resolved.Spec.Rounds[0].Mode != "extract" || resolved.Spec.Rounds[0].Extract.TemplateContent == "") {
				t.Fatal("pre bootstrap did not create an executable extraction round")
			}
			for _, r := range resolved.Spec.Rounds {
				if r.Translate != nil && r.Translate.Prompt.Content != "custom prompt" {
					t.Fatal("prompt override was not frozen")
				}
				if r.Translate != nil && (r.Translate.InlineTermExtraction == nil || r.Translate.InlineTermExtraction.Enabled != (mode == "inline")) {
					t.Fatal("inline extraction flag was not resolved on the translation round")
				}
			}
		})
	}
	for _, opts := range []translateOptions{{profile: "missing", changed: map[string]bool{"profile": true}}, {prompt: "missing", changed: map[string]bool{"prompt": true}}, {bootstrapMode: "missing", changed: map[string]bool{"bootstrap": true}}, {changed: map[string]bool{"from": true}}} {
		if err := applyTranslateFlags(newTestCLIConfig(), opts); err == nil {
			t.Fatal("invalid explicit flag accepted")
		}
	}
}

func TestTranslateSingleFileExtractBeforeTranslate(t *testing.T) {
	var extractCalls, translateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-cli-test-secret" {
			t.Error("request did not use the bound credential")
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var input struct {
			Task       string `json:"task"`
			SourceLang string `json:"source_lang"`
			TargetLang string `json:"target_lang"`
			Segments   map[string]struct {
				Translate bool `json:"translate"`
			} `json:"segments"`
		}
		for _, m := range request.Messages {
			if m.Role == "user" {
				if err := json.Unmarshal([]byte(m.Content), &input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
			}
		}
		if input.SourceLang != "en" || input.TargetLang != "zh" {
			t.Errorf("unexpected resolved languages: %s/%s", input.SourceLang, input.TargetLang)
		}
		var reply any
		if input.Task == "extract_terms" {
			extractCalls.Add(1)
			reply = map[string]any{"glossary": []any{}}
		} else {
			translateCalls.Add(1)
			translations := map[string]string{}
			for id, seg := range input.Segments {
				if seg.Translate {
					translations[id] = "你好"
				}
			}
			reply = map[string]any{"translations": translations}
		}
		body, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(body)}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
	}))
	defer server.Close()
	cfg := newTestCLIConfig()
	cfg.Backends["test"].Options["base_url"] = server.URL + "/v1"
	cfg.Glossary.Path = filepath.Join(t.TempDir(), "glossary.csv")
	profile := execution.DefaultProfile()
	profile.Protect.Enabled, profile.Ruby.Enabled = false, false
	cfg.Execution.Profile = "plain"
	cfg.TranslationProfiles["plain"] = config.CLIConfigTranslationProfile{ProfileSpec: profile}
	if err := applyTranslateFlags(cfg, translateOptions{bootstrapMode: "pre", changed: map[string]bool{"bootstrap": true}}); err != nil {
		t.Fatal(err)
	}
	cfg.Execution.Rounds[0].Extract.Retry = config.RetryConfig{}
	eng, resolved, err := buildEngineFromCLIConfig(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resolved.Close()
	defer eng.Close()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "input.txt"), filepath.Join(dir, "output.txt")
	if err := os.WriteFile(src, []byte("hello world\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := translateSingleFile(context.Background(), eng, FileJob{InputPath: src, OutputPath: dst}, resolved.Spec.SourceLang, resolved.Spec.TargetLang, nil); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "你好") {
		t.Fatalf("translation did not reach output: %q", output)
	}
	if extractCalls.Load() != 1 || translateCalls.Load() != 1 {
		t.Fatalf("requests: extraction=%d translation=%d", extractCalls.Load(), translateCalls.Load())
	}
}
