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
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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

// translateSemanticQASnapshot 构造 translate + semantic_qa 双轮最小快照。
// semantic_qa 轮 MaxAttempts=0 → transientBudget=1 → 单次失败即 unresolved。
func translateSemanticQASnapshot(t *testing.T) *service.JobExecutionSnapshot {
	t.Helper()
	return &service.JobExecutionSnapshot{
		ExecutionPlanID:   1,
		ExecutionPlanName: "test",
		SourceLang:        "en",
		TargetLang:        "zh",
		GlossaryEnabled:   false,
		Rounds: []service.JobRoundSnapshot{
			{
				Mode: "translate",
				Backend: service.BackendSnapshot{
					ID:      1,
					Name:    "fake",
					Type:    fakeBackendType,
					Options: map[string]any{},
				},
				Translate: &service.JobTranslateRoundSnapshot{
					Prompt:           service.PromptSnapshot{Content: templates.EmbeddedPromptTemplate()},
					BatchSize:        10,
					MaxWordsPerBatch: 500,
					Concurrency:      1,
					Retry: schema.RetryConfig{
						MaxAttempts: 0,
					},
				},
			},
			{
				Mode: "semantic_qa",
				Backend: service.BackendSnapshot{
					ID:      2,
					Name:    "fake",
					Type:    fakeBackendType,
					Options: map[string]any{},
				},
				SemanticQA: &service.JobSemanticQARoundSnapshot{
					BatchSize:        10,
					MaxWordsPerBatch: 500,
					Concurrency:      1,
					Retry: schema.RetryConfig{
						MaxAttempts: 0,
					},
				},
			},
		},
	}
}

// TestQuickTranslateRun_SemanticQAParseFailureReturnsPartial 验证 semantic_qa 轮解析失败
// （LLM 返回非 JSON 响应）在 MaxAttempts=0 下落 unresolved，最终整体状态为 "partial"
// 而非 "success"。这是 quick/preview 流可见性回归的修复验证：解析失败后路径只填
// result.Unresolved 切片（非 translate 特化的 UnresolvedCount/FailedBatchCount），
// 故 partial 判定门须检查 len(result.Unresolved)。
func TestQuickTranslateRun_SemanticQAParseFailureReturnsPartial(t *testing.T) {
	ensureFakeBackend(t)
	// translate 轮：有效的翻译 JSON 响应 → 成功，targetText 非空。
	// semantic_qa 轮：复用同一后端，返回的翻译 JSON 不是合法的 semantic_qa schema → 解析失败。
	currentFake.err = nil
	currentFake.text = `{"translations":{"1":"你好"}}`

	r := newQuickRunner(t)
	result, err := r.Run(context.Background(), service.QuickTranslateRunnerInput{
		Snapshot:   translateSemanticQASnapshot(t),
		SourceLang: "en",
		TargetLang: "zh",
		SourceText: "hello",
		Glossary:   glossary.Nop{},
		Format:     "txt",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	// translate 成功但 semantic_qa 解析失败 → unresolved → 整体应为 partial（非 success）。
	if result.Status != "partial" {
		t.Errorf("status=%q want %q (semantic_qa parse failure must surface as partial, "+
			"rounds=%+v warnings=%v)", result.Status, "partial", result.RoundSummary, result.Warnings)
	}
}

// TestBuildReviseBatchHandlerCommon_NoOpAndEmptyPreserveIssues 验证 no-op 修订
// （LLM 返回原译文或空译文）跳过写回与任何 issue 变更：即使 targetedCodes 命中，
// 既有 issue（含 targeted pending）也完整保留——未发生实际修订，无从谈起"已修复"。
func TestBuildReviseBatchHandlerCommon_NoOpAndEmptyPreserveIssues(t *testing.T) {
	makeDoc := func() *pipeline.Document {
		return &pipeline.Document{
			SourceLang: "en", TargetLang: "zh", Vars: map[string]any{},
			Segments: []pipeline.Segment{
				{ID: "0", Source: "hello", Target: "你好", Status: "translated", Translate: true,
					Issues: []qa.QualityIssue{{Code: qa.IssueCodeCalque, Message: "借译", Disposition: qa.DispositionPending}}},
			},
		}
	}

	// no-op 修订（LLM 返回原译文）：与 DB 版一致跳过写回，语义 issue 必须保留。
	doc := makeDoc()
	handler := buildReviseBatchHandlerCommon(doc, nil, 0, []string{qa.IssueCodeCalque})
	if err := handler(context.Background(), pipeline.BatchResult{Segments: []pipeline.TranslatedSegment{{Index: 0, TargetText: "你好"}}}); err != nil {
		t.Fatal(err)
	}
	if doc.Segments[0].Target != "你好" || len(doc.Segments[0].Issues) != 1 || !doc.Segments[0].Issues[0].IsPending() {
		t.Fatalf("no-op revise should preserve target and issues, got target=%q issues=%#v", doc.Segments[0].Target, doc.Segments[0].Issues)
	}

	// 空译文同样按 no-op 处理，不覆盖、不移除。
	doc = makeDoc()
	handler = buildReviseBatchHandlerCommon(doc, nil, 0, []string{qa.IssueCodeCalque})
	if err := handler(context.Background(), pipeline.BatchResult{Segments: []pipeline.TranslatedSegment{{Index: 0, TargetText: ""}}}); err != nil {
		t.Fatal(err)
	}
	if doc.Segments[0].Target != "你好" || len(doc.Segments[0].Issues) != 1 || !doc.Segments[0].Issues[0].IsPending() {
		t.Fatalf("empty revise should preserve target and issues, got target=%q issues=%#v", doc.Segments[0].Target, doc.Segments[0].Issues)
	}
}

// TestBuildReviseBatchHandlerCommon_QANilRemovesTargetedPending 验证 qaRan=false
// （qaEngine=nil）的声明式契约：译文确有变化时，targetedCodes 命中且 pending 的
// issue 视为本轮已修复而移除；targeted 但已 dismissed 的记录与范围外 pending 保留。
func TestBuildReviseBatchHandlerCommon_QANilRemovesTargetedPending(t *testing.T) {
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh", Vars: map[string]any{},
		Segments: []pipeline.Segment{
			{ID: "0", Source: "hello", Target: "你好", Status: "translated", Translate: true,
				Issues: []qa.QualityIssue{
					{Code: qa.IssueCodeCalque, Message: "借译", Disposition: qa.DispositionPending},    // targeted pending → 移除
					{Code: qa.IssueCodeCalque, Message: "借译旧", Disposition: qa.DispositionDismissed}, // targeted 但 dismissed → 保留
					{Code: qa.IssueCodeGrammar, Message: "语法", Disposition: qa.DispositionPending},   // 范围外 pending → 保留
				}},
		},
	}
	handler := buildReviseBatchHandlerCommon(doc, nil, 0, []string{qa.IssueCodeCalque})
	if err := handler(context.Background(), pipeline.BatchResult{Segments: []pipeline.TranslatedSegment{{Index: 0, SourceText: "hello", TargetText: "你好（改）"}}}); err != nil {
		t.Fatal(err)
	}
	if doc.Segments[0].Target != "你好（改）" {
		t.Fatalf("target=%q want updated", doc.Segments[0].Target)
	}
	issues := doc.Segments[0].Issues
	if len(issues) != 2 {
		t.Fatalf("issues=%#v want [calque dismissed, grammar pending]", issues)
	}
	if issues[0].Code != qa.IssueCodeCalque || !issues[0].Dismissed() {
		t.Fatalf("targeted dismissed issue must survive: %#v", issues[0])
	}
	if issues[1].Code != qa.IssueCodeGrammar || !issues[1].IsPending() {
		t.Fatalf("out-of-scope pending issue must survive: %#v", issues[1])
	}
}

// TestBuildReviseBatchHandlerCommon_QARunRecomputesDeterministic 验证 qaRan=true
// 的重算契约：fresh 确定性 issue 进入并按指纹继承旧裁决；范围外语义 pending 保留；
// targeted 语义 pending 移除。
func TestBuildReviseBatchHandlerCommon_QARunRecomputesDeterministic(t *testing.T) {
	decidedBy := 42
	doc := &pipeline.Document{
		SourceLang: "en", TargetLang: "zh", Vars: map[string]any{},
		Segments: []pipeline.Segment{
			{ID: "0", Source: "hello", Target: "你好", Status: "translated", Translate: true,
				Issues: []qa.QualityIssue{
					// 确定性 dismissed（无 Span，指纹 "untranslated:" 与 fresh 相同）→ 裁决被继承。
					{Code: qa.CheckUntranslated, Disposition: qa.DispositionDismissed, DecidedBy: &decidedBy, Note: "人工确认"},
					// 范围外语义 pending → 保留。
					{Code: qa.IssueCodeGrammar, Message: "语法", Span: &qa.Span{MatchedText: "语法"}, Disposition: qa.DispositionPending},
					// targeted 语义 pending → 移除。
					{Code: qa.IssueCodeCalque, Message: "借译", Span: &qa.Span{MatchedText: "借译"}, Disposition: qa.DispositionPending},
				}},
		},
	}
	// 修订后译文与原文相同 → 启用的 untranslated 检查器确定性地重新检出该问题。
	qaEngine := qa.NewEngine(qa.Config{Enabled: true, Checks: []string{qa.CheckUntranslated}}, nil)
	handler := buildReviseBatchHandlerCommon(doc, qaEngine, 0, []string{qa.IssueCodeCalque})
	if err := handler(context.Background(), pipeline.BatchResult{Segments: []pipeline.TranslatedSegment{{Index: 0, SourceText: "hello", TargetText: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if doc.Segments[0].Target != "hello" {
		t.Fatalf("target=%q want updated", doc.Segments[0].Target)
	}
	issues := doc.Segments[0].Issues
	if len(issues) != 2 {
		t.Fatalf("issues=%#v want [untranslated dismissed, grammar pending]", issues)
	}
	if issues[0].Code != qa.CheckUntranslated || !issues[0].Dismissed() || issues[0].DecidedBy == nil || *issues[0].DecidedBy != decidedBy || issues[0].Note != "人工确认" {
		t.Fatalf("fresh untranslated issue should inherit dismissed verdict: %#v", issues[0])
	}
	if issues[0].Message != "译文与原文相同" {
		t.Fatalf("fresh issue fields should win over old: %#v", issues[0])
	}
	if issues[1].Code != qa.IssueCodeGrammar || !issues[1].IsPending() {
		t.Fatalf("out-of-scope semantic pending must survive QA recompute: %#v", issues[1])
	}
	for _, iss := range issues {
		if iss.Code == qa.IssueCodeCalque {
			t.Fatalf("targeted semantic pending must be removed: %#v", issues)
		}
	}
}
