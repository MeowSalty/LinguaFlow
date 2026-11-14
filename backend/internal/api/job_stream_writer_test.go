package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/golang-jwt/jwt/v5"
)

func streamPanic(fn func()) (rec any) {
	defer func() { rec = recover() }()
	fn()
	return nil
}

func assertStreamAbort(t *testing.T, fn func()) {
	t.Helper()
	if rec := streamPanic(fn); rec != http.ErrAbortHandler {
		t.Fatalf("stream panic = %v, want ErrAbortHandler", rec)
	}
}

type streamClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *streamClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *streamClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type streamIO struct {
	kind     string
	deadline time.Time
	data     string
}

// Models the deadline contract even when the logical clock is advanced without
// wall-clock sleeps. Channels can hold a Write at its actual I/O boundary.
type controlledStreamWriter struct {
	*safeRecorder
	clock     *streamClock
	mu        sync.Mutex
	deadline  time.Time
	trace     []streamIO
	failAt    int
	short     bool
	onWrite   func([]byte)
	onFlush   func()
	completed chan struct{}
}

func newControlledStreamWriter(clock *streamClock) *controlledStreamWriter {
	return &controlledStreamWriter{safeRecorder: newSafeRecorder(), clock: clock, completed: make(chan struct{}, 32)}
}

func (w *controlledStreamWriter) record(kind, data string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trace = append(w.trace, streamIO{kind, w.deadline, data})
	if w.failAt == len(w.trace) || (kind != "deadline" && kind != "clear" && !w.deadline.IsZero() && !w.clock.now().Before(w.deadline)) {
		return os.ErrDeadlineExceeded
	}
	return nil
}

func (w *controlledStreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadline = deadline
	w.mu.Unlock()
	kind := "deadline"
	if deadline.IsZero() {
		kind = "clear"
	}
	err := w.record(kind, "")
	if err == nil && kind == "clear" {
		w.completed <- struct{}{}
	}
	return err
}

func (w *controlledStreamWriter) WriteHeader(status int) {
	if err := w.record("header", ""); err != nil {
		panic(err)
	}
	w.safeRecorder.WriteHeader(status)
}

func (w *controlledStreamWriter) Write(data []byte) (int, error) {
	if err := w.record("write", string(data)); err != nil {
		return 0, err
	}
	if w.onWrite != nil {
		w.onWrite(data)
	}
	w.mu.Lock()
	expired := !w.deadline.IsZero() && !w.clock.now().Before(w.deadline)
	w.mu.Unlock()
	if expired {
		return 0, os.ErrDeadlineExceeded
	}
	if w.short {
		return len(data) - 1, nil
	}
	return w.safeRecorder.Write(data)
}

func (w *controlledStreamWriter) FlushError() error {
	if err := w.record("flush", ""); err != nil {
		return err
	}
	w.safeRecorder.Flush()
	if w.onFlush != nil {
		w.onFlush()
	}
	return nil
}

func (w *controlledStreamWriter) snapshot() []streamIO {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]streamIO(nil), w.trace...)
}

func testJobStreamWriter(w *controlledStreamWriter, expiry time.Time) *jobStreamWriter {
	return &jobStreamWriter{w: w, controller: http.NewResponseController(w), now: w.clock.now, expiresAt: expiry}
}

func TestJobStreamWriterFailureDoesNotRecover(t *testing.T) {
	for _, tc := range []struct {
		name           string
		failAt         int
		short, expired bool
	}{
		{name: "set deadline", failAt: 1},
		{name: "write", failAt: 3},
		{name: "short write", short: true},
		{name: "flush", failAt: 4},
		{name: "clear deadline", failAt: 5},
		{name: "expired after write", expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &streamClock{t: time.Now()}
			w := newControlledStreamWriter(clock)
			w.failAt, w.short = tc.failAt, tc.short
			if tc.expired {
				w.onWrite = func([]byte) { clock.advance(sseWriteBudget) }
			}
			stream := testJobStreamWriter(w, time.Time{})
			assertStreamAbort(t, func() {
				defer stream.finish()
				if stream.writeAndFlush([]byte(": connected\n\n"), true) == nil {
					t.Fatal("failed I/O succeeded")
				}
				if stream.writeAndFlush([]byte("unexpected"), false) == nil {
					t.Fatal("failed stream resumed")
				}
			})
			wantAttempts := tc.failAt
			if wantAttempts == 0 {
				wantAttempts = 3
			}
			if trace := w.snapshot(); len(trace) != wantAttempts {
				t.Fatalf("I/O after failure: %+v", trace)
			}
		})
	}
}

func TestJobStreamWriterExpiredSessionNeverStarts(t *testing.T) {
	clock := &streamClock{t: time.Now()}
	w := newControlledStreamWriter(clock)
	stream := testJobStreamWriter(w, clock.now())
	assertStreamAbort(t, func() {
		defer stream.finish()
		stream.writeAndFlush([]byte(": connected\n\n"), true)
	})
	if trace := w.snapshot(); len(trace) != 0 {
		t.Fatalf("expired session did I/O: %+v", trace)
	}
}

func TestJobStreamWriterSuccessfulFlushAtDeadlineDoesNotReset(t *testing.T) {
	clock := &streamClock{t: time.Now()}
	w := newControlledStreamWriter(clock)
	w.onFlush = func() { clock.advance(sseWriteBudget) }
	stream := testJobStreamWriter(w, time.Time{})
	assertStreamAbort(t, func() {
		defer stream.finish()
		if stream.writeAndFlush([]byte(": connected\n\n"), true) == nil {
			t.Fatal("expired Flush succeeded")
		}
	})
	trace := w.snapshot()
	if len(trace) != 4 || trace[3].kind != "flush" {
		t.Fatalf("expired deadline was reset: %+v", trace)
	}
}

func TestJobStreamWriterLocalExpiryWithPendingAbortsConnectionBuffer(t *testing.T) {
	clock := &streamClock{t: time.Now()}
	w := newControlledStreamWriter(clock)
	stream := testJobStreamWriter(w, clock.now().Add(time.Second))
	assertStreamAbort(t, func() {
		defer stream.finish()
		if stream.writeAndFlush([]byte(": connected\n\n"), true) != nil || !stream.writeEvent([]byte("id: 1\n\n")) {
			t.Fatal("initial writes failed")
		}
		clock.advance(time.Second)
		if stream.writeEvent([]byte("id: 2\n\n")) {
			t.Fatal("expired session resumed")
		}
	})
	trace := w.snapshot()
	last := trace[len(trace)-1]
	if len(trace) != 9 || last.kind != "deadline" || !last.deadline.Before(clock.now()) || strings.Contains(w.String(), "id: 2") {
		t.Fatalf("local expiry allowed pending connection buffer to escape: %+v", trace)
	}
}

func TestJobStreamWriterPendingAndPanicAbort(t *testing.T) {
	for _, cause := range []string{"pending", "panic", "header panic"} {
		t.Run(cause, func(t *testing.T) {
			clock := &streamClock{t: time.Now()}
			w := newControlledStreamWriter(clock)
			stream := testJobStreamWriter(w, time.Time{})
			if cause == "header panic" {
				w.failAt = 2
			}
			assertStreamAbort(t, func() {
				defer stream.finish()
				if stream.writeAndFlush([]byte(": connected\n\n"), true) != nil {
					t.Fatal("handshake")
				}
				if cause == "pending" {
					if !stream.writeEvent([]byte("id: 1\n\n")) {
						t.Fatal("event")
					}
					return
				}
				panic("unexpected")
			})
			trace := w.snapshot()
			last := trace[len(trace)-1]
			if last.kind != "deadline" || !last.deadline.Before(clock.now()) {
				t.Fatalf("abort did not expire connection buffer deadline: %+v", trace)
			}
		})
	}
}

func awaitStreamSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("stream signal timed out")
	}
}

func awaitStreamResult(t *testing.T, done <-chan any) any {
	t.Helper()
	select {
	case rec := <-done:
		return rec
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not stop")
		return nil
	}
}

func manualStreamTiming(clock *streamClock) (jobStreamTiming, chan struct{}, chan time.Time) {
	ready, ticks := make(chan struct{}), make(chan time.Time, 1)
	return jobStreamTiming{now: clock.now, newTicker: func(time.Duration) jobStreamTicker {
		close(ready)
		return jobStreamTicker{ch: ticks, stop: func() {}}
	}}, ready, ticks
}

func TestJobStreamIOWindowsAndIdleRevocation(t *testing.T) {
	s, client, account, job := serverStreamFixture(t, 3)
	s.sseReplayBatch = 2
	// Seed through the store so Publish's assigned live sequence follows replay.
	// Direct DB seeding after HybridStore creation does not advance its counter.
	store := event.NewRingBufferStore(event.RingBufferConfig{Capacity: 16})
	for range 3 {
		if _, err := store.Append(job.ID, event.Event{JobID: job.ID, Type: "translation", Message: "history"}); err != nil {
			t.Fatal(err)
		}
	}
	s.eventBroker = event.NewBroker(store)
	clock := &streamClock{t: time.Now()}
	expiresAt := clock.now().Add(time.Second)
	claims := &service.AccessTokenClaims{UserID: account.ID, AuthVersion: account.AuthVersion,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: &jwt.NumericDate{Time: expiresAt}}}
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(context.WithValue(t.Context(), authContextKey{}, authenticatedUser{User: account, Claims: claims}))
	w := newControlledStreamWriter(clock)
	w.onWrite = func(data []byte) {
		if strings.Contains(string(data), ": connected") {
			claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		}
	}
	timing, ready, ticks := manualStreamTiming(clock)
	done := make(chan any, 1)
	go func() { done <- streamPanic(func() { s.handleJobStreamWithTiming(w, r, job.ID, time.Second, timing) }) }()
	awaitStreamSignal(t, ready)
	for range 6 {
		awaitStreamSignal(t, w.completed)
	}
	// Both heartbeat and live use the same combined budget as the handshake.
	ticks <- clock.now()
	awaitStreamSignal(t, w.completed)
	s.eventBroker.Publish(job.ID, event.Event{JobID: job.ID, Seq: 4, Type: "translation", Message: "live"})
	awaitStreamSignal(t, w.completed)
	trace := w.snapshot()
	var kinds []string
	for _, op := range trace {
		kinds = append(kinds, op.kind)
		if op.kind != "clear" && !op.deadline.Equal(expiresAt) {
			t.Fatalf("session expiry was extended: %+v", op)
		}
	}
	want := []string{"deadline", "header", "write", "flush", "clear",
		"deadline", "write", "clear", "deadline", "write", "clear", "deadline", "flush", "clear",
		"deadline", "write", "clear", "deadline", "flush", "clear",
		"deadline", "write", "flush", "clear", "deadline", "write", "flush", "clear"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("I/O windows or page flush changed: %v", kinds)
	}
	clock.advance(6 * time.Second)
	client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
	ticks <- clock.now()
	if rec := awaitStreamResult(t, done); rec != nil {
		t.Fatalf("healthy idle close aborted: %v", rec)
	}
	closed := w.snapshot()
	if len(closed) != len(trace)+1 || !closed[len(trace)].deadline.Equal(clock.now().Add(sseCloseBudget)) {
		t.Fatalf("idle retained business deadline or close used expired session: %+v", closed)
	}
	if ids := parseIDLines(w.String()); !reflect.DeepEqual(ids, []int64{1, 2, 3, 4}) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestJobStreamBlockedWriteKeepsDeadlineDuringRevocation(t *testing.T) {
	for _, lifetime := range []time.Duration{time.Hour, time.Second} {
		t.Run(lifetime.String(), func(t *testing.T) {
			s, client, account, job := serverStreamFixture(t, 1)
			clock := &streamClock{t: time.Now()}
			claims := &service.AccessTokenClaims{UserID: account.ID, AuthVersion: account.AuthVersion,
				RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: &jwt.NumericDate{Time: clock.now().Add(lifetime)}}}
			r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(context.WithValue(t.Context(), authContextKey{}, authenticatedUser{User: account, Claims: claims}))
			w := newControlledStreamWriter(clock)
			entered, release := make(chan struct{}), make(chan struct{})
			w.onWrite = func(data []byte) {
				if strings.Contains(string(data), "id: 1") {
					close(entered)
					<-release
				}
			}
			done := make(chan any, 1)
			go func() {
				done <- streamPanic(func() { s.handleJobStreamWithTiming(w, r, job.ID, time.Second, jobStreamTiming{now: clock.now}) })
			}()
			awaitStreamSignal(t, entered)
			trace := w.snapshot()
			deadline := trace[len(trace)-1].deadline
			budget := min(lifetime, sseWriteBudget)
			if !deadline.Equal(clock.now().Add(budget)) {
				t.Fatalf("blocked Write deadline = %v", deadline)
			}
			client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
			clock.advance(budget)
			close(release)
			if rec := awaitStreamResult(t, done); rec != http.ErrAbortHandler {
				t.Fatalf("blocked stream = %v", rec)
			}
			if after := w.snapshot(); len(after) != len(trace) {
				t.Fatalf("recovered after expired Write: %+v", after)
			}
		})
	}
}

func TestJobStreamReplayPageChecksAccessBeforeFlush(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "revocation at page end", true: "canceled within page"}[cancelRequest], func(t *testing.T) {
			s, client, account, job := serverStreamFixture(t, 3)
			s.sseReplayBatch = 2
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := streamBearerRequest(t, account, time.Now().Add(time.Hour)).WithContext(ctx)
			w := newControlledStreamWriter(&streamClock{t: time.Now()})
			w.onWrite = func(data []byte) {
				if strings.Contains(string(data), "id: 2") {
					if cancelRequest {
						cancel()
					} else {
						client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
					}
				}
			}
			assertStreamAbort(t, func() {
				s.handleJobStreamWithTiming(w, r, job.ID, time.Second, jobStreamTiming{now: w.clock.now})
			})
			flushes := 0
			for _, op := range w.snapshot() {
				if op.kind == "flush" {
					flushes++
				}
			}
			if flushes != 1 || !reflect.DeepEqual(parseIDLines(w.String()), []int64{1, 2}) {
				t.Fatalf("page was flushed after access failed: flushes=%d body=%q", flushes, w.String())
			}
		})
	}
}
