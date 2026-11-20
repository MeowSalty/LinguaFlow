package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/net/http2"
)

// Recover in the goroutine that calls the handler, just as net/http does.
// A started stream may convert an unexpected panic into ErrAbortHandler, so
// handler tests additionally require the absence of its panic log.
func streamTermination(fn func()) (rec any) {
	defer func() {
		rec = recover()
		if rec != nil && rec != http.ErrAbortHandler {
			panic(rec)
		}
	}()
	fn()
	return nil
}

func TestStreamTerminationAcceptsOnlyHTTPAbort(t *testing.T) {
	if rec := streamTermination(func() {}); rec != nil {
		t.Fatalf("normal return = %v", rec)
	}
	if rec := streamTermination(func() { panic(http.ErrAbortHandler) }); rec != http.ErrAbortHandler {
		t.Fatalf("abort = %v", rec)
	}
	unexpected := errors.New("unexpected test panic")
	if rec := streamPanic(func() { streamTermination(func() { panic(unexpected) }) }); rec != unexpected {
		t.Fatalf("unexpected panic was replaced or swallowed: %v", rec)
	}
}

func captureStreamLogs(s *Server) *bytes.Buffer {
	logs := new(bytes.Buffer)
	// Each fixture owns its Server. Install before starting its goroutines and
	// read only after their outer completion signal, including middleware logs.
	s.logger = slog.New(slog.NewJSONHandler(logs, nil))
	return logs
}

func streamHandlerPanicCount(t *testing.T, logs *bytes.Buffer) int {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	count := 0
	for {
		var record struct {
			Message string `json:"msg"`
		}
		if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
			return count
		} else if err != nil {
			t.Fatalf("invalid fixture log: %v", err)
		}
		if record.Message == "job_stream: handler panic" {
			count++
		}
	}
}

func assertNoStreamHandlerPanic(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	if count := streamHandlerPanicCount(t, logs); count != 0 {
		t.Fatalf("stream converted %d unexpected handler panic(s) into abort", count)
	}
}

type expiryHandshakeWriter struct {
	*revokingStreamWriter
	firstFlush time.Time
}

func (w *expiryHandshakeWriter) FlushError() error {
	w.safeRecorder.Flush()
	if w.firstFlush.IsZero() {
		w.firstFlush = time.Now()
	}
	return nil
}

func (w *expiryHandshakeWriter) Flush() { _ = w.FlushError() }

type expiryQueryObservation struct {
	at  time.Time
	ctx context.Context
	err error
}

type expiryQueryBarrier struct {
	armed   atomic.Bool
	entered chan expiryQueryObservation
	release chan struct{}
	once    sync.Once
}

func newExpiryQueryBarrier(client *ent.Client) *expiryQueryBarrier {
	b := &expiryQueryBarrier{entered: make(chan expiryQueryObservation, 1), release: make(chan struct{})}
	client.Job.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			value, err := next.Query(ctx, query)
			if !b.armed.CompareAndSwap(true, false) {
				return value, err
			}
			b.entered <- expiryQueryObservation{time.Now(), ctx, err}
			if err != nil {
				return value, err
			}
			select {
			case <-b.release:
				return value, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}))
	return b
}

func (b *expiryQueryBarrier) unblock() { b.once.Do(func() { close(b.release) }) }

func (b *expiryQueryBarrier) await(t *testing.T, expiresAt time.Time) expiryQueryObservation {
	t.Helper()
	select {
	case observation := <-b.entered:
		if observation.err != nil || observation.ctx.Err() != nil || !observation.at.Before(expiresAt) {
			t.Fatalf("query did not succeed before expiry: query=%v context=%v at=%v expiry=%v", observation.err, observation.ctx.Err(), observation.at, expiresAt)
		}
		return observation
	case <-time.After(3 * time.Second):
		t.Fatal("permission query did not reach the expiry barrier")
		return expiryQueryObservation{}
	}
}

func awaitActualStreamExpiry(t *testing.T, ctx context.Context, expiresAt time.Time) {
	t.Helper()
	timer := time.NewTimer(time.Until(expiresAt))
	defer timer.Stop()
	select {
	case <-timer.C:
		if time.Now().Before(expiresAt) || ctx.Err() != nil {
			t.Fatalf("expiry barrier used early/canceled time: expiry=%v context=%v", expiresAt, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatalf("request canceled before natural expiry: %v", ctx.Err())
	}
}

func startExpiryStream(t *testing.T, cancel context.CancelFunc, unblock func(), fn func()) (<-chan any, <-chan struct{}) {
	t.Helper()
	done, joined := make(chan any, 1), make(chan struct{})
	t.Cleanup(func() {
		cancel()
		if unblock != nil {
			unblock()
		}
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			t.Error("expiry fixture handler did not join during cleanup")
		}
	})
	go func() {
		defer close(joined)
		done <- streamTermination(fn)
	}()
	return done, joined
}

func observableExpiryTiming(clock *streamClock) (jobStreamTiming, <-chan struct{}, chan time.Time, <-chan struct{}) {
	timing, ready, ticks := manualStreamTiming(clock)
	stopped := make(chan struct{})
	newTicker := timing.newTicker
	timing.newTicker = func(interval time.Duration) jobStreamTicker {
		ticker := newTicker(interval)
		ticker.stop = func() { close(stopped) }
		return ticker
	}
	return timing, ready, ticks, stopped
}

func testJobStreamControlledSessionExpiry(t *testing.T, abort bool) {
	s, client, account, job := serverStreamFixture(t, 0)
	s.eventBroker = event.NewBroker(nil)
	logs := captureStreamLogs(s)
	barrier := newExpiryQueryBarrier(client)
	ctx, cancel := context.WithCancel(t.Context())
	clock := &streamClock{t: time.Now()}
	w := newControlledStreamWriter(clock)
	claims := &service.AccessTokenClaims{UserID: account.ID, AuthVersion: account.AuthVersion}
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(context.WithValue(ctx, authContextKey{}, authenticatedUser{User: account, Claims: claims}))
	w.onWrite = func(data []byte) {
		if strings.HasPrefix(string(data), ": connected") {
			claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
		}
	}
	timing, ready, ticks, stopped := observableExpiryTiming(clock)
	expiresAt := time.Now().Add(time.Second)
	claims.ExpiresAt = &jwt.NumericDate{Time: expiresAt}
	done, joined := startExpiryStream(t, cancel, barrier.unblock, func() {
		s.handleJobStreamWithTiming(w, r, job.ID, time.Second, timing)
	})
	awaitStreamSignal(t, ready)
	before := w.snapshot()
	if !time.Now().Before(expiresAt) || w.String() != ": connected\n\n" || len(before) != 5 || before[3].kind != "flush" || before[4].kind != "clear" {
		t.Fatalf("stream was not ready before original expiry: %+v body=%q", before, w.String())
	}
	if !before[0].deadline.Equal(expiresAt) {
		t.Fatalf("handshake did not retain the original expiry: %+v", before)
	}
	if abort {
		barrier.armed.Store(true)
		s.eventBroker.Publish(job.ID, event.Event{JobID: job.ID, Seq: 1, Type: "translation", Message: "private"})
		observation := barrier.await(t, expiresAt)
		// Job.Only succeeded after session authentication. CheckJobAccess also
		// reads Project after this barrier; cancellation must not replace success.
		awaitActualStreamExpiry(t, observation.ctx, expiresAt)
		clock.advance(expiresAt.Sub(clock.now()))
		barrier.unblock()
	} else {
		awaitActualStreamExpiry(t, ctx, expiresAt)
		clock.advance(expiresAt.Sub(clock.now()))
		ticks <- clock.now()
	}
	rec := awaitStreamResult(t, done)
	awaitStreamSignal(t, joined)
	awaitStreamSignal(t, stopped)
	if abort && rec != http.ErrAbortHandler || !abort && rec != nil || ctx.Err() != nil {
		t.Fatalf("natural expiry returned %v (abort=%t), context=%v", rec, abort, ctx.Err())
	}
	after := w.snapshot()
	if len(after) != len(before)+1 || after[len(before)].kind != "deadline" {
		t.Fatalf("new business I/O after expiry: %+v", after)
	}
	last := after[len(before)].deadline
	if abort && !last.Before(clock.now()) || !abort && !last.Equal(clock.now().Add(sseCloseBudget)) {
		t.Fatalf("wrong expiry cleanup deadline: %v", last)
	}
	if countIDLines(w.String()) != 0 || strings.Contains(w.String(), ": keepalive") || !claims.ExpiresAt.Time.After(expiresAt.Add(time.Minute)) {
		t.Fatalf("stream renewed or sent data after expiry: %q", w.String())
	}
	assertNoStreamHandlerPanic(t, logs)
}

type expiryHTTPWriter struct {
	http.ResponseWriter
	mu      sync.Mutex
	trace   []streamIO
	onWrite func([]byte)
}

func (w *expiryHTTPWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *expiryHTTPWriter) record(kind, data string, deadline time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trace = append(w.trace, streamIO{kind: kind, data: data, deadline: deadline})
}
func (w *expiryHTTPWriter) SetWriteDeadline(deadline time.Time) error {
	kind := "deadline"
	if deadline.IsZero() {
		kind = "clear"
	}
	w.record(kind, "", deadline)
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}
func (w *expiryHTTPWriter) Write(data []byte) (int, error) {
	w.record("write", string(data), time.Time{})
	n, err := w.ResponseWriter.Write(data)
	if err == nil && w.onWrite != nil {
		w.onWrite(data)
	}
	return n, err
}
func (w *expiryHTTPWriter) FlushError() error {
	w.record("flush", "", time.Time{})
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *expiryHTTPWriter) Flush() { _ = w.FlushError() }
func (w *expiryHTTPWriter) snapshot() []streamIO {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]streamIO(nil), w.trace...)
}

func TestJobStreamHTTPWriterSessionExpiry(t *testing.T) {
	for _, protocol := range []string{"http1", "http2"} {
		t.Run(protocol, func(t *testing.T) {
			for _, path := range []string{"healthy_return", "write_boundary_abort", "pending_abort"} {
				if protocol == "http2" && path == "pending_abort" {
					continue // HTTP/1 connection finalFlush is a distinct buffer boundary.
				}
				t.Run(path, func(t *testing.T) { testHTTPStreamSessionExpiry(t, protocol == "http2", path) })
			}
		})
	}
}

func testHTTPStreamSessionExpiry(t *testing.T, useHTTP2 bool, path string) {
	pending := path == "pending_abort"
	eventCount := 0
	if pending {
		eventCount = 3
	}
	s, client, account, job := serverStreamFixture(t, eventCount)
	if pending {
		// Reach HTTP/1's 4 KiB connection buffer, but not the peer: the response
		// buffer is 2 KiB and this first event is between the two capacities.
		client.SSEEvent.Update().SetMessage(strings.Repeat("private", 400)).ExecX(t.Context())
	} else {
		s.eventBroker = event.NewBroker(nil)
	}
	logs := captureStreamLogs(s)
	barrier := newExpiryQueryBarrier(client)
	clock := &streamClock{t: time.Now()}
	timing, ready, ticks, stopped := observableExpiryTiming(clock)
	innerDone, done := make(chan struct{}), make(chan struct{})
	pendingWrite, pendingRelease := make(chan struct{}), make(chan struct{})
	var pendingReleaseOnce sync.Once
	releasePendingWrite := func() { pendingReleaseOnce.Do(func() { close(pendingRelease) }) }
	requests := make(chan *http.Request, 1)
	writers := make(chan *expiryHTTPWriter, 1)
	var started atomic.Bool
	router := timedStreamRouter(s, job.ID, timing, innerDone)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started.Store(true)
		defer close(done) // Outside the complete middleware stack and stream defers.
		requests <- r
		wrapped := &expiryHTTPWriter{ResponseWriter: w}
		if pending {
			wrapped.onWrite = func(data []byte) {
				if strings.HasPrefix(string(data), "id: 1\n") {
					close(pendingWrite)
					select {
					case <-pendingRelease:
					case <-r.Context().Done():
					}
					barrier.armed.Store(true)
				}
			}
		}
		writers <- wrapped
		router.ServeHTTP(wrapped, r)
	}))
	server.EnableHTTP2 = useHTTP2
	if useHTTP2 {
		server.StartTLS()
	} else {
		server.Start()
	}
	ctx, cancel := context.WithCancel(t.Context())
	clientHTTP := server.Client()
	var resetCount atomic.Int32
	if useHTTP2 {
		base := clientHTTP.Transport.(*http.Transport)
		clientHTTP.Transport = &http2.Transport{TLSClientConfig: base.TLSClientConfig.Clone(), CountError: func(kind string) {
			if kind == "recv_rststream_INTERNAL_ERROR" {
				resetCount.Add(1)
			}
		}}
	}
	clientHTTP.Timeout = 10 * time.Second
	var response *http.Response
	t.Cleanup(func() {
		cancel()
		releasePendingWrite()
		barrier.unblock()
		if response != nil {
			_ = response.Body.Close()
		}
		if started.Load() {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("HTTP expiry fixture handler did not join during cleanup")
			}
		}
		clientHTTP.CloseIdleConnections()
		server.Close()
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	// NumericDate serializes at the JWT library's current precision. Use the
	// signed token's actual exp, without mutating global jwt.TimePrecision.
	token := authTestToken(t, account, time.Now().Add(3*time.Second), []byte(authTestSecret))
	claims := new(service.AccessTokenClaims)
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil || claims.ExpiresAt == nil {
		t.Fatalf("fixture token expiry unavailable: %v", err)
	}
	expiresAt := claims.ExpiresAt.Time
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = clientHTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.ProtoMajor != 1+boolInt(useHTTP2) || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response = %d %s %q", response.StatusCode, response.Proto, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	for _, expected := range []string{": connected\n", "\n"} {
		if line, err := reader.ReadString('\n'); err != nil || line != expected {
			t.Fatalf("stream handshake: %q %v", line, err)
		}
	}
	serverRequest, writer := <-requests, <-writers
	if !time.Now().Before(expiresAt) || serverRequest.Context().Err() != nil {
		t.Fatalf("HTTP stream did not establish before expiry: context=%v", serverRequest.Context().Err())
	}
	if !pending {
		awaitStreamSignal(t, ready)
	}
	var observation expiryQueryObservation
	if path != "healthy_return" {
		if pending {
			awaitStreamSignal(t, pendingWrite)
		}
		// Keep token establishment separate from the 2s authorization query
		// budget. Enter the successful Job-query barrier only in its final
		// 750ms, rather than letting that context timeout impersonate expiry.
		awaitActualStreamExpiry(t, serverRequest.Context(), expiresAt.Add(-750*time.Millisecond))
		if pending {
			releasePendingWrite()
		}
		if !pending {
			barrier.armed.Store(true)
			s.eventBroker.Publish(job.ID, event.Event{JobID: job.ID, Seq: 1, Type: "translation", Message: "private"})
		}
		observation = barrier.await(t, expiresAt)
	}
	before := writer.snapshot()
	if path == "healthy_return" {
		awaitActualStreamExpiry(t, serverRequest.Context(), expiresAt)
	} else {
		awaitActualStreamExpiry(t, observation.ctx, expiresAt)
	}
	clock.advance(expiresAt.Sub(clock.now()))
	if path == "healthy_return" {
		ticks <- clock.now()
	} else {
		barrier.unblock()
	}
	body, readErr := io.ReadAll(reader)
	awaitStreamSignal(t, done)
	if !pending {
		awaitStreamSignal(t, stopped)
	}
	if len(body) != 0 || ctx.Err() != nil {
		t.Fatalf("post-expiry data or fixture cancellation: data=%q context=%v", body, ctx.Err())
	}
	if path == "healthy_return" {
		if readErr != nil {
			t.Fatalf("healthy expiry did not complete: %v", readErr)
		}
	} else if useHTTP2 {
		var streamErr http2.StreamError
		if !errors.As(readErr, &streamErr) || streamErr.StreamID != 1 || streamErr.Code != http2.ErrCodeInternal || resetCount.Load() != 1 {
			t.Fatalf("expiry did not reset the first HTTP/2 stream: error=%v resets=%d", readErr, resetCount.Load())
		}
	} else if !errors.Is(readErr, io.ErrUnexpectedEOF) {
		t.Fatalf("HTTP/1 expiry abort error = %v, want unexpected EOF", readErr)
	}
	after := writer.snapshot()
	if len(after) != len(before)+1 || after[len(before)].kind != "deadline" {
		t.Fatalf("business I/O resumed after the expiry barrier: before=%+v after=%+v", before, after)
	}
	last := after[len(before)].deadline
	if path == "healthy_return" && !last.Equal(clock.now().Add(sseCloseBudget)) || path != "healthy_return" && !last.Before(clock.now()) {
		t.Fatalf("wrong HTTP expiry cleanup deadline: %v", last)
	}
	assertNoStreamHandlerPanic(t, logs)
}
