package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type recoveryBeforePromotionStore struct {
	*MemoryRoundStore
	resume                                                                    bool
	readerReady, responseReady, releaseRecovery, candidateWritten, releaseACK chan struct{}
	responseOnce                                                              sync.Once
	recoveryComplete                                                          atomic.Bool
	pendingCount                                                              int
	pendingBytes                                                              int64
	windowPresent                                                             bool
}

func (s *recoveryBeforePromotionStore) ResponseHeaders(ctx context.Context, after, limit int) ([]MainResponseHeader, int, error) {
	page, next, err := s.MemoryRoundStore.ResponseHeaders(ctx, after, limit)
	if s.resume && len(page) > 0 {
		s.responseOnce.Do(func() { close(s.responseReady) })
	}
	return page, next, err
}

func (s *recoveryBeforePromotionStore) CandidateHeaders(ctx context.Context, after, limit int) ([]*Candidate, int, error) {
	if s.resume && after == 0 {
		close(s.readerReady)
		select {
		case <-s.releaseRecovery:
		case <-ctx.Done():
			return nil, after, ctx.Err()
		}
	}
	page, next, err := s.MemoryRoundStore.CandidateHeaders(ctx, after, limit)
	if s.resume && err == nil && len(page) == 0 {
		s.recoveryComplete.Store(true)
	}
	return page, next, err
}

func (s *recoveryBeforePromotionStore) PromoteMember(ctx context.Context, key MemberKey, c *Candidate, next WorkCursor, failure string) error {
	if c != nil && c.Index == 1 {
		if !s.resume {
			return terminalRuntimeStoreError{errors.New("disk unavailable")}
		}
		if !s.recoveryComplete.Load() {
			return terminalRuntimeStoreError{errors.New("promotion wrote before discovery completed")}
		}
	}
	if err := s.MemoryRoundStore.PromoteMember(ctx, key, c, next, failure); err != nil {
		return err
	}
	if s.resume && c != nil && c.Index == 1 {
		runtime := ExecutionRuntimeFromContext(ctx)
		entry := runtime.Admission.BudgetEntries()[c.ID]
		s.pendingCount, s.pendingBytes = entry.Count, entry.Bytes
		runtime.Window.mu.Lock()
		_, s.windowPresent = runtime.Window.entries[c.ID]
		runtime.Window.mu.Unlock()
		close(s.candidateWritten)
		select {
		case <-s.releaseACK:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestMemberRecoveryCompletesBeforePromotion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first := stagedTestRuntime(t, DefaultCandidateLimits())
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"first","2":"second"}}`}, nil
	}}
	store := &recoveryBeforePromotionStore{MemoryRoundStore: NewMemoryRoundStore(nil), readerReady: make(chan struct{}), responseReady: make(chan struct{}), releaseRecovery: make(chan struct{}), candidateWritten: make(chan struct{}), releaseACK: make(chan struct{})}
	defer store.Close()
	doc := newTestDoc(2)
	defer first.CloseDocument(doc)
	_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, first, main)}, doc, store, first, quietLogger(), progress.Nop{})
	var storageErr *StorageError
	if !errors.As(err, &storageErr) || main.calls.Load() != 1 {
		t.Fatalf("initial partial save: %v calls=%d", err, main.calls.Load())
	}
	headers, _, err := store.ResponseHeaders(ctx, 0, 16)
	if err != nil || len(headers) != 1 || headers[0].Cursor != 1 {
		t.Fatalf("partial response: %+v %v", headers, err)
	}
	store.resume = true
	second := stagedTestRuntime(t, DefaultCandidateLimits())
	defer second.CloseDocument(doc)
	type outcome struct {
		result RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, second, main)}, doc, store, second, quietLogger(), progress.Nop{})
		done <- outcome{result, err}
	}()
	var recoveryOnce, ackOnce sync.Once
	joined := false
	defer func() {
		recoveryOnce.Do(func() { close(store.releaseRecovery) })
		ackOnce.Do(func() { close(store.releaseACK) })
		cancel()
		if !joined {
			<-done
		}
	}()
	awaitSignal(t, store.readerReady)
	awaitSignal(t, store.responseReady)
	// Give the coordinator an opportunity to incorrectly launch promotion while
	// discovery is held. Correctness itself is checked again inside PromoteMember.
	select {
	case out := <-done:
		joined = true
		t.Fatalf("round ended before discovery release: %+v", out)
	case <-store.candidateWritten:
		t.Fatal("candidate written while discovery was held")
	case <-time.After(100 * time.Millisecond):
	}
	recoveryOnce.Do(func() { close(store.releaseRecovery) })
	awaitSignal(t, store.candidateWritten)
	if !store.recoveryComplete.Load() || store.pendingCount != 0 || store.pendingBytes <= 0 || store.windowPresent {
		t.Fatalf("incorrect ACK boundary: discovery=%t count=%d bytes=%d installed=%t", store.recoveryComplete.Load(), store.pendingCount, store.pendingBytes, store.windowPresent)
	}
	ackOnce.Do(func() { close(store.releaseACK) })
	out := <-done
	joined = true
	if out.err != nil || len(out.result.Resolved) != 2 || main.calls.Load() != 1 || doc.Segments[0].Target != "first" || doc.Segments[1].Target != "second" {
		t.Fatalf("recovery result=%+v err=%v calls=%d", out.result, out.err, main.calls.Load())
	}
	snapshot := second.Window.Snapshot()
	if snapshot.Segments != 0 || snapshot.Bytes != 0 {
		t.Fatalf("remaining inventory: %+v", snapshot)
	}
	t.Logf("discovery_complete=true ACK_old_count=%d ACK_bytes=%d ACK_window_present=%t resolved=%d calls=%d final_inventory=%+v", store.pendingCount, store.pendingBytes, store.windowPresent, len(out.result.Resolved), main.calls.Load(), snapshot)
}
