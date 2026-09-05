package api

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// TestToTranslationBatchDiagnostic_TruncationSignals 验证截断信号
// (Truncated/Repaired) 从 progress.BatchEvent 透传到 OpenAPI 诊断结构。
func TestToTranslationBatchDiagnostic_TruncationSignals(t *testing.T) {
	event := progress.BatchEvent{
		Stage:     "translate",
		Status:    "success",
		Truncated: true,
		Repaired:  []string{"json.close-braces", "json.truncation-salvage"},
	}
	diagnostic := toTranslationBatchDiagnostic(event)

	if diagnostic.Truncated == nil || !*diagnostic.Truncated {
		t.Errorf("Truncated = %v, want pointer to true", diagnostic.Truncated)
	}
	if diagnostic.Repaired == nil || len(*diagnostic.Repaired) != 2 ||
		(*diagnostic.Repaired)[0] != "json.close-braces" || (*diagnostic.Repaired)[1] != "json.truncation-salvage" {
		t.Errorf("Repaired = %v, want [json.close-braces json.truncation-salvage]", diagnostic.Repaired)
	}
}

// TestToTranslationBatchDiagnostic_TruncationSignalsZero 验证零值事件：
// Truncated 恒透传为 false 指针（与 ShrinkAttempted 同风格），Repaired 省略。
func TestToTranslationBatchDiagnostic_TruncationSignalsZero(t *testing.T) {
	diagnostic := toTranslationBatchDiagnostic(progress.BatchEvent{Stage: "translate", Status: "success"})

	if diagnostic.Truncated == nil || *diagnostic.Truncated {
		t.Errorf("Truncated = %v, want pointer to false", diagnostic.Truncated)
	}
	if diagnostic.Repaired != nil {
		t.Errorf("Repaired = %v, want nil when event has no repair chain", diagnostic.Repaired)
	}
	if diagnostic.ShrinkAttempted == nil || *diagnostic.ShrinkAttempted {
		t.Errorf("ShrinkAttempted = %v, want pointer to false", diagnostic.ShrinkAttempted)
	}
}
