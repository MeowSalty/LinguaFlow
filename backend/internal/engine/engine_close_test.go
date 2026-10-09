package engine

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// closeTrackingBackend 记录 Close 是否被调用。
type closeTrackingBackend struct {
	name       string
	closed     bool
	closeCount int
}

func (b *closeTrackingBackend) Name() string { return b.name }

func (b *closeTrackingBackend) Translate(context.Context, backend.Request) (*backend.Response, error) {
	return nil, nil
}

func (b *closeTrackingBackend) Close() error {
	b.closed = true
	b.closeCount++
	return nil
}

func TestEngineCloseOwnsRubyRetryBackends(t *testing.T) {
	shared := &closeTrackingBackend{name: "shared"}
	ruby := &closeTrackingBackend{name: "ruby"}
	e := &Engine{
		rounds:            []pipeline.Round{{Handler: &pipeline.TranslateHandler{Backend: shared}}},
		rubyRetryBackends: []backend.Backend{ruby, shared, nil, ruby},
	}
	for range 2 {
		if err := e.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if shared.closeCount != 1 || ruby.closeCount != 1 {
		t.Fatalf("close counts: shared=%d ruby=%d", shared.closeCount, ruby.closeCount)
	}
}

// TestEngineCloseClosesReviseHandlerBackend 防止回归：Engine.Close 的类型分支
// 曾遗漏 ReviseHandler，导致 revise 轮后端不被关闭。
func TestEngineCloseClosesReviseHandlerBackend(t *testing.T) {
	b := &closeTrackingBackend{name: "revise-backend"}
	e := &Engine{
		rounds: []pipeline.Round{
			{Handler: &pipeline.ReviseHandler{Backend: b}},
		},
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !b.closed {
		t.Fatal("revise handler backend was not closed")
	}
}
