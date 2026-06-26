package pipeline

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// Protect 调用 Protector 把不应翻译的片段替换为占位符。
type Protect struct {
	Protector protect.Protector
}

func NewProtect(p protect.Protector) *Protect { return &Protect{Protector: p} }

func (*Protect) Name() string { return "protect" }

func (s *Protect) Run(_ context.Context, doc *Document) error {
	for i := range doc.Segments {
		// 在替换前快照原文，供 BuildContext / 调试使用。
		// 已有值时（理论上不该出现）不覆盖。
		if doc.Segments[i].OriginalSource == "" {
			doc.Segments[i].OriginalSource = doc.Segments[i].Source
		}
		if err := s.Protector.Protect(&doc.Segments[i]); err != nil {
			return err
		}
	}
	return nil
}
