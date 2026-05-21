package plugin

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// NopHost 不加载任何脚本，Emit 始终返回 nil。
type NopHost struct{}

func (NopHost) Load(string) error                                       { return nil }
func (NopHost) Emit(context.Context, Hook, *pipeline.Segment) error { return nil }
func (NopHost) Close() error                                            { return nil }
