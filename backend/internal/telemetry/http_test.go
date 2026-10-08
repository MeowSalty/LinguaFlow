package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type observedBody struct {
	reader io.Reader
	closed chan struct{}
	once   sync.Once
	closes atomic.Int64
}

func (b *observedBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *observedBody) Close() error {
	b.closes.Add(1)
	b.once.Do(func() { close(b.closed) })
	return nil
}

func requestMetrics(c *Collector) ExternalRequestSnapshot { return c.Snapshot().ExternalRequests[0] }
func assertCounters(t *testing.T, c *Collector, total, inflight int64, outcome string) {
	t.Helper()
	s := requestMetrics(c)
	if s.Total != total || s.Inflight != inflight {
		t.Fatalf("counts=%+v, want total %d inflight %d", s, total, inflight)
	}
	var finished int64
	for _, o := range s.Outcomes {
		finished += o.Finished
		if o.DurationCount != o.Finished {
			t.Fatalf("duration mismatch: %+v", o)
		}
		if o.Outcome == outcome && o.Finished != total-inflight {
			t.Fatalf("outcome=%+v", o)
		}
	}
	if s.Total != finished+s.Inflight {
		t.Fatalf("inconsistent snapshot: %+v", s)
	}
}

func TestHTTPBodyLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    io.Reader
		outcome string
	}{
		{"success", 200, strings.NewReader("ok"), "success"},
		{"http_error", 429, strings.NewReader("retry"), "http_error"},
		{"empty_200", 200, strings.NewReader(""), "success"},
		{"read_error", 200, errorReader{}, "transport_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCollector()
			body := &observedBody{reader: tc.body, closed: make(chan struct{})}
			f := NewHTTPClients(c, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
			}))
			defer f.Shutdown()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
			resp, err := f.Client("openai", "generate").Do(req)
			if err != nil {
				t.Fatal(err)
			}
			assertCounters(t, c, 1, 1, "")
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			_ = resp.Body.Close()
			cancel()
			assertCounters(t, c, 1, 0, tc.outcome)
			if body.closes.Load() != 1 {
				t.Fatalf("underlying body closed %d times", body.closes.Load())
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestHTTPCancelAndShutdownReleaseUnconsumedBody(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "request_cancel", true: "server_shutdown"}[shutdown], func(t *testing.T) {
			c := NewCollector()
			body := &observedBody{reader: strings.NewReader("unread"), closed: make(chan struct{})}
			f := NewHTTPClients(c, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
			}))
			defer f.Shutdown()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
			resp, err := f.Client("openai", "generate").Do(req)
			if err != nil {
				t.Fatal(err)
			}
			assertCounters(t, c, 1, 1, "")
			if shutdown {
				f.Shutdown()
			} else {
				cancel()
			}
			select {
			case <-body.closed:
			case <-time.After(time.Second):
				t.Fatal("body not released")
			}
			assertCounters(t, c, 1, 0, "cancelled")
			_ = resp.Body.Close()
			if shutdown {
				_, err = f.Client("openai", "generate").Get("https://example.test")
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("shutdown request: %v", err)
				}
				assertCounters(t, c, 1, 0, "cancelled")
			}
		})
	}
}

func TestHTTPTransportErrorsAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		err     error
		outcome string
	}{{errors.New("dial failed"), "transport_error"}, {context.DeadlineExceeded, "timeout"}, {context.Canceled, "cancelled"}} {
		t.Run(tc.outcome, func(t *testing.T) {
			c := NewCollector()
			f := NewHTTPClients(c, roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, tc.err }))
			defer f.Shutdown()
			_, err := f.Client("openai", "generate").Get("https://example.test")
			if err == nil {
				t.Fatal("expected transport error")
			}
			assertCounters(t, c, 1, 0, tc.outcome)
		})
	}
}

func TestHTTPBodyDeadline(t *testing.T) {
	c := NewCollector()
	body := &observedBody{reader: strings.NewReader("unread"), closed: make(chan struct{})}
	f := NewHTTPClients(c, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
	}))
	defer f.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
	resp, err := f.Client("openai", "generate").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertCounters(t, c, 1, 1, "")
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("deadline did not release body")
	}
	_ = resp.Body.Close()
	assertCounters(t, c, 1, 0, "timeout")
}

func TestHTTPConcurrentSnapshotsAndInstanceIsolation(t *testing.T) {
	c := NewCollector()
	other := NewCollector()
	if c.Snapshot().InstanceID == other.Snapshot().InstanceID {
		t.Fatal("shared instance identity")
	}
	f := NewHTTPClients(c, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	}))
	defer f.Shutdown()
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := f.Client("openai", "generate").Get("https://example.test")
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}()
	}
	for range 100 {
		for _, s := range c.Snapshot().ExternalRequests {
			var finished int64
			for _, o := range s.Outcomes {
				finished += o.Finished
			}
			if s.Total != finished+s.Inflight {
				t.Fatalf("torn snapshot: %+v", s)
			}
		}
	}
	wg.Wait()
	assertCounters(t, c, 40, 0, "success")
	assertCounters(t, other, 0, 0, "")
}

func TestHTTPWaitIsBoundedAndJoinsShutdown(t *testing.T) {
	c := NewCollector()
	entered := make(chan struct{})
	release := make(chan struct{})
	f := NewHTTPClients(c, roundTripFunc(func(*http.Request) (*http.Response, error) { close(entered); <-release; return nil, context.Canceled }))
	done := make(chan struct{})
	go func() { defer close(done); _, _ = f.Client("openai", "generate").Get("https://example.test") }()
	<-entered
	f.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("join should time out for blocked transport: %v", err)
	}
	close(release)
	if err := f.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-done
	assertCounters(t, c, 1, 0, "cancelled")
}
