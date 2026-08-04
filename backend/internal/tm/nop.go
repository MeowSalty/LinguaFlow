package tm

import "context"

// Nop 永远返回空命中，Add 是 no-op。
type Nop struct{}

func (Nop) Search(context.Context, string, string, string) ([]Match, error) {
	return nil, nil
}

func (Nop) Add(context.Context, string, string, string, string) error {
	return nil
}
