package progress

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type reporterBudgetSaver func(context.Context, persistence.Metadata, func(context.Context) error) error

func (s reporterBudgetSaver) Save(ctx context.Context, meta persistence.Metadata, fn func(context.Context) error) error {
	return s(ctx, meta, fn)
}

func TestDBReporterUsesActualResourceForPersistenceMetadata(t *testing.T) {
	var metadata []persistence.Metadata
	r := NewDBReporter(DBReporterOptions{JobID: 7, JobResourceID: 81, ResourceID: 23, Ticker: time.Hour, Saver: reporterBudgetSaver(func(_ context.Context, m persistence.Metadata, _ func(context.Context) error) error {
		metadata = append(metadata, m)
		return nil
	})})
	r.StageStart("translate", 1)
	if err := r.FlushCheckpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if r.jobResourceID != 81 {
		t.Fatal("observation change modified business identity")
	}
	for _, m := range metadata {
		if m.ResourceID != 23 || m.JobID != 7 {
			t.Fatalf("wrong persistence resource association: %+v", m)
		}
	}
	if len(metadata) < 2 {
		t.Fatal("stage/checkpoint did not use saving boundary")
	}
}

func TestDBReporterCloseKeepsDirtyCheckpointHandoffDeadline(t *testing.T) {
	r, _ := newTestReporter(time.Hour)
	failure := errors.New("checkpoint blocked")
	var budgets []time.Time
	r.saver = reporterBudgetSaver(func(ctx context.Context, _ persistence.Metadata, _ func(context.Context) error) error {
		budgets = append(budgets, persistence.Deadline(ctx))
		return failure
	})
	r.SegmentDone()
	deadline := time.Now().Add(time.Second)
	if err := r.FlushCheckpoint(persistence.WithDeadline(context.Background(), deadline)); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := r.Close(); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(budgets) != 2 || !budgets[0].Equal(deadline) || !budgets[1].Equal(deadline) {
		t.Fatalf("close restarted dirty response handoff budget: %v", budgets)
	}
	if len(r.pending) != 1 {
		t.Fatal("failed checkpoint discarded dirty completion")
	}
}

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
