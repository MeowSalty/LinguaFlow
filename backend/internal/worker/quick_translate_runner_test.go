package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

const fakeBackendType = "quicktest"

// fakeBackend 是测试用后端，输出由 currentFake 动态控制。
type fakeBackend struct {
	name string
	text string
	err  error
}

func (f *fakeBackend) Name() string { return f.name }

func (f *fakeBackend) Translate(_ context.Context, _ backend.Request) (*backend.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &backend.Response{Text: f.text}, nil
}

func (f *fakeBackend) Close() error { return nil }

var registerFake sync.Once

// currentFake 被工厂捕获，测试可动态改写 text/err 控制本轮输出。
var currentFake = &fakeBackend{}

func ensureFakeBackend(t *testing.T) {
	t.Helper()
	registerFake.Do(func() {
		backend.Register(fakeBackendType, func(cfg backend.Config) (backend.Backend, error) {
			currentFake.name = cfg.Name
			return currentFake, nil
		})
	})
}

// translateSnapshot 构造一个最小化的单 translate 轮快照。
func translateSnapshot(t *testing.T) *service.JobExecutionSnapshot {
	t.Helper()
	return &service.JobExecutionSnapshot{
		ExecutionPlanID:   1,
		ExecutionPlanName: "test",
		SourceLang:        "en",
		TargetLang:        "zh",
		GlossaryEnabled:   false,
		Rounds: []service.JobRoundSnapshot{{
			Mode: "translate",
			Backend: service.BackendSnapshot{
				ID:      1,
				Name:    "fake",
				Type:    fakeBackendType,
				Options: map[string]any{},
			},
			Translate: &service.JobTranslateRoundSnapshot{
				Prompt:           service.PromptSnapshot{Content: templates.EmbeddedPromptTemplate()},
				Strategy:         service.StrategySnapshot{},
				BatchSize:        10,
				MaxWordsPerBatch: 500,
				Concurrency:      1,
				Retry: schema.RetryConfig{
					MaxAttempts: 0,
				},
			},
		}},
	}
}

func newQuickRunner(t *testing.T) *QuickTranslateRunner {
	t.Helper()
	return NewQuickTranslateRunner(slog.Default(), nil, nil)
}

func TestQuickTranslateRun_NoRounds_ReturnsError(t *testing.T) {
	ensureFakeBackend(t)
	r := newQuickRunner(t)
	_, err := r.Run(context.Background(), service.QuickTranslateRunnerInput{
		Snapshot: &service.JobExecutionSnapshot{},
	})
	if err == nil {
		t.Fatal("expected error for empty rounds, got nil")
	}
}

func TestQuickTranslateRun_NoTranslateRound_ReturnsError(t *testing.T) {
	ensureFakeBackend(t)
	r := newQuickRunner(t)
	_, err := r.Run(context.Background(), service.QuickTranslateRunnerInput{
		Snapshot: &service.JobExecutionSnapshot{
			Rounds: []service.JobRoundSnapshot{{Mode: "adjudicate"}},
		},
	})
	if err == nil {
		t.Fatal("expected error when no translate round present, got nil")
	}
}

func TestQuickTranslateRun_Success_SingleSegment(t *testing.T) {
	ensureFakeBackend(t)
	// 翻译 handler 将段重编号为 1-based ID（wantIDs[0]=="1"，而非 doc 的 "0"）。
	currentFake.text = `{"translations":{"1":"你好"}}`
	currentFake.err = nil

	r := newQuickRunner(t)
	result, err := r.Run(context.Background(), service.QuickTranslateRunnerInput{
		Snapshot:   translateSnapshot(t),
		SourceLang: "en",
		TargetLang: "zh",
		SourceText: "hello",
		Glossary:   glossary.Nop{},
		Format:     "txt",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != "success" {
		t.Errorf("status=%q want %q (rounds=%+v warnings=%v)", result.Status, "success", result.RoundSummary, result.Warnings)
	}
	if result.TargetText != "你好" {
		t.Errorf("target=%q want %q", result.TargetText, "你好")
	}
	if len(result.Metrics) < 1 {
		t.Errorf("metrics empty, want >=1")
	}
	if result.Collector == nil {
		t.Fatal("collector is nil")
	}
	if len(result.Collector.Events()) < 1 {
		t.Errorf("collector events empty, want >=1")
	}
}

func TestQuickTranslateRun_BackendError_ReturnsFailed(t *testing.T) {
	ensureFakeBackend(t)
	// 用 4xx（非可重试）错误，避免 5s 退避：handler 直接判终态/转入下一池，
	// 轮次以 unresolved 收尾，最终 targetText 为空 → derivePreviewStatus="failed"。
	currentFake.err = &backend.StatusError{StatusCode: 400, Err: errors.New("bad request")}
	currentFake.text = ""

	r := newQuickRunner(t)
	result, err := r.Run(context.Background(), service.QuickTranslateRunnerInput{
		Snapshot:   translateSnapshot(t),
		SourceLang: "en",
		TargetLang: "zh",
		SourceText: "hello",
		Glossary:   glossary.Nop{},
		Format:     "txt",
	})
	if err != nil {
		t.Fatalf("run returned error, want nil result with Status=failed: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.Status != "failed" {
		t.Errorf("status=%q want %q", result.Status, "failed")
	}
	if result.TargetText != "" {
		t.Errorf("target=%q want empty on failure", result.TargetText)
	}
	if len(result.Metrics) < 1 {
		t.Errorf("metrics empty, want >=1 (metered backend still recorded the call)")
	}
}
