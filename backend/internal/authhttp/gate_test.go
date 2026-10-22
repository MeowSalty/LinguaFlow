package authhttp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateEnterAllowsWhenOpen(t *testing.T) {
	gate := NewGate()
	ctx, release, err := gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatalf("Enter returned error: %v", err)
	}
	if ctx == nil || release == nil {
		t.Fatal("Enter returned nil context or release")
	}
	release()
	if len(gate.keys) != 0 {
		t.Fatalf("keys not cleaned after release; len=%d", len(gate.keys))
	}
}

func TestGateEnterRejectsWhenBlocked(t *testing.T) {
	gate := NewGate()
	unblock, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if !errors.Is(enterErr, ErrGateBlocked) {
		t.Fatalf("expected ErrGateBlocked; got %v", enterErr)
	}
	unblock()
	if len(gate.keys) != 0 {
		t.Fatalf("keys not cleaned after unblock; len=%d", len(gate.keys))
	}
}

func TestGateEnterCheckFails(t *testing.T) {
	gate := NewGate()
	wantErr := errors.New("connection disabled")
	_, _, err := gate.Enter(context.Background(), "conn-1", func(context.Context) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected check error; got %v", err)
	}
	if len(gate.keys) != 0 {
		t.Fatalf("keys not cleaned after failed check; len=%d", len(gate.keys))
	}
}

// TestGateRegistersActivityBeforeCheck verifies that the activity is registered
// before the admission check runs, so a concurrent Block cancels it and waits.
func TestGateRegistersActivityBeforeCheck(t *testing.T) {
	gate := NewGate()
	checkStarted := make(chan struct{})
	enterErrCh := make(chan error, 1)
	go func() {
		_, _, err := gate.Enter(context.Background(), "conn-1", func(actCtx context.Context) error {
			close(checkStarted)
			<-actCtx.Done()
			return actCtx.Err()
		})
		enterErrCh <- err
	}()
	<-checkStarted
	unblock, blockErr := gate.Block(context.Background(), "conn-1")
	if blockErr != nil {
		t.Fatalf("Block returned error: %v", blockErr)
	}
	unblock()
	enterErr := <-enterErrCh
	if enterErr == nil || !errors.Is(enterErr, context.Canceled) {
		t.Fatalf("expected context.Canceled from cancelled check; got %v", enterErr)
	}
}

// TestGateBlockWaitsForConcurrentBlock verifies that a second Block waits for
// the first to unblock, then acquires the block itself.
func TestGateBlockWaitsForConcurrentBlock(t *testing.T) {
	gate := NewGate()
	unblock1, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	block2Done := make(chan func(), 1)
	block2ErrCh := make(chan error, 1)
	go func() {
		unblock2, err := gate.Block(context.Background(), "conn-1")
		if err != nil {
			block2ErrCh <- err
			return
		}
		block2Done <- unblock2
	}()
	time.Sleep(10 * time.Millisecond)
	unblock1()
	select {
	case unblock2 := <-block2Done:
		_, _, enterErr := gate.Enter(context.Background(), "conn-1", nil)
		if !errors.Is(enterErr, ErrGateBlocked) {
			t.Fatalf("expected ErrGateBlocked from block2; got %v", enterErr)
		}
		unblock2()
	case err := <-block2ErrCh:
		t.Fatalf("block2 returned error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("block2 did not acquire after block1 unblocked")
	}
}

// TestGateBlockConcurrentWaitTimeout verifies that a concurrent Block returns
// ctx.Err() when the first Block does not unblock in time.
func TestGateBlockConcurrentWaitTimeout(t *testing.T) {
	gate := NewGate()
	unblock1, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	defer unblock1()
	blockCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = gate.Block(blockCtx, "conn-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded; got %v", err)
	}
	_, _, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if !errors.Is(enterErr, ErrGateBlocked) {
		t.Fatalf("block1 should still be active; got %v", enterErr)
	}
}

func TestGateBlockCancelsActiveAndDrains(t *testing.T) {
	gate := NewGate()
	entered := make(chan struct{})
	var released atomic.Int32
	actCtx, release, err := gate.Enter(context.Background(), "conn-1", func(context.Context) error {
		close(entered)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-actCtx.Done()
		released.Add(1)
		// Block waits for release, so record cancellation before releasing.
		release()
	}()
	<-entered
	unblock, blockErr := gate.Block(context.Background(), "conn-1")
	if blockErr != nil {
		t.Fatalf("Block returned error: %v", blockErr)
	}
	if released.Load() != 1 {
		t.Fatalf("activity not released after Block; released=%d", released.Load())
	}
	_, _, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if !errors.Is(enterErr, ErrGateBlocked) {
		t.Fatalf("Enter after Block should reject; got %v", enterErr)
	}
	unblock()
	_, release2, err := gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatalf("Enter after unblock returned error: %v", err)
	}
	release2()
}

func TestGateBlockRollsBackOnTimeout(t *testing.T) {
	gate := NewGate()
	_, slowRelease, err := gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slowRelease()
	blockCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = gate.Block(blockCtx, "conn-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded; got %v", err)
	}
	_, release2, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if enterErr != nil {
		t.Fatalf("Enter after rollback returned error: %v", enterErr)
	}
	release2()
}

func TestGateBlockNoActive(t *testing.T) {
	gate := NewGate()
	unblock, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatalf("Block with no active returned error: %v", err)
	}
	unblock()
	_, release, err := gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatalf("Enter after unblock returned error: %v", err)
	}
	release()
}

func TestGateUnblockIdempotent(t *testing.T) {
	gate := NewGate()
	unblock, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	unblock()
	_, _, err = gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatalf("Enter after double unblock returned error: %v", err)
	}
	_, release, err := gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatalf("Enter after double unblock returned error: %v", err)
	}
	release()
}

// TestGateStaleUnblockDoesNotAffectNewBlock verifies that calling an old
// unblock after a new Block has been acquired is a no-op.
func TestGateStaleUnblockDoesNotAffectNewBlock(t *testing.T) {
	gate := NewGate()
	unblock1, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	unblock1()
	unblock2, err := gate.Block(context.Background(), "conn-1")
	if err != nil {
		t.Fatal(err)
	}
	unblock1()
	_, _, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if !errors.Is(enterErr, ErrGateBlocked) {
		t.Fatalf("stale unblock should not have opened the gate; got %v", enterErr)
	}
	unblock2()
	_, release, err := gate.Enter(context.Background(), "conn-1", nil)
	if err != nil {
		t.Fatalf("Enter after unblock2 returned error: %v", err)
	}
	release()
}

func TestGateKeyCleanup(t *testing.T) {
	gate := NewGate()
	_, release, err := gate.Enter(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if len(gate.keys) != 0 {
		t.Fatalf("keys not cleaned after release; len=%d", len(gate.keys))
	}
	unblock, err := gate.Block(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	if len(gate.keys) != 0 {
		t.Fatalf("keys not cleaned after unblock; len=%d", len(gate.keys))
	}
	_, release2, err := gate.Enter(context.Background(), "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gate.keys) != 1 {
		t.Fatalf("keys should exist while active; len=%d", len(gate.keys))
	}
	release2()
	if len(gate.keys) != 0 {
		t.Fatalf("keys not cleaned after final release; len=%d", len(gate.keys))
	}
}

func TestGateConcurrentEnterAndBlock(t *testing.T) {
	gate := NewGate()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := gate.Enter(context.Background(), "conn-concurrent", nil)
			if err != nil {
				return
			}
			release()
		}()
	}
	unblock, err := gate.Block(context.Background(), "conn-concurrent")
	if err != nil {
		t.Fatalf("Block returned error: %v", err)
	}
	unblock()
	wg.Wait()
}

func TestGateConcurrentBlockStress(t *testing.T) {
	gate := NewGate()
	for round := 0; round < 20; round++ {
		key := fmt.Sprintf("stress-%d", round)
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				unblock, err := gate.Block(context.Background(), key)
				if err != nil {
					return
				}
				unblock()
			}()
		}
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, release, err := gate.Enter(context.Background(), key, nil)
				if err != nil {
					return
				}
				release()
			}()
		}
		wg.Wait()
	}
}

func TestWithGate(t *testing.T) {
	gate := NewGate()
	ctx := WithGate(context.Background(), gate, "conn-1", func(context.Context) error { return nil })
	info := gateFromContext(ctx)
	if info == nil || info.gate != gate || info.key != "conn-1" {
		t.Fatal("WithGate did not store gate info correctly")
	}
}

func TestGatedBodyReleaseOnEOF(t *testing.T) {
	var released atomic.Int32
	body := &gatedBody{
		ReadCloser: &syncReadCloser{data: []byte("hello")},
		release:    func() { released.Add(1) },
	}
	buf := make([]byte, 16)
	n, err := body.Read(buf)
	if err != nil && err.Error() != "EOF" {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes; got %d", n)
	}
	if released.Load() == 0 && err == nil {
		_, err = body.Read(buf)
		if !errors.Is(err, errEOF) {
			t.Fatalf("expected EOF; got %v", err)
		}
	}
	if released.Load() == 0 {
		t.Fatal("release not called on EOF")
	}
}

func TestGatedBodyReleaseOnClose(t *testing.T) {
	var released atomic.Int32
	body := &gatedBody{
		ReadCloser: &syncReadCloser{data: []byte("hello")},
		release:    func() { released.Add(1) },
	}
	_ = body.Close()
	if released.Load() != 1 {
		t.Fatalf("release not called on Close; released=%d", released.Load())
	}
	_ = body.Close()
	if released.Load() != 1 {
		t.Fatalf("double release detected; released=%d", released.Load())
	}
}

var errEOF = errors.New("EOF")

type syncReadCloser struct {
	data []byte
	pos  int
}

func (r *syncReadCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, errEOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *syncReadCloser) Close() error { return nil }

// TestGateBlockRejectsCancelledContext verifies that Block returns ctx.Err()
// without sealing the key when the context is already cancelled at entry.
func TestGateBlockRejectsCancelledContext(t *testing.T) {
	gate := NewGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := gate.Block(ctx, "conn-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled; got %v", err)
	}
	if len(gate.keys) != 0 {
		t.Fatalf("cancelled Block should not create key state; len=%d", len(gate.keys))
	}
	_, _, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if enterErr != nil {
		t.Fatalf("Enter after cancelled Block should succeed; got %v", enterErr)
	}
}

// TestGateBlockRollsBackAfterDrainIfContextExpired verifies that when the
// context expires during the drain wait, Block rolls back the seal and
// returns ctx.Err() instead of returning a successful unblock.
func TestGateBlockRollsBackAfterDrainIfContextExpired(t *testing.T) {
	gate := NewGate()
	entered := make(chan struct{})
	releaseCh := make(chan struct{})
	_, release, err := gate.Enter(context.Background(), "conn-1", func(context.Context) error {
		close(entered)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-releaseCh
		release()
	}()
	<-entered
	blockCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	unblockCh := make(chan func(), 1)
	blockErrCh := make(chan error, 1)
	go func() {
		unblock, blockErr := gate.Block(blockCtx, "conn-1")
		if blockErr != nil {
			blockErrCh <- blockErr
			return
		}
		unblockCh <- unblock
	}()
	// Release the activity after the block context deadline has expired so
	// the drain completes but ctx.Err() is non-zero.
	<-time.After(80 * time.Millisecond)
	close(releaseCh)
	select {
	case <-unblockCh:
		t.Fatal("Block should not succeed when context expired during drain")
	case err := <-blockErrCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected DeadlineExceeded; got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Block did not return after context expiry and drain")
	}
	// Verify the rollback: Enter should succeed again.
	_, release2, enterErr := gate.Enter(context.Background(), "conn-1", nil)
	if enterErr != nil {
		t.Fatalf("Enter after rollback returned error: %v", enterErr)
	}
	release2()
}
