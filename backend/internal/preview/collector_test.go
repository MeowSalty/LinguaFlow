package preview

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
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

func TestMemoryCollectorMutableInputCloneAndClose(t *testing.T) {
	c := NewMemoryCollector()
	event := progress.BatchEvent{SegmentIDs: []string{"one"}, JSONSchema: map[string]any{"field": "old"}}
	c.OnBatchEvent(event)
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	event.SegmentIDs[0] = "changed"
	event.JSONSchema["field"] = "changed"
	got := c.Events()
	if len(got) != 1 || got[0].SegmentIDs[0] != "one" || got[0].JSONSchema["field"] != "old" {
		t.Fatalf("mutable backing borrowed: %+v", got)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if c.events != nil || c.Events() != nil {
		t.Fatal("event backing survived close")
	}
}
func TestMemoryCollectorCapacityCancelsAndDoesNotAppend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewBoundedMemoryCollector(ctx, CollectorBudget{Reserve: func(int64) error { return resourcebudget.ErrCapacity }, Cancel: cancel})
	c.OnBatchEvent(progress.BatchEvent{Stage: "translate"})
	if !errors.Is(c.Err(), resourcebudget.ErrCapacity) || ctx.Err() == nil || len(c.events) != 0 {
		t.Fatalf("failure hidden: %v %v %d", c.Err(), ctx.Err(), len(c.events))
	}
	c.OnBatchEvent(progress.BatchEvent{})
	if len(c.events) != 0 {
		t.Fatal("appended after failure")
	}
	deep := map[string]any{}
	node := deep
	for i := 0; i < 80; i++ {
		next := map[string]any{}
		node["next"] = next
		node = next
	}
	bounded := NewMemoryCollector()
	bounded.OnBatchEvent(progress.BatchEvent{JSONSchema: deep})
	if !errors.Is(bounded.Err(), backend.ErrBuildCapacity) {
		t.Fatalf("deep graph accepted: %v", bounded.Err())
	}
}
func TestMemoryCollectorSnapshotCopyHasGrant(t *testing.T) {
	var reserved, released int64
	c := NewBoundedMemoryCollector(context.Background(), CollectorBudget{Reserve: func(n int64) error { reserved += n; return nil }, Release: func(n int64) error { released += n; return nil }})
	c.OnBatchEvent(progress.BatchEvent{Stage: "x"})
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	before := reserved - released
	if len(c.Events()) != 1 || reserved-released-before != backend.MaxNodeAllocationBytes {
		t.Fatal("snapshot slice copy was not granted")
	}
	c.copyBytes = MaxCollectorBytes
	if c.Events() != nil || !errors.Is(c.Err(), backend.ErrBuildCapacity) {
		t.Fatal("copy budget not enforced")
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
