package stages

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// Unprotect 把译文中的占位符还原为原片段。
type Unprotect struct {
	Protector protect.Protector
}

func NewUnprotect(p protect.Protector) *Unprotect { return &Unprotect{Protector: p} }

func (*Unprotect) Name() string { return "unprotect" }

func (s *Unprotect) Run(_ context.Context, doc *pipeline.Document) error {
	for i := range doc.Segments {
		if err := s.Protector.Unprotect(&doc.Segments[i]); err != nil {
			return err
		}
	}
	return nil
}
