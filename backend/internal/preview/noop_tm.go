package preview

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// NoopTM implements tm.TranslationMemory as a no-op. Search returns nil
// matches. Add is a no-op so preview translations never pollute the
// translation memory.
type NoopTM struct{}

func (NoopTM) Search(_ context.Context, _, _, _ string) ([]tm.Match, error) {
	return nil, nil
}

func (NoopTM) Add(_ context.Context, _, _, _, _ string) error {
	return nil
}
