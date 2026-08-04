package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// TestRunRound_SemanticQATerminalFailureCounts 验证终态失败累计到 FailedBatches/FailedSegments，且不进入 Unresolved。
func TestRunRound_SemanticQATerminalFailureCounts(t *testing.T) {
	doc := semanticQADoc([]string{"translated", "translated"}, nil)
	fb := &fakeBackend{
		name: "fake",
		errs: []error{
			&backend.StatusError{StatusCode: 401, Err: errors.New("unauthorized")},
			&backend.StatusError{StatusCode: 401, Err: errors.New("unauthorized")},
		},
	}
	h := &SemanticQAHandler{
		Backend:   fb,
		Renderer:  newSemanticQARenderer(t),
		BatchSize: 1, // 两批各 1 段，便于计 FailedBatches
		Retry:     backend.RetryPolicy{MaxAttempts: 3},
		Logger:    quietLogger(),
	}
	round := Round{
		Handler:     h,
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 3},
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved=%v want empty", result.Unresolved)
	}
	if result.FailedBatches != 2 {
		t.Fatalf("FailedBatches=%d want 2", result.FailedBatches)
	}
	// 两段均失败，顺序不保证并发，但本测试 Concurrency=1 且按 pending 顺序
	if len(result.FailedSegments) != 2 {
		t.Fatalf("FailedSegments=%v want 2 entries", result.FailedSegments)
	}
	got := append([]int{}, result.FailedSegments...)
	// 排序后比对
	if got[0] > got[1] {
		got[0], got[1] = got[1], got[0]
	}
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("FailedSegments=%v want [0 1]", result.FailedSegments)
	}
}
