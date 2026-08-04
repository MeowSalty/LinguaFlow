// Package tm 定义翻译记忆接口。MVP 仅提供 Nop 实现。
// 完整实现需引入 SQLite 与相似度算法（fuzzy match），待后续阶段。
package tm

import "context"

// Match 是一条命中记录。Score ∈ [0,1]。
type Match struct {
	Source string
	Target string
	Score  float32
}

// TranslationMemory 在翻译前查询相似句，并在翻译后写回。
type TranslationMemory interface {
	Search(ctx context.Context, src, srcLang, tgtLang string) ([]Match, error)
	Add(ctx context.Context, src, tgt, srcLang, tgtLang string) error
}
