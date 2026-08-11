package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// TestRunRound_SemanticQA401FatalUnresolvedCrossRound 验证 401 致命错误路由为 fatalUnresolved：
// 跳过剩余池（单池后即终止），但仍计入 finalUnresolved 供跨轮传播。
func TestRunRound_SemanticQA401FatalUnresolvedCrossRound(t *testing.T) {
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
		BatchSize: 1,                                   // 两批各 1 段
		Retry:     backend.RetryPolicy{MaxAttempts: 3}, // maxPools=4，但 401 跳池
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
	// 401 → fatalUnresolved → 并入 finalUnresolved（供跨轮传播换 backend）
	if len(result.Unresolved) != 2 {
		t.Fatalf("Unresolved=%v want 2 entries (cross-round propagation)", result.Unresolved)
	}
	got := append([]int{}, result.Unresolved...)
	sort.Ints(got)
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("Unresolved=%v want [0 1]", result.Unresolved)
	}
	// 401 不再计入 failedSegments 软警告
	if len(result.FailedSegments) != 0 {
		t.Fatalf("FailedSegments=%v want empty (401 now fatalUnresolved)", result.FailedSegments)
	}
}
