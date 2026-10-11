package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

func TestRubyCLIStagedHTTPConcurrencyAndCancellation(t *testing.T) {
	for _, cancelAlignment := range []bool{false, true} {
		name := "success"
		if cancelAlignment {
			name = "cancel_alignment"
		}
		t.Run(name, func(t *testing.T) {
			const source = "<ruby>source<rt>original</rt></ruby>"
			const target = "<ruby>alpha<rt>reading</rt></ruby>"
			var mu sync.Mutex
			var mainCalls, alignmentCalls, mainActive, alignmentActive, mainPeak, alignmentPeak, canceled int
			secondMain := make(chan struct{})
			twoAlignments := make(chan struct{})
			bothCanceled := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				var request struct {
					Segments map[string]prompt.SegmentDetail `json:"segments"`
					Missing  []struct {
						ID string `json:"id"`
					} `json:"missing"`
					Regions []struct {
						Text string `json:"text"`
					} `json:"translation_regions"`
					Digest string `json:"region_digest"`
				}
				for _, message := range payload.Messages {
					if message.Role == "user" {
						if err := json.Unmarshal([]byte(message.Content), &request); err != nil {
							http.Error(w, err.Error(), http.StatusBadRequest)
							return
						}
					}
				}
				alignment := len(request.Missing) > 0
				mu.Lock()
				if alignment {
					alignmentCalls++
					alignmentActive++
					alignmentPeak = max(alignmentPeak, alignmentActive)
					if alignmentCalls == 2 {
						close(twoAlignments)
					}
				} else {
					mainCalls++
					mainActive++
					mainPeak = max(mainPeak, mainActive)
					if mainCalls == 2 {
						close(secondMain)
					}
				}
				mu.Unlock()
				defer func() {
					mu.Lock()
					defer mu.Unlock()
					if alignment {
						alignmentActive--
					} else {
						mainActive--
					}
				}()
				var content any
				if alignment {
					if len(request.Missing) != 1 || len(request.Regions) != 1 || request.Regions[0].Text != "alpha" || request.Digest == "" {
						http.Error(w, "alignment did not receive one frozen candidate", http.StatusBadRequest)
						return
					}
					select {
					case <-r.Context().Done():
						mu.Lock()
						canceled++
						if canceled == 2 {
							close(bothCanceled)
						}
						mu.Unlock()
						return
					case <-release:
					}
					content = map[string]any{"ruby_output": []ruby.OutputEntry{{ID: request.Missing[0].ID, Base: "alpha", Text: "reading", Kind: "creative", Occurrence: 1}}}
				} else {
					translations := make(map[string]string)
					for id, segment := range request.Segments {
						if segment.Translate {
							translations[id] = "alpha"
						}
					}
					content = map[string]any{"translations": translations}
				}
				encoded, err := json.Marshal(content)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "cli-entry", "object": "chat.completion",
					"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": string(encoded)}, "finish_reason": "stop"}},
					"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 11, "total_tokens": 18},
				})
			}))
			t.Cleanup(upstream.Close)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			cfg := newTestCLIConfig()
			provider := cfg.Backends["test"]
			provider.Options = map[string]any{"base_url": upstream.URL, "model": "cli-entry", "response_format": "none", "stream": false}
			cfg.Backends["test"] = provider
			profile := execution.DefaultProfile()
			profile.Ruby.Enabled = true
			profile.Ruby.PreserveKinds = []string{"creative"}
			profile.QA.Enabled = false
			cfg.Execution.Profile = "ruby-entry"
			cfg.TranslationProfiles["ruby-entry"] = config.CLIConfigTranslationProfile{ProfileSpec: profile}
			cfg.Execution.Rounds[0].Translate.BatchSize = 2
			cfg.Execution.Rounds = append(cfg.Execution.Rounds, translateRoundCfg())
			cfg.Execution.RubyRetry = &config.CLIConfigRubyRetry{Enabled: true, Backend: "test", MaxAttempts: 1, Concurrency: new(2)}
			eng, resolved, err := buildEngineFromCLIConfig(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resolved.Close()
			defer eng.Close()
			rounds := eng.Rounds()
			if rounds[0].Runtime == nil || rounds[1].Runtime != rounds[0].Runtime || rounds[0].Store != nil || resolved.Spec.RubyRetry.Concurrency != 2 {
				t.Fatal("CLI did not construct one invocation runtime without a Job store")
			}
			if rounds[0].Handler.(*pipeline.TranslateHandler).RubyProtocolVersion != 2 {
				t.Fatal("CLI did not use the frozen v2 protocol")
			}
			assertRubyCLIBudgets(t, rounds[0].Runtime, resolved.Spec.Rounds[0].Backend.ID)
			directory := t.TempDir()
			input, output := filepath.Join(directory, "input.txt"), filepath.Join(directory, "output.txt")
			paragraphs := []string{source + " first", source + " second", source + " third", source + " fourth"}
			if err := os.WriteFile(input, []byte(strings.Join(paragraphs, "\n\n")), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(output, []byte("previous output"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- translateSingleFile(ctx, eng, FileJob{InputPath: input, OutputPath: output}, "en", "zh", nil)
			}()
			for _, barrier := range []<-chan struct{}{secondMain, twoAlignments} {
				select {
				case <-barrier:
				case err := <-done:
					t.Fatalf("CLI ended before concurrent main/alignment work: %v", err)
				case <-ctx.Done():
					t.Fatal("CLI did not dispatch the next main batch and two alignment requests")
				}
			}
			if cancelAlignment {
				cancel()
			} else {
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case err := <-done:
				if cancelAlignment && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel result: %v", err)
				}
				if !cancelAlignment && err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("CLI did not join its provider calls")
			}
			if cancelAlignment {
				select {
				case <-bothCanceled:
				case <-time.After(10 * time.Second):
					t.Fatal("CLI did not cancel both provider calls")
				}
			}
			actual, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if cancelAlignment {
				if string(actual) != "previous output" {
					t.Fatal("canceled CLI invocation replaced an existing output file")
				}
			} else if strings.Count(string(actual), target) != 4 {
				t.Fatalf("CLI did not export four finalized candidates: %s", actual)
			}
			mu.Lock()
			defer mu.Unlock()
			wantAlignmentCalls := 4
			if cancelAlignment {
				wantAlignmentCalls = 2
			}
			if mainCalls != 2 || alignmentCalls != wantAlignmentCalls || mainPeak != 1 || alignmentPeak != 2 || mainActive != 0 || alignmentActive != 0 {
				t.Fatal(fmt.Sprintf("calls main/alignment=%d/%d; peaks=%d/%d; active=%d/%d", mainCalls, alignmentCalls, mainPeak, alignmentPeak, mainActive, alignmentActive))
			}
		})
	}
}

func assertRubyCLIBudgets(t *testing.T, runtime *pipeline.ExecutionRuntime, backendID int) {
	t.Helper()
	var permits []*backend.RequestPermit
	defer func() {
		for _, permit := range permits {
			permit.Release()
		}
	}()
	for _, probe := range []struct {
		stage backend.RequestStage
		want  bool
	}{
		{backend.RequestStageMain, true}, {backend.RequestStageMain, false},
		{backend.RequestStageAlignment, true}, {backend.RequestStageAlignment, true}, {backend.RequestStageAlignment, false},
	} {
		permit, _, err := runtime.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: backendID, Stage: probe.stage}, runtime.Gate)
		if permit != nil {
			permits = append(permits, permit)
		}
		if err != nil || (permit != nil) != probe.want {
			t.Fatalf("CLI budget stage=%s admitted=%v err=%v", probe.stage, permit != nil, err)
		}
	}
}
