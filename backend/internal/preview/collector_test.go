package preview

import (
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func TestMemoryCollector_AppendAndSnapshot(t *testing.T) {
	c := NewMemoryCollector()
	e1 := progress.BatchEvent{Stage: "translate", SegmentCount: 3, Status: "success"}
	e2 := progress.BatchEvent{Stage: "review", SegmentCount: 1, Status: "failed"}

	c.OnBatchEvent(e1)
	c.OnBatchEvent(e2)

	got := c.Events()
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Stage != "translate" || got[1].Stage != "review" {
		t.Errorf("unexpected events: %+v", got)
	}

	// Mutating returned slice must not affect collector.
	got[0] = progress.BatchEvent{}
	afterMutate := c.Events()
	if afterMutate[0].Stage != "translate" {
		t.Error("mutating Events() snapshot mutated internal state")
	}
}

func TestMemoryCollector_Concurrency(t *testing.T) {
	const N = 100
	c := NewMemoryCollector()
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			c.OnBatchEvent(progress.BatchEvent{Stage: "concurrent", SegmentCount: 1})
		}()
	}
	wg.Wait()

	got := c.Events()
	if len(got) != N {
		t.Fatalf("got %d events, want %d", len(got), N)
	}
}
