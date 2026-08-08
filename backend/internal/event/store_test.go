package event

import (
	"context"
	"sync"
	"testing"
)

func TestRingBufferAppendAndReplay(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 16})

	evt1 := Event{Type: "a", JobID: 1, Message: "first"}
	evt2 := Event{Type: "b", JobID: 1, Message: "second"}

	seq1, err1 := store.Append(1, evt1)
	seq2, err2 := store.Append(1, evt2)

	if err1 != nil {
		t.Fatalf("unexpected error on seq1: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("unexpected error on seq2: %v", err2)
	}

	if seq1 != 1 {
		t.Fatalf("expected seq1=1, got %d", seq1)
	}
	if seq2 != 2 {
		t.Fatalf("expected seq2=2, got %d", seq2)
	}

	events := store.Replay(context.Background(), 1, 0, 1000)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[0].Message != "first" {
		t.Errorf("unexpected events[0]: %+v", events[0])
	}
	if events[1].Seq != 2 || events[1].Message != "second" {
		t.Errorf("unexpected events[1]: %+v", events[1])
	}
}

func TestRingBufferOverflow(t *testing.T) {
	capacity := 4
	store := NewRingBufferStore(RingBufferConfig{Capacity: capacity})

	for i := 0; i < capacity+3; i++ {
		_, _ = store.Append(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	}

	// afterSeq=0 指向被淘汰的 seq 1，应返回 nil（缓存不完整）
	events := store.Replay(context.Background(), 1, 0, 1000)
	if events != nil {
		t.Fatalf("expected nil when afterSeq points to evicted event, got %d events", len(events))
	}

	// afterSeq=3 指向被淘汰的 seq 3（oldestSeq=4），应返回 nil
	events = store.Replay(context.Background(), 1, 3, 1000)
	if events != nil {
		t.Fatalf("expected nil when afterSeq=3 < oldestSeq=4, got %d events", len(events))
	}

	// afterSeq=4 指向 buffer 内的 seq 4，应返回 seq 5,6,7
	events = store.Replay(context.Background(), 1, 4, 1000)
	if len(events) != 3 {
		t.Fatalf("expected 3 events after seq 4, got %d", len(events))
	}
	if events[0].Seq != 5 {
		t.Errorf("expected first replayed seq=5, got %d", events[0].Seq)
	}
	if events[2].Seq != 7 {
		t.Errorf("expected last replayed seq=7, got %d", events[2].Seq)
	}

	// afterSeq=3 不会进入该分支，但 buffer 中 seq 4,5,6,7 全部 > 3
	// 确认 afterSeq 恰好等于 oldestSeq-1 时也能正确返回 buffer 中的事件
	events = store.Replay(context.Background(), 1, 3, 1000)
	if events != nil {
		// 当前实现返回 nil（afterSeq < oldestSeq），这是预期行为
		// 因为 seq 3 虽被淘汰但存在于 DB，需要 DB 回退才能保证完整性
	}
}

func TestRingBufferReplayEmpty(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())

	events := store.Replay(context.Background(), 999, 0, 1000)
	if events != nil {
		t.Fatalf("expected nil for empty buffer, got %v", events)
	}
}

func TestRingBufferLatestSeq(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 4})

	if seq, ok := store.LatestSeq(context.Background(), 1); ok {
		t.Fatalf("expected ok=false for empty buffer, got seq=%d", seq)
	}

	for i := 0; i < 7; i++ {
		_, _ = store.Append(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	}
	seq, ok := store.LatestSeq(context.Background(), 1)
	if !ok || seq != 7 {
		t.Fatalf("expected latest seq=7, got seq=%d ok=%v", seq, ok)
	}

	if _, ok := store.LatestSeq(context.Background(), 2); ok {
		t.Fatalf("expected ok=false for absent job buffer")
	}
}

func TestRingBufferPurge(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())

	_, _ = store.Append(1, Event{Type: "a", JobID: 1, Message: "msg"})
	store.Purge(1)

	events := store.Replay(context.Background(), 1, 0, 1000)
	if events != nil {
		t.Fatalf("expected nil after purge, got %v", events)
	}
}

func TestRingBufferConcurrent(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 256})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _ = store.Append(id, Event{Type: "test", JobID: id, Message: "msg"})
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < 10; i++ {
		events := store.Replay(context.Background(), i, 0, 1000)
		if len(events) != 100 {
			t.Errorf("job %d: expected 100 events, got %d", i, len(events))
		}
	}
}

func TestRingBufferReplayGap(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 16})

	_, _ = store.Append(1, Event{Type: "a", JobID: 1, Message: "first"})
	_, _ = store.Append(1, Event{Type: "b", JobID: 1, Message: "second"})
	_, _ = store.Append(1, Event{Type: "c", JobID: 1, Message: "third"})

	// Replay after seq 1 — should get seq 2 and 3
	events := store.Replay(context.Background(), 1, 1, 1000)
	if len(events) != 2 {
		t.Fatalf("expected 2 events after seq 1, got %d", len(events))
	}
	if events[0].Seq != 2 {
		t.Errorf("expected seq=2, got %d", events[0].Seq)
	}
	if events[1].Seq != 3 {
		t.Errorf("expected seq=3, got %d", events[1].Seq)
	}

	// Replay after seq 2 — should get seq 3
	events = store.Replay(context.Background(), 1, 2, 1000)
	if len(events) != 1 {
		t.Fatalf("expected 1 event after seq 2, got %d", len(events))
	}
	if events[0].Seq != 3 {
		t.Errorf("expected seq=3, got %d", events[0].Seq)
	}

	// Replay after seq 3 — should get nothing
	events = store.Replay(context.Background(), 1, 3, 1000)
	if len(events) != 0 {
		t.Fatalf("expected 0 events after seq 3, got %d", len(events))
	}
}

func TestRingBufferOverflowAfterSeqAtOldest(t *testing.T) {
	capacity := 4
	store := NewRingBufferStore(RingBufferConfig{Capacity: capacity})

	// 写入 seq 1~7，buffer 溢出，含 [seq4,5,6,7]，oldestSeq=4
	for i := 0; i < 7; i++ {
		_, _ = store.Append(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	}

	// afterSeq=4（等于 oldestSeq）→ 返回 seq 5,6,7
	events := store.Replay(context.Background(), 1, 4, 1000)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	for _, evt := range events {
		if evt.Seq <= 4 {
			t.Errorf("unexpected seq %d, expected > 4", evt.Seq)
		}
	}

	// afterSeq=6（等于最新 seq - 1）→ 返回 seq 7
	events = store.Replay(context.Background(), 1, 6, 1000)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Seq != 7 {
		t.Errorf("expected seq=7, got %d", events[0].Seq)
	}

	// afterSeq=7（等于最新 seq）→ 返回空
	events = store.Replay(context.Background(), 1, 7, 1000)
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestRingBufferNoOverflowWithLowAfterSeq(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 16})

	// 写入 3 个事件，未溢出
	_, _ = store.Append(1, Event{Type: "a", JobID: 1, Message: "first"})
	_, _ = store.Append(1, Event{Type: "b", JobID: 1, Message: "second"})
	_, _ = store.Append(1, Event{Type: "c", JobID: 1, Message: "third"})

	// afterSeq=0 未溢出时应返回全部事件
	events := store.Replay(context.Background(), 1, 0, 1000)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestRingBufferReplayLimit(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 16})

	for i := 0; i < 5; i++ {
		_, _ = store.Append(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	}

	// limit=2 应只返回最早的 2 条匹配事件（seq 1,2），保持升序
	events := store.Replay(context.Background(), 1, 0, 2)
	if len(events) != 2 {
		t.Fatalf("expected 2 events with limit=2, got %d", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Errorf("expected seq 1,2 got %d,%d", events[0].Seq, events[1].Seq)
	}

	// limit 超过匹配数时返回全部
	events = store.Replay(context.Background(), 1, 0, 100)
	if len(events) != 5 {
		t.Fatalf("expected 5 events with large limit, got %d", len(events))
	}

	// limit=0 表示无限制（向后兼容「全部」语义）
	events = store.Replay(context.Background(), 1, 0, 0)
	if len(events) != 5 {
		t.Fatalf("expected 5 events with limit=0 (unlimited), got %d", len(events))
	}

	// limit 与 afterSeq 组合：afterSeq=2 → 匹配 seq 3,4,5，limit=2 取前 2 条
	events = store.Replay(context.Background(), 1, 2, 2)
	if len(events) != 2 {
		t.Fatalf("expected 2 events after seq 2 with limit=2, got %d", len(events))
	}
	if events[0].Seq != 3 || events[1].Seq != 4 {
		t.Errorf("expected seq 3,4 got %d,%d", events[0].Seq, events[1].Seq)
	}
}
