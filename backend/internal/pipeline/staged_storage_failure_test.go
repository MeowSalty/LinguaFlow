package pipeline

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

type blockedCommitFailureStore struct {
	*MemoryRoundStore
	committed, failing, release chan struct{}
	thirdBlocked                chan struct{}
	once                        sync.Once
	thirdOnce                   sync.Once
	cause                       error
}

type permanentCommitFailure struct{ error }

func (permanentCommitFailure) Permanent() bool { return true }

func (e permanentCommitFailure) Unwrap() error { return e.error }

func (*blockedCommitFailureStore) ResourceIdentity() int { return 1 }
func (*blockedCommitFailureStore) RoundIdentity() int    { return 1 }

func (s *blockedCommitFailureStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	if c.Index == 0 {
		outcome, err := s.MemoryRoundStore.Commit(ctx, c, result)
		if err == nil && outcome == CommitAccepted {
			s.once.Do(func() { close(s.committed) })
		}
		return outcome, err
	}
	close(s.failing)
	select {
	case <-s.release:
		return "", s.cause
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *blockedCommitFailureStore) Response(ctx context.Context, id string, freeze, raw, business int64) (MainResponse, error) {
	response, err := s.MemoryRoundStore.Response(ctx, id, freeze, raw, business)
	release := func() {
		done := response.ReleaseOwned
		response = MainResponse{}
		if done != nil {
			done()
		}
	}
	if err != nil {
		release()
		return MainResponse{}, err
	}
	// Wait before promotion acquires the saver slot or a build worker.
	switch {
	case response.Cursor == 1:
		select {
		case <-s.committed:
		case <-ctx.Done():
			release()
			return MainResponse{}, ctx.Err()
		}
	case response.Cursor >= 2:
		s.thirdOnce.Do(func() { close(s.thirdBlocked) })
		<-ctx.Done()
		release()
		return MainResponse{}, ctx.Err()
	}
	return response, nil
}

func TestStagedStorageFailureStopsSharedProducerAndPreservesPaidFacts(t *testing.T) {
	limits := DefaultCandidateLimits()
	limits.Segments = 16
	r := stagedTestRuntime(t, limits)
	cause := permanentCommitFailure{errors.New("terminal candidate commit failure")}
	store := &blockedCommitFailureStore{
		MemoryRoundStore: NewMemoryRoundStore(nil),
		committed:        make(chan struct{}),
		failing:          make(chan struct{}),
		release:          make(chan struct{}),
		thirdBlocked:     make(chan struct{}),
		cause:            cause,
	}
	h := &windowBatchProbe{batchSize: 16}
	handler := h.handler(t, r)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	var runs sync.WaitGroup
	defer func() {
		cancel()
		runs.Wait()
	}()
	first, second := make(chan error, 1), make(chan error, 1)
	runs.Add(1)
	go func() {
		defer runs.Done()
		_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, newTestDoc(16), store, r, quietLogger(), nil)
		first <- err
	}()
	select {
	case <-store.failing:
	case <-ctx.Done():
		t.Fatal("second candidate never reached its failing commit barrier")
	}
	select {
	case <-store.thirdBlocked:
	case <-ctx.Done():
		t.Fatal("third member never reached its response cancellation barrier")
	}
	waiterMemory := NewMemoryRoundStore(nil)
	if _, err := waiterMemory.Seal(ctx, []int{0}); err != nil {
		t.Fatal(err)
	}
	waiter := &windowResourceStore{RoundStore: waiterMemory, resourceID: 2, roundID: 1}
	runs.Add(1)
	go func() {
		defer runs.Done()
		_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: handler}, newTestDoc(1), waiter, r, quietLogger(), nil)
		second <- err
	}()
	waitWindowProbe(t, func() bool { _, queued := r.Preparation.Snapshot(); return queued > 0 })
	close(store.release)
	select {
	case err := <-first:
		if !errors.Is(err, cause) || !storageFailure(err) {
			t.Fatalf("actual storage cause lost: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("failed resource did not join")
	}
	select {
	case err := <-second:
		if !errors.Is(err, backend.ErrAdmissionClosed) {
			t.Fatalf("waiting producer did not stop dispatch: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("waiting resource retained a full-owner wait after storage failure")
	}
	if batches, _ := h.snapshot(); len(batches) != 1 || len(batches[0]) != 16 {
		t.Fatalf("another request dispatched after storage failure: %v", batches)
	}
	state, err := store.Load(ctx)
	if err != nil || !slices.Equal(state.Completed, []int{0}) {
		t.Fatalf("confirmed fact lost: %+v %v", state, err)
	}
	headers, _, err := store.ResponseHeaders(ctx, 0, 16)
	if err != nil || len(headers) != 1 || headers[0].Cursor != 2 || headers[0].MemberCount != 16 || headers[0].ResponseBytes == 0 {
		t.Fatalf("paid response lost: %+v %v", headers, err)
	}
	candidates, _, err := store.CandidateHeaders(ctx, 0, 16)
	if err != nil || len(candidates) != 1 || candidates[0].Index != 1 {
		t.Fatalf("failed candidate is not recoverable: %+v %v", candidates, err)
	}
	saved := false
	if err := r.SaveNamed(ctx, persistence.Metadata{Operation: "paid_fact", Class: persistence.Reliable}, func(context.Context) error { saved = true; return nil }); err != nil || !saved {
		t.Fatalf("shared dispatch stop closed reliable saver: saved=%t err=%v", saved, err)
	}
}

type controlCommitStore struct {
	*MemoryRoundStore
	cause   error
	entered chan struct{}
}

func (s *controlCommitStore) Commit(ctx context.Context, _ *Candidate, _ TranslatedSegment) (CommitOutcome, error) {
	if s.entered != nil {
		close(s.entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return "", s.cause
}

func TestStagedStorageControlErrorsDoNotCloseSharedDispatch(t *testing.T) {
	for _, cause := range []error{context.Canceled, backend.ErrDispatchPaused} {
		t.Run(cause.Error(), func(t *testing.T) {
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			h := &windowBatchProbe{batchSize: 1}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(1), &controlCommitStore{MemoryRoundStore: NewMemoryRoundStore(nil), cause: cause}, r, quietLogger(), nil)
			if !errors.Is(err, cause) {
				t.Fatalf("control cause lost: %v", err)
			}
			if err := r.beginDispatch(ctx); err != nil {
				t.Fatalf("control failure stopped other resource dispatch: %v", err)
			}
		})
	}
}

func TestStagedStorageCallerCancellationDoesNotCloseSharedDispatch(t *testing.T) {
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	h := &windowBatchProbe{batchSize: 1}
	store := &controlCommitStore{MemoryRoundStore: NewMemoryRoundStore(nil), entered: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(1), store, r, quietLogger(), nil)
		done <- err
	}()
	select {
	case <-store.entered:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("caller-cancel commit barrier not reached")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cause lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled caller did not join")
	}
	if err := r.beginDispatch(context.Background()); err != nil {
		t.Fatalf("local caller cancellation stopped shared dispatch: %v", err)
	}
}
