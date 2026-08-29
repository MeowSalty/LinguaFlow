package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// 复现用户报告：adjudicate 轮部分批次出现后端错误后，轮次未推进到下一池而是直接结束。
// 本文件用真实 AdjudicateHandler + RunRound 验证三类错误路径的池推进行为。

func adjudicateReproDoc() *Document {
	return adjudicableDoc(
		[]string{"translated"},
		[][]qa.QualityIssue{
			{{Code: "source_residual", Severity: qa.SeverityWarning, Message: "residual"}},
		},
	)
}

func adjudicateReproRound(t *testing.T, fb *fakeBackend, rep *recordingPoolObserver, maxAttempts int) Round {
	t.Helper()
	h := &AdjudicateHandler{
		Backend:   fb,
		Renderer:  newAdjudicationRenderer(t),
		BatchSize: 10,
		Retry:     backend.RetryPolicy{MaxAttempts: maxAttempts},
		Logger:    quietLogger(),
		Reporter:  rep,
	}
	return Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: maxAttempts},
		Shrink:      0, // 模拟 engine_factory：不填 Shrink，RunRound 兜底 1.0
		Handler:     h,
	}
}

// 场景一：非致命不可重试错误（如 400）→ 应逐池推进直到末池。
func TestRunRound_Adjudicate_NonRetryableError_AdvancesAllPools(t *testing.T) {
	doc := adjudicateReproDoc()
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		// 每次调用都返回 400（不可重试、非致命）
		errs: []error{
			&backend.StatusError{StatusCode: 400, Err: errors.New("bad request")},
			&backend.StatusError{StatusCode: 400, Err: errors.New("bad request")},
			&backend.StatusError{StatusCode: 400, Err: errors.New("bad request")},
			&backend.StatusError{StatusCode: 400, Err: errors.New("bad request")},
			&backend.StatusError{StatusCode: 400, Err: errors.New("bad request")},
		},
	}
	// MaxAttempts=3 → maxPools=4（对应 SSE 显示"池 1/4"）
	round := adjudicateReproRound(t, fb, rep, 3)

	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	var starts, advances int
	for _, e := range rep.events {
		if e.Phase == "pool_start" {
			starts++
		}
		if e.Phase == "pool_advance" {
			advances++
		}
		if e.MaxPools != 4 {
			t.Fatalf("MaxPools=%d want 4", e.MaxPools)
		}
	}
	if starts != 4 {
		t.Fatalf("pool_start events=%d want 4 (每池各一次): %+v", starts, rep.events)
	}
	if advances != 3 {
		t.Fatalf("pool_advance events=%d want 3", advances)
	}
	if len(result.Unresolved) != 1 {
		t.Fatalf("Unresolved=%v want [0]", result.Unresolved)
	}
}

// 场景二：致命错误（401/403）→ 跳过剩余池，仅池 0 执行（设计行为）。
func TestRunRound_Adjudicate_FatalError_SinglePoolOnly(t *testing.T) {
	doc := adjudicateReproDoc()
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		errs: []error{
			&backend.StatusError{StatusCode: 403, Err: errors.New("forbidden")},
		},
	}
	round := adjudicateReproRound(t, fb, rep, 3)

	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	var starts, advances int
	for _, e := range rep.events {
		if e.Phase == "pool_start" {
			starts++
		}
		if e.Phase == "pool_advance" {
			advances++
		}
	}
	if starts != 1 || advances != 0 {
		t.Fatalf("fatal 403: pool_start=%d want 1, pool_advance=%d want 0 (跳过剩余池)", starts, advances)
	}
	if len(result.Unresolved) != 1 {
		t.Fatalf("Unresolved=%v want [0] (跨轮传播)", result.Unresolved)
	}
}

// 场景三：可重试错误（500）在池内退避重试，预算耗尽后应进入下一池。
// 注意：backoffDuration 对所有可重试错误强制最小 5s 退避，本测试约耗时 20s。
func TestRunRound_Adjudicate_RetryableError_ExhaustsBudgetThenAdvances(t *testing.T) {
	if testing.Short() {
		t.Skip("backoff sleeps ~20s")
	}
	doc := adjudicateReproDoc()
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		errs: []error{
			&backend.StatusError{StatusCode: 500, Err: errors.New("server error")},
			&backend.StatusError{StatusCode: 500, Err: errors.New("server error")},
			&backend.StatusError{StatusCode: 500, Err: errors.New("server error")},
			&backend.StatusError{StatusCode: 500, Err: errors.New("server error")},
		},
	}
	// MaxAttempts=1 → maxPools=2，transientBudget=min(2,3)=2（池内尝试 attempt 0、1）
	round := adjudicateReproRound(t, fb, rep, 1)

	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	var starts, advances int
	for _, e := range rep.events {
		if e.Phase == "pool_start" {
			starts++
		}
		if e.Phase == "pool_advance" {
			advances++
		}
	}
	if starts != 2 || advances != 1 {
		t.Fatalf("retryable 500: pool_start=%d want 2, pool_advance=%d want 1: %+v", starts, advances, rep.events)
	}
	if len(result.Unresolved) != 1 {
		t.Fatalf("Unresolved=%v want [0]", result.Unresolved)
	}
}
