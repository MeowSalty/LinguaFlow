// Package ollama 是原生 Ollama 后端的占位实现。
// MVP 不实现——建议直接用 openai 后端 + base_url=http://localhost:11434/v1。
package ollama

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

const TypeName = "ollama"

type Backend struct{}

func (*Backend) Name() string { return TypeName }

func (*Backend) Translate(context.Context, backend.Request) (*backend.Response, error) {
	return nil, backend.ErrNotImplemented
}

func (*Backend) Close() error { return nil }

func init() {
	backend.Register(TypeName, func(map[string]any) (backend.Backend, error) {
		return &Backend{}, nil
	})
}
