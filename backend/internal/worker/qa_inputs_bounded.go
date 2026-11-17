package worker

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func buildQACheckInputsBounded(b *backend.BuildBounds, result pipeline.BatchResult) ([]qa.CheckInput, error) {
	if err := backend.ReserveSlice[qa.CheckInput](b, int64(len(result.Segments))); err != nil {
		return nil, err
	}
	return buildQACheckInputs(result), nil
}

func issuesForBounded(b *backend.BuildBounds, index int, issues []qa.QualityIssue) ([]qa.QualityIssue, error) {
	count := 0
	for _, issue := range issues {
		if issue.SegmentIndex == index {
			count++
		}
	}
	if err := backend.ReserveSlice[qa.QualityIssue](b, int64(count)); err != nil {
		return nil, err
	}
	result := make([]qa.QualityIssue, 0, count)
	for _, issue := range issues {
		if issue.SegmentIndex == index {
			result = append(result, issue)
		}
	}
	return result, nil
}
