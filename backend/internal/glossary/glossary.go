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

// Glossary 在原文中查找命中的术语，并支持运行时增量。
//
// Lookup 由 translate stage 在每段调用一次；Add 由 bootstrap stage 把 LLM 抽取的
// 术语合并进运行时表。具体合并语义（覆盖/跳过/标记）由实现决定——FileGlossary
// 采用严格合并：source 已存在则跳过。
type Glossary interface {
	Lookup(ctx context.Context, text, srcLang, tgtLang string) ([]Entry, error)
	Add(ctx context.Context, entries ...Entry) error
}

// Saver 由可持久化的 Glossary 实现。engine 在 pipeline 成功后用 type assertion
// 检测；Nop 等内存实现不需要实现它。
type Saver interface {
	Save(ctx context.Context) error
}
