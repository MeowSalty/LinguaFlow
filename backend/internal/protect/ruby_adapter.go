package protect

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// NewRubyProtector 返回以 protect.Protector 接口包装的 ruby 注音剥离器。
// Protect：提取注音条目到 seg.Meta["ruby_items"] 并剥离源文标签；Unprotect 为空操作
// （注音还原由 pipeline 的 RubyRestore 阶段委托 ruby.RestoreItems 完成）。
func NewRubyProtector() Protector { return &rubyProtector{} }

// rubyProtector 是 Protector 接口的注音实现（见 NewRubyProtector）：
// 委托纯函数域包 ruby.Extract 完成提取与剥离，自身只负责 Meta 落库。
type rubyProtector struct{}

func (*rubyProtector) Name() string { return "ruby" }

func (*rubyProtector) Protect(seg *model.Segment) error {
	items, stripped := ruby.Extract(seg.Source)
	seg.Source = stripped
	// 无条目时不建 map，保持「无注音 → Meta 不被写入任何 ruby key」的历史约定
	if len(items) > 0 {
		if seg.Meta == nil {
			seg.Meta = make(map[string]any)
		}
		seg.Meta["ruby_items"] = items
	}
	return nil
}

func (*rubyProtector) Unprotect(*model.Segment) error {
	// 注音还原不在 Unprotect 阶段执行：还原需要 LLM 的对齐回填结果，
	// 由 pipeline 的 RubyRestore 阶段委托 ruby.RestoreItems 完成。
	return nil
}
