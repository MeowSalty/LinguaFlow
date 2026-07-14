// Package glossary 定义术语表接口与实现。
// Nop 是默认占位；FileGlossary 是 CSV 文件后端，支持 Lookup / Add / Save。
package glossary

import "context"

// Entry 是一条术语映射。
type Entry struct {
	Source        string
	Target        string
	CaseSensitive bool
	Notes         string
}

// SkipReason 描述 Add 时一条 Entry 未被写入的原因。
type SkipReason string

const (
	// SkipReasonExists：表中已存在同 source 且 target 不同的条目。
	// SkippedEntry.Existing 字段会被填充为表中现有的版本。
	SkipReasonExists SkipReason = "exists"
	// SkipReasonEmpty：Source 或 Target 为空。
	SkipReasonEmpty SkipReason = "empty"
)

// SkippedEntry 表示 Add 时被丢弃的一条候选。调用方据此可做下游修正
// （例如 inline bootstrap 在 Skipped 出现时把本批译文里的 Proposed.Target
// 替换成 Existing.Target，避免文档内术语不一致）。
type SkippedEntry struct {
	Proposed Entry // 调用方提交的版本
	Existing Entry // 表中已有版本（仅 Reason == SkipReasonExists 时有效）
	Reason   SkipReason
}

// AddResult 描述一次 Add 的处理详情。
//
// 关键约定：Proposed.Target 与 Existing.Target 完全相等时既不算 Added 也不进
// Skipped——视作 noop，避免误触发调用方的冲突修正。
type AddResult struct {
	Added   []Entry        // 真正写入表的条目
	Skipped []SkippedEntry // 因冲突或非法被丢弃的条目
}

// Glossary 在原文中查找命中的术语，并支持运行时增量。
//
// Lookup 由 translate stage 在每段调用一次；Add 由 bootstrap stage 把 LLM 抽取的
// 术语合并进运行时表。具体合并语义（覆盖/跳过/标记）由实现决定——FileGlossary
// 采用严格合并：source 已存在则跳过，并通过 AddResult.Skipped 反馈冲突详情。
type Glossary interface {
	Lookup(ctx context.Context, text, srcLang, tgtLang string) ([]Entry, error)
	Add(ctx context.Context, entries ...Entry) (AddResult, error)
}

// Saver 由可持久化的 Glossary 实现。engine 在 pipeline 成功后用 type assertion
// 检测；Nop 等内存实现不需要实现它。
type Saver interface {
	Save(ctx context.Context) error
}
