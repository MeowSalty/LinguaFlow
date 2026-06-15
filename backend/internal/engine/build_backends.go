// Package engine 提供引擎级别的后端构建辅助函数。
package engine

import (
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// BuildBackends 从配置构建后端实例列表，保持配置中的顺序。
// 调用方负责在引擎 Close 时释放返回的后端。
func BuildBackends(cfgs []config.BackendConfig) ([]backend.Backend, error) {
	var out []backend.Backend
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		b, err := backend.Build(c)
		if err != nil {
			for _, created := range out {
				_ = created.Close()
			}
			return nil, fmt.Errorf("engine: build backend %q: %w", c.Name, err)
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("engine: no enabled backends")
	}
	return out, nil
}
