package glossary

import "context"

// Nop 是默认实现：永远返回空命中。
type Nop struct{}

func (Nop) Lookup(context.Context, string, string, string) ([]Entry, error) {
	return nil, nil
}
