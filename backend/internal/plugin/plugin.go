// Package plugin 定义脚本扩展（Lua）的 Hook 接口。
// MVP 仅提供 NopHost；真正的 Lua 实现待引入 gopher-lua 时通过 build tag 添加。
package plugin

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Hook string

const (
	HookBeforeTranslate Hook = "before_translate"
	HookAfterTranslate  Hook = "after_translate"
)

// ScriptHost 加载脚本并在 Hook 触发时分发事件。
type ScriptHost interface {
	Load(path string) error
	Emit(ctx context.Context, hook Hook, seg *pipeline.Segment) error
	Close() error
}
