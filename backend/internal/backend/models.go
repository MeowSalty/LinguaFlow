package backend

import (
	"context"
	"fmt"
)

// ModelInfo 是可用模型的统一描述。
type ModelInfo struct {
	ID   string
	Name string
}

// ModelLister 按 api_key(+base_url) 探测上游可用模型列表。
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelListerFactory 仅凭 options（api_key 必填，base_url 可选）构造 ModelLister。
type ModelListerFactory func(opts map[string]any) (ModelLister, error)

// MaxModels 是单次 ListModels 返回条目上限，防止极端服务返回超大列表。
const MaxModels = 200

// modelListerFactories 在 init 阶段由各后端包写入，main 后只读。
var modelListerFactories = map[string]ModelListerFactory{}

// RegisterModelLister 注册一个后端 type 的模型列表工厂。仅应在 init 中调用。
func RegisterModelLister(typ string, f ModelListerFactory) {
	modelListerFactories[typ] = f
}

// NewModelLister 按 type 与 options 构造 ModelLister。
func NewModelLister(typ string, opts map[string]any) (ModelLister, error) {
	f, ok := modelListerFactories[typ]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownBackendType, typ)
	}
	return f(opts)
}
