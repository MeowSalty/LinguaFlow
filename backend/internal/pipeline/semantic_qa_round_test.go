package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
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

// TestRunRound_SemanticQARenderFailureLeavesUnresolvedEmpty 验证 render 失败（确定性配置/模板
// 问题）经 terminalFailure → failedSegments 累计，但 NOT 计入 Unresolved 切片。
// 这是 job_runner 警告门失效回归的根因：唯一保留的警告门读 len(result.Unresolved)，
// 而 render 失败只填 FailedSegments/FailedBatches，故必须同时检查 FailedSegmentCount。
func TestRunRound_SemanticQARenderFailureLeavesUnresolvedEmpty(t *testing.T) {
	doc := semanticQADoc([]string{"translated", "translated"}, nil)
	// 模板 Parse 通过、Execute 必失败（引用不存在的字段），精准触发 render 失败分支。
	badRenderer, err := prompt.NewSemanticQARenderer(`bad {{.NoExist.Foo}}`)
	if err != nil {
		t.Fatalf("build bad renderer: %v", err)
	}
	h := &SemanticQAHandler{
		Backend:   &fakeBackend{name: "fake"},
		Renderer:  badRenderer,
		BatchSize: 1,
		Retry:     backend.RetryPolicy{MaxAttempts: 0}, // 单池
		Logger:    quietLogger(),
	}
	round := Round{
		Handler:     h,
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// render 失败只填 FailedSegments，Unresolved 切片为空 —— 这正是警告门漏判的根因。
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved=%v want empty (render failure uses failedSegments, not unresolved)", result.Unresolved)
	}
	if len(result.FailedSegments) != 2 {
		t.Fatalf("FailedSegments=%v want 2 (both segments hit render failure)", result.FailedSegments)
	}
	if result.FailedBatches == 0 {
		t.Fatal("FailedBatches want >0 (render failure increments failed batches)")
	}
}
