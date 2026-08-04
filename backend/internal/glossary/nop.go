package glossary

import "context"

// Nop 是默认实现：Lookup 永远返回空命中，Add 是 no-op。
type Nop struct{}

func (Nop) Lookup(context.Context, string, string, string) ([]Entry, error) {
	return nil, nil
}

func (Nop) Add(context.Context, ...Entry) (AddResult, error) {
	return AddResult{}, nil
}
