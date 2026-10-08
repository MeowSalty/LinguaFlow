package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestDispatcherRecoveryBarrierPrecedesAllConsumers(t *testing.T) {
	a, b := newDispatcherTestRunner("translation", 1), newDispatcherTestRunner("sync", 1)
	a.add(1)
	b.add(2)
	var prepared atomic.Int32
	a.prepare = func(context.Context) error { prepared.Add(1); return nil }
	b.prepare = func(context.Context) error { prepared.Add(1); return nil }
	entered, release := make(chan struct{}), make(chan struct{})
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, a, b)
	d.SetRecoveryBarrier(func(ctx context.Context) error {
		if prepared.Load() != 2 {
			t.Error("barrier ran before all recovery preparation")
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	runTestDispatcher(t, d)
	<-entered
	for _, runner := range []*dispatcherTestRunner{a, b} {
		select {
		case <-runner.pages:
			t.Fatal("discovery preceded barrier")
		default:
		}
		select {
		case <-runner.processed:
			t.Fatal("worker preceded barrier")
		default:
		}
	}
	close(release)
	if receiveWorker(t, a.processed) != 1 || receiveWorker(t, b.processed) != 2 {
		t.Fatal("prepared work did not run")
	}
}

func TestDispatcherRecoveryBarrierFailureAndShutdown(t *testing.T) {
	r := newDispatcherTestRunner("translation", 1)
	r.add(1)
	called := make(chan struct{}, 1)
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	d.SetRecoveryBarrier(func(context.Context) error { called <- struct{}{}; return errors.New("migration unavailable") })
	runTestDispatcher(t, d)
	<-called
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.processed:
		t.Fatal("failed barrier released worker")
	default:
	}
}
