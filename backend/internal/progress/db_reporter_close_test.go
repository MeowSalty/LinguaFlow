package progress

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestDBReporterCloseJoinsTickerAndRejectsLateWrites(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	r := &DBReporter{
		logger: slog.Default(), ticker: time.NewTicker(time.Millisecond),
		done: make(chan struct{}), tickerExited: make(chan struct{}),
		flushFn: func([]segmentUpdate) error {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return nil
		},
	}
	r.SegmentDone()
	go r.runTicker()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ticker did not start flush")
	}
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	<-r.done
	select {
	case <-closed:
		t.Fatal("Close returned while ticker was still writing")
	default:
	}
	close(release)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.tickerExited:
	default:
		t.Fatal("Close did not join ticker")
	}
	r.SegmentDone()
	r.SegmentResolved(1)
	r.BatchComplete()
	r.StageDone()
	r.SwitchRound(1, func(int) (int, bool) { return 1, true })
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || len(r.pending) != 0 || r.round != nil {
		t.Fatal("closed reporter accepted late producer")
	}
}

func TestDBReporterClosePreservesFinalFlushError(t *testing.T) {
	failure := errors.New("final flush failure")
	r := &DBReporter{
		logger: slog.Default(), ticker: time.NewTicker(time.Hour),
		done: make(chan struct{}), tickerExited: make(chan struct{}),
		flushFn: func([]segmentUpdate) error { return failure },
	}
	go r.runTicker()
	r.SegmentDone()
	if err := r.Close(); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if err := r.Close(); !errors.Is(err, failure) {
		t.Fatalf("second close got %v", err)
	}
	select {
	case <-r.tickerExited:
	default:
		t.Fatal("failed final flush left writer running")
	}
}
