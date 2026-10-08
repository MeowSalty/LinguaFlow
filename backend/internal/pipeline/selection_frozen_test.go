package pipeline

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestEmptyExecutionSelectionsDoNotExpandAtRuntime(t *testing.T) {
	ctx := context.Background()
	for _, codes := range [][]string{nil, {}} {
		adjudicate := &AdjudicateHandler{AdjudicateCodes: codes, Backend: &fakeBackend{name: "fake"}, Renderer: newAdjudicationRenderer(t), BatchSize: 10, Logger: quietLogger()}
		doc := adjudicableDoc([]string{"translated"}, [][]qa.QualityIssue{{{Code: "source_residual"}}})
		batches, err := adjudicate.BuildBatches(ctx, doc, nil, 0)
		if err != nil || len(batches) != 0 {
			t.Fatalf("empty adjudication selection expanded: batches=%v err=%v", batches, err)
		}
		revise := &ReviseHandler{IssueCodes: codes, Backend: &fakeBackend{name: "fake"}, Renderer: newReviseRenderer(t), BatchSize: 10, Logger: quietLogger()}
		batches, err = revise.BuildBatches(ctx, reviseDoc(), nil, 0)
		if err != nil || len(batches) != 0 {
			t.Fatalf("empty revision selection expanded: batches=%v err=%v", batches, err)
		}
	}
}
