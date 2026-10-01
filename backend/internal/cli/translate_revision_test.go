package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestCLIRevisionRunsAndWritesAcceptedTargets(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed=%v", mixed), func(t *testing.T) {
			var revisionCalls, translationCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer revision-secret" {
					t.Error("missing revision credential")
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
					revisionCalls.Add(1)
					var input struct {
						Segments []struct {
							ID, Source, Target string
							Issues             []struct{ Code, Message string }
						} `json:"segments"`
					}
					if err := json.Unmarshal([]byte(raw), &input); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if len(input.Segments) != 1 {
						t.Errorf("revision segments=%d", len(input.Segments))
						w.WriteHeader(400)
						return
					}
					segment := input.Segments[0]
					if segment.Source != "hello world" || segment.Target != "错误译文" || len(segment.Issues) != 1 || segment.Issues[0].Code != "mistranslation" {
						t.Errorf("revision did not receive reviewed source, target and issue: %+v", segment)
					}
					reply = map[string]any{"revisions": []any{map[string]any{"id": segment.ID, "target": "你好世界"}}}
				} else {
					translationCalls.Add(1)
					var input struct {
						Segments map[string]struct {
							Source    string
							Translate bool
						} `json:"segments"`
					}
					if err := json.Unmarshal([]byte(raw), &input); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					translations := map[string]string{}
					for id, segment := range input.Segments {
						if segment.Translate {
							if segment.Source == "hello world" {
								t.Error("reviewed translation was overwritten by translate round")
							}
							translations[id] = "其他译文"
						}
					}
					reply = map[string]any{"translations": translations}
				}
				writeCLICompletion(w, reply)
			}))
			defer server.Close()
			cfgPath, inputPath, reviewPath, outputPath := revisionCommandFiles(t, server.URL, "hello world", mixed)
			root, _ := newRoot()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"translate", "--config", cfgPath, "-i", inputPath, "-o", outputPath, "--revision-input", reviewPath, "--progress", "none"})
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(output), "你好世界") || strings.Contains(string(output), "错误译文") {
				t.Fatalf("revision was not rendered: %q", output)
			}
			if revisionCalls.Load() != 1 {
				t.Fatalf("revision requests=%d", revisionCalls.Load())
			}
			if mixed {
				if translationCalls.Load() != 1 || !strings.Contains(string(output), "其他译文") {
					t.Fatalf("mixed pipeline lost translation: %q", output)
				}
			} else if translationCalls.Load() != 0 || !strings.Contains(string(output), "untouched source") {
				t.Fatalf("revision-only pipeline changed unreviewed content: %q", output)
			}
		})
	}
}

func TestCLIRevisionRejectsMissingEmptyStaleAndUnresolvedInput(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "stale", "unresolved"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeCLICompletion(w, map[string]any{"revisions": []any{}})
			}))
			defer server.Close()
			source := "hello world"
			if mode == "stale" {
				source = "stale source"
			}
			cfgPath, inputPath, reviewPath, outputPath := revisionCommandFiles(t, server.URL, source, false)
			if mode == "empty" {
				if err := os.WriteFile(reviewPath, []byte("schema_version: 1\nsegments: []\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"translate", "--config", cfgPath, "-i", inputPath, "-o", outputPath, "--progress", "none"}
			if mode != "missing" {
				args = append(args, "--revision-input", reviewPath)
			}
			root, _ := newRoot()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(args)
			if err := root.ExecuteContext(context.Background()); err == nil {
				t.Fatal("invalid revision run succeeded")
			}
			if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
				t.Fatalf("failed revision published output: %v", err)
			}
			if mode != "unresolved" && calls.Load() != 0 {
				t.Fatal("invalid revision input reached provider")
			}
			if mode == "unresolved" && calls.Load() != 1 {
				t.Fatalf("zero retry did not remain zero: calls=%d", calls.Load())
			}
		})
	}
}

func revisionCommandFiles(t *testing.T, endpoint, reviewedSource string, mixed bool) (string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath, inputPath, reviewPath, outputPath := filepath.Join(dir, "translation.yaml"), filepath.Join(dir, "input.txt"), filepath.Join(dir, "review.yaml"), filepath.Join(dir, "output.txt")
	translate := ""
	if mixed {
		translate = "    - mode: translate\n      backend: test\n      translate:\n        retry: {max_attempts: 0, backoff_ms: 0, jitter: false}\n"
	}
	configuration := fmt.Sprintf(`kind: translation
version: 2
source_lang: en
target_lang: zh
backends:
  test:
    type: openai
    secret: revision-secret
    options: {model: test-model, base_url: %s/v1}
translation_profiles:
  plain:
    schema_version: 1
    protect: {enabled: false}
    ruby: {enabled: false}
execution:
  profile: plain
  rounds:
%s    - mode: revise
      backend: test
      revise:
        retry: {max_attempts: 0, backoff_ms: 0, jitter: false}
log: {level: error, format: text}
`, endpoint, translate)
	review := fmt.Sprintf("schema_version: 1\nsegments:\n  - index: 0\n    source: %s\n    target: 错误译文\n    issues:\n      - code: mistranslation\n        message: Restore the greeting meaning.\n", reviewedSource)
	for path, data := range map[string]string{cfgPath: configuration, inputPath: "hello world\n\nuntouched source\n", reviewPath: review} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath, inputPath, reviewPath, outputPath
}

func writeCLICompletion(w http.ResponseWriter, reply any) {
	body, _ := json.Marshal(reply)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "test", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(body)}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
}

func TestRevisionInputRequiresOneFileAndRevisionPlan(t *testing.T) {
	cfg := newTestCLIConfig()
	opts := translateOptions{revisionInput: "review.yaml", changed: map[string]bool{"revision-input": true}}
	if _, err := resolveCLIRevisionInput(cfg, opts, 1); err == nil {
		t.Fatal("review input without revision plan accepted")
	}
	cfg.Execution.Rounds = []config.CLIConfigRound{reviseRoundCfg()}
	if _, err := resolveCLIRevisionInput(cfg, opts, 2); err == nil {
		t.Fatal("one review file silently applied to multiple inputs")
	}
}
