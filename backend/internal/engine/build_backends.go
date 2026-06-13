// Package engine 提供引擎级别的后端构建辅助函数。
package engine

import (
	"fmt"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// BuildBackends 从配置构建后端实例列表，按 Priority 降序排列。
// 调用方负责在引擎 Close 时释放返回的后端。
func BuildBackends(cfgs []config.BackendConfig) ([]backend.Backend, error) {
	type item struct {
		backend  backend.Backend
		priority int
	}
	var items []item
	for _, c := range cfgs {
		if !c.Enabled {
			continue
		}
		b, err := backend.Build(c)
		if err != nil {
			for _, created := range items {
				_ = created.backend.Close()
			}
			return nil, fmt.Errorf("engine: build backend %q: %w", c.Name, err)
		}
		items = append(items, item{backend: b, priority: c.Priority})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("engine: no enabled backends")
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].priority > items[j].priority
	})
	out := make([]backend.Backend, len(items))
	for i, it := range items {
		out[i] = it.backend
	}
	return out, nil
}
