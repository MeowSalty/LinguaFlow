package api

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/go-chi/chi/v5"
)

type streamRecordingConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Time
}

func (c *streamRecordingConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadlines = append(c.deadlines, deadline)
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(deadline)
}

func (c *streamRecordingConn) snapshot() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.deadlines...)
}

type streamRecordingListener struct {
	net.Listener
	accepted chan *streamRecordingConn
}

func (l *streamRecordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	wrapped := &streamRecordingConn{Conn: c}
	l.accepted <- wrapped
	return wrapped, nil
}

func timedStreamRouter(s *Server, jobID int, timing jobStreamTiming, done chan struct{}) http.Handler {
	router := chi.NewRouter()
	s.applyMiddleware(router)
	router.Get("/stream", func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		s.handleJobStreamWithTiming(w, r, jobID, time.Second, timing)
	})
	return router
}

func TestJobStreamHTTPIdleRevocationCompletes(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "http1", true: "http2"}[http2], func(t *testing.T) {
			s, client, account, job := serverStreamFixture(t, 0)
			clock := &streamClock{t: time.Now()}
			timing, ready, ticks := manualStreamTiming(clock)
			done := make(chan struct{})
			server := httptest.NewUnstartedServer(timedStreamRouter(s, job.ID, timing, done))
			listener := &streamRecordingListener{Listener: server.Listener, accepted: make(chan *streamRecordingConn, 1)}
			server.Listener = listener
			server.EnableHTTP2 = http2
			if http2 {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret)))
			clientHTTP := server.Client()
			clientHTTP.Timeout = 10 * time.Second
			response, err := clientHTTP.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 200 || response.ProtoMajor != 1+boolInt(http2) {
				t.Fatalf("response = %d %s", response.StatusCode, response.Proto)
			}
			reader := bufio.NewReader(response.Body)
			line, err := reader.ReadString('\n')
			if err != nil || line != ": connected\n" {
				t.Fatalf("handshake: %q %v", line, err)
			}
			awaitStreamSignal(t, ready)
			conn := <-listener.accepted
			if !http2 {
				before := conn.snapshot()
				if len(before) < 2 || !before[len(before)-1].IsZero() {
					t.Fatalf("idle retained transport deadline: %v", before)
				}
			}
			clock.advance(6 * time.Second)
			client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
			ticks <- clock.now()
			remaining, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("stream did not complete cleanly: %v", err)
			}
			if string(remaining) != "\n" {
				t.Fatalf("data after revocation: %q", remaining)
			}
			awaitStreamSignal(t, done)
			if !http2 {
				wantClose := clock.now().Add(sseCloseBudget)
				found := false
				for _, deadline := range conn.snapshot() {
					if deadline.Equal(wantClose) {
						found = true
					}
				}
				if !found {
					t.Fatalf("protocol close has no independent deadline: %v", conn.snapshot())
				}
			}
		})
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type streamRevokeHTTPWriter struct {
	http.ResponseWriter
	revoke func()
}

func (w *streamRevokeHTTPWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *streamRevokeHTTPWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *streamRevokeHTTPWriter) Flush() { _ = w.FlushError() }
func (w *streamRevokeHTTPWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if strings.Contains(string(data), "id: 1") {
		w.revoke()
	}
	return n, err
}

func TestJobStreamHTTPPendingAbortBlocksConnectionFinalFlush(t *testing.T) {
	s, client, account, job := serverStreamFixture(t, 3)
	// Larger than the response's 2 KiB buffer and smaller than the connection's
	// 4 KiB buffer: Write reaches conn.bufw but has not reached the peer yet.
	client.SSEEvent.Update().SetMessage(strings.Repeat("private", 400)).ExecX(t.Context())
	done := make(chan struct{})
	router := s.newRouter()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		router.ServeHTTP(&streamRevokeHTTPWriter{ResponseWriter: w, revoke: func() {
			client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
		}}, r)
	}))
	listener := &streamRecordingListener{Listener: server.Listener, accepted: make(chan *streamRecordingConn, 1)}
	server.Listener = listener
	server.Start()
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/jobs/"+strconv.Itoa(job.ID)+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret)))
	server.Client().Timeout = 10 * time.Second
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err == nil {
		t.Fatal("pending abort unexpectedly sent normal EOF")
	}
	if string(body) != ": connected\n\n" {
		t.Fatalf("unflushed connection buffer escaped abort: %q", body)
	}
	awaitStreamSignal(t, done)
	conn := <-listener.accepted
	deadlines := conn.snapshot()
	found := false
	for _, deadline := range deadlines {
		if !deadline.IsZero() && deadline.Before(time.Now()) {
			found = true
		}
	}
	if !found {
		t.Fatalf("pending abort did not expire connection writes: %v", deadlines)
	}
}

type expiringHTTP2StreamWriter struct {
	http.ResponseWriter
	clock     *streamClock
	mu        sync.Mutex
	deadlines []time.Time
}

func (w *expiringHTTP2StreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *expiringHTTP2StreamWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *expiringHTTP2StreamWriter) Flush() { _ = w.FlushError() }
func (w *expiringHTTP2StreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	return http.NewResponseController(w.ResponseWriter).SetWriteDeadline(deadline)
}
func (w *expiringHTTP2StreamWriter) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "event:") {
		// Expire the real stream timer and its logical I/O window together.
		if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(-time.Nanosecond)); err != nil {
			return 0, err
		}
		w.clock.advance(sseWriteBudget)
	}
	return w.ResponseWriter.Write(data)
}

// Exercise a real HTTP/2 reset without matching it to HTTP/1 EOF wording.
func TestJobStreamHTTP2ExpiredWriteResets(t *testing.T) {
	s, _, account, job := serverStreamFixture(t, 0)
	clock := &streamClock{t: time.Now()}
	timing, ready, _ := manualStreamTiming(clock)
	done := make(chan struct{})
	writers := make(chan *expiringHTTP2StreamWriter, 1)
	router := timedStreamRouter(s, job.ID, timing, done)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped := &expiringHTTP2StreamWriter{ResponseWriter: w, clock: clock}
		writers <- wrapped
		router.ServeHTTP(wrapped, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret)))
	server.Client().Timeout = 10 * time.Second
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	awaitStreamSignal(t, ready)
	s.eventBroker.Publish(job.ID, event.Event{JobID: job.ID, Seq: 1, Type: "translation", Message: "private"})
	remaining, err := io.ReadAll(response.Body)
	if err == nil {
		t.Fatal("expired HTTP/2 stream completed instead of resetting")
	}
	if string(remaining) != ": connected\n\n" {
		t.Fatalf("HTTP/2 data: %q", remaining)
	}
	awaitStreamSignal(t, done)
	w := <-writers
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.deadlines) != 3 || w.deadlines[2].IsZero() {
		t.Fatalf("reset stream deadline was recovered: %v", w.deadlines)
	}
}
