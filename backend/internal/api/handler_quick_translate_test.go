package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// noopRunner 是一个永不真正执行的 QuickTranslateRunner 占位实现。
// 仅用于构造 service.QuickTranslateService（空 source 等廉价错误路径不会触达 runner）。
type noopRunner struct{}

func (noopRunner) Run(context.Context, service.QuickTranslateRunnerInput) (*service.QuickTranslateResult, error) {
	return nil, nil
}

// quickTranslateMinimalServer 构造仅含 handleQuickTranslate 所需字段的 Server。
func quickTranslateMinimalServer(svc *service.QuickTranslateService) *Server {
	return &Server{
		serverCfg:         &config.ServerConfig{ServiceName: "test"},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		quickTranslateSvc: svc,
	}
}

// TestToQuickTranslateResponse_MapsAllFields 验证 toQuickTranslateResponse 对
// service.QuickTranslateOutput 的字段映射（纯函数，无需 Server）。
func TestToQuickTranslateResponse_MapsAllFields(t *testing.T) {
	out := &service.QuickTranslateOutput{
		Status:     "success",
		SourceText: "hi",
		TargetText: "你好",
		SourceLang: "en",
		TargetLang: "zh",
		QualityIssues: []qa.QualityIssue{
			{SegmentIndex: 0, Code: "untranslated", Message: "m", Severity: qa.SeverityWarning},
		},
		RoundSummary: []service.PreviewRoundSummary{
			{Index: 0, Mode: "translate", Backend: "be", Status: "success", Duration: 150 * time.Millisecond},
		},
		BatchEvents: []progress.BatchEvent{
			{Stage: "translate", Status: "success", RoundIndex: 0, BackendName: "be", DurationMs: 100, InputTokens: 5, OutputTokens: 3},
		},
		Usage:    service.UsageSummary{APICalls: 2, InputTokens: 11, OutputTokens: 7},
		Warnings: []string{"w1"},
	}

	resp := toQuickTranslateResponse(out)

	if resp.Status != QuickTranslateResponseStatusSuccess {
		t.Errorf("Status = %q, want %q", resp.Status, QuickTranslateResponseStatusSuccess)
	}
	if resp.SourceText != "hi" {
		t.Errorf("SourceText = %q, want %q", resp.SourceText, "hi")
	}
	if resp.TargetText != "你好" {
		t.Errorf("TargetText = %q, want %q", resp.TargetText, "你好")
	}
	if resp.SourceLang == nil || *resp.SourceLang != "en" {
		t.Errorf("SourceLang = %v, want &en", resp.SourceLang)
	}
	if resp.TargetLang == nil || *resp.TargetLang != "zh" {
		t.Errorf("TargetLang = %v, want &zh", resp.TargetLang)
	}
	if resp.QualityIssues == nil || len(*resp.QualityIssues) != 1 {
		t.Fatalf("QualityIssues = %v, want 1 entry", resp.QualityIssues)
	}
	if got := (*resp.QualityIssues)[0]; got.Code != "untranslated" || got.SegmentIndex != 0 || got.Message != "m" {
		t.Errorf("QualityIssues[0] = %+v", got)
	}
	if resp.RoundSummary == nil || len(*resp.RoundSummary) != 1 {
		t.Fatalf("RoundSummary = %v, want 1 entry", resp.RoundSummary)
	}
	round := (*resp.RoundSummary)[0]
	if round.Index != 0 || round.Mode != "translate" || round.DurationMs != 150 {
		t.Errorf("RoundSummary[0] = %+v, want Index=0 Mode=translate DurationMs=150", round)
	}
	if round.Backend == nil || *round.Backend != "be" {
		t.Errorf("RoundSummary[0].Backend = %v, want &be", round.Backend)
	}
	if round.Status != QuickRoundSummaryStatusSuccess {
		t.Errorf("RoundSummary[0].Status = %q, want %q", round.Status, QuickRoundSummaryStatusSuccess)
	}
	if resp.Batches == nil || len(*resp.Batches) < 1 {
		t.Fatalf("Batches = %v, want at least 1 entry", resp.Batches)
	}
	if resp.Usage == nil {
		t.Fatal("Usage = nil, want non-nil")
	}
	if resp.Usage.ApiCalls != 2 || resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Errorf("Usage = %+v, want ApiCalls=2 InputTokens=11 OutputTokens=7", resp.Usage)
	}
	if resp.Warnings == nil || len(*resp.Warnings) != 1 || (*resp.Warnings)[0] != "w1" {
		t.Errorf("Warnings = %v, want [w1]", resp.Warnings)
	}
}

// TestHandleQuickTranslate_Unauthorized_401 验证未认证请求返回 401。
func TestHandleQuickTranslate_Unauthorized_401(t *testing.T) {
	s := quickTranslateMinimalServer(nil) // 不设置 quickTranslateSvc，认证先于服务调用。
	body := bytes.NewBufferString(`{"source_text":"hi","execution_plan_id":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quick-translate", body)
	w := httptest.NewRecorder()

	s.handleQuickTranslate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestHandleQuickTranslate_EmptySource_400 验证空 source_text 返回 400。
func TestHandleQuickTranslate_EmptySource_400(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := service.NewQuickTranslateService(logger, nil, nil, nil, nil, nil, nil, noopRunner{}, 2, 5*time.Minute)
	s := quickTranslateMinimalServer(svc)

	body := bytes.NewBufferString(`{"source_text":"   ","execution_plan_id":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/quick-translate", body)
	req = withAuthUser(req, &ent.User{ID: 1})
	w := httptest.NewRecorder()

	s.handleQuickTranslate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
