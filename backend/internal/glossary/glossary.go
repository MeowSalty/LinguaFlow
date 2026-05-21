// Package glossary 定义术语表接口。MVP 仅提供 Nop 实现。
package glossary

import "context"

// Entry 是一条术语映射。
type Entry struct {
	Source        string
	Target        string
	CaseSensitive bool
	Notes         string
}

// Glossary 在原文中查找命中的术语。
type Glossary interface {
	Lookup(ctx context.Context, text, srcLang, tgtLang string) ([]Entry, error)
}
