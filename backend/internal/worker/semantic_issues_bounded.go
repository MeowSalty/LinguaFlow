package worker

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"unsafe"
)

func mergeSemanticQAIssuesBounded(b *backend.BuildBounds, existing, fresh []qa.QualityIssue) ([]qa.QualityIssue, error) {
	n := len(fresh)
	for _, e := range existing {
		if !prompt.IsSemanticQACode(e.Code) {
			n++
		}
	}
	if err := b.ReserveNodes(int64(n), int64(unsafe.Sizeof(qa.QualityIssue{}))); err != nil {
		return nil, err
	}
	out := make([]qa.QualityIssue, 0, n)
	for _, e := range existing {
		if !prompt.IsSemanticQACode(e.Code) {
			out = append(out, e)
		}
	}
	return append(out, fresh...), nil
}
