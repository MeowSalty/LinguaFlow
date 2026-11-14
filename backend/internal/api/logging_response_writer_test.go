package api

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

type basicLoggingWriter struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (w *basicLoggingWriter) Header() http.Header         { return w.header }
func (w *basicLoggingWriter) WriteHeader(code int)        { w.code = code }
func (w *basicLoggingWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

type loggingHTTP1Writer struct {
	*controlledStreamWriter
	readFrom int
}

func (w *loggingHTTP1Writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}
func (w *loggingHTTP1Writer) ReadFrom(r io.Reader) (int64, error) {
	w.readFrom++
	return io.Copy(w.safeRecorder, r)
}

type loggingHTTP2Writer struct{ *controlledStreamWriter }

func (w *loggingHTTP2Writer) Push(string, *http.PushOptions) error { return http.ErrNotSupported }

// A future buffering layer's FlushError must run before reaching its wrapped
// transport. This one intentionally fails instead of flushing its pending data.
type failingBufferedLoggingWriter struct {
	*controlledStreamWriter
	flushes int
}

func (w *failingBufferedLoggingWriter) FlushError() error { w.flushes++; return osStreamFlushError }

var osStreamFlushError = errors.New("buffer flush failed")

func TestLoggingResponseWriterPreservesCapabilities(t *testing.T) {
	clock := &streamClock{t: time.Now()}
	for _, tc := range []struct {
		name   string
		writer http.ResponseWriter
		proto  int
	}{
		{"basic", &basicLoggingWriter{header: make(http.Header)}, 1},
		{"http1", &loggingHTTP1Writer{controlledStreamWriter: newControlledStreamWriter(clock)}, 1},
		{"http2", &loggingHTTP2Writer{newControlledStreamWriter(clock)}, 2},
		{"flush", newControlledStreamWriter(clock), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := chimiddleware.NewWrapResponseWriter(tc.writer, tc.proto)
			got := newLoggingResponseWriter(tc.writer, tc.proto)
			capabilities := func(w http.ResponseWriter) [4]bool {
				_, fl := w.(http.Flusher)
				_, hj := w.(http.Hijacker)
				_, rf := w.(io.ReaderFrom)
				_, ps := w.(http.Pusher)
				return [4]bool{fl, hj, rf, ps}
			}
			if capabilities(got) != capabilities(want) {
				t.Fatalf("capabilities = %v, chi = %v", capabilities(got), capabilities(want))
			}
			_, errorFlush := got.(interface{ FlushError() error })
			if errorFlush != capabilities(want)[0] {
				t.Fatalf("FlushError exposed without Flusher: %t", errorFlush)
			}
			if got.Unwrap() != tc.writer {
				t.Fatal("writer identity changed")
			}
		})
	}
}

func TestLoggingResponseWriterFlushErrorAndDiscard(t *testing.T) {
	for _, proto := range []int{1, 2} {
		t.Run(strconv.Itoa(proto), func(t *testing.T) {
			clock := &streamClock{t: time.Now()}
			base := &failingBufferedLoggingWriter{controlledStreamWriter: newControlledStreamWriter(clock)}
			var original http.ResponseWriter = base
			if proto == 2 {
				original = &struct {
					*failingBufferedLoggingWriter
					http.Pusher
				}{base, &loggingHTTP2Writer{base.controlledStreamWriter}}
			}
			w := newLoggingResponseWriter(original, proto)
			if err := http.NewResponseController(w).Flush(); !errors.Is(err, osStreamFlushError) {
				t.Fatalf("lost FlushError: %v", err)
			}
			if w.Status() != 200 || w.BytesWritten() != 0 || base.flushes != 1 {
				t.Fatalf("flush accounting: status=%d bytes=%d calls=%d", w.Status(), w.BytesWritten(), base.flushes)
			}
			var tee bytes.Buffer
			w.Tee(&tee)
			w.Discard()
			if n, err := w.Write([]byte("tee only")); n != 8 || err != nil {
				t.Fatalf("discard Write: %d %v", n, err)
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Fatalf("Discard flushed underlying: %v", err)
			}
			if base.flushes != 1 || base.String() != "" || tee.String() != "tee only" || w.BytesWritten() != 8 {
				t.Fatal("Discard/Tee semantics changed")
			}
		})
	}
}

func TestLoggingResponseWriterReaderFromAccounting(t *testing.T) {
	for _, mode := range []string{"direct", "tee", "discard"} {
		t.Run(mode, func(t *testing.T) {
			base := &loggingHTTP1Writer{controlledStreamWriter: newControlledStreamWriter(&streamClock{t: time.Now()})}
			w := newLoggingResponseWriter(base, 1)
			var tee bytes.Buffer
			if mode != "direct" {
				w.Tee(&tee)
			}
			if mode == "discard" {
				w.Discard()
			}
			n, err := w.(io.ReaderFrom).ReadFrom(strings.NewReader("payload"))
			if err != nil || n != 7 || w.BytesWritten() != 7 || w.Status() != 200 {
				t.Fatalf("ReadFrom accounting: %d %v %d %d", n, err, w.BytesWritten(), w.Status())
			}
			if mode == "direct" && base.readFrom != 1 {
				t.Fatal("lost fast ReadFrom")
			}
			if mode != "direct" && (base.readFrom != 0 || tee.String() != "payload") {
				t.Fatal("ReadFrom bypassed Tee")
			}
			if mode == "discard" && base.String() != "" {
				t.Fatal("ReadFrom bypassed Discard")
			}
		})
	}
}

func TestJobStreamRouterObservesFlushErrorAndLogsAbort(t *testing.T) {
	for _, proto := range []int{1, 2} {
		t.Run(strconv.Itoa(proto), func(t *testing.T) {
			s, _, account, job := serverStreamFixture(t, 0)
			var log bytes.Buffer
			s.logger = slog.New(slog.NewJSONHandler(&log, nil))
			base := &failingBufferedLoggingWriter{controlledStreamWriter: newControlledStreamWriter(&streamClock{t: time.Now()})}
			var w http.ResponseWriter = base
			if proto == 2 {
				w = &struct {
					*failingBufferedLoggingWriter
					http.Pusher
				}{base, &loggingHTTP2Writer{base.controlledStreamWriter}}
			}
			r := streamBearerRequest(t, account, time.Now().Add(time.Hour))
			r.URL.Path = "/api/v1/jobs/" + strconv.Itoa(job.ID) + "/stream"
			r.ProtoMajor = proto
			assertStreamAbort(t, func() { s.newRouter().ServeHTTP(w, r) })
			if base.flushes != 1 || base.String() != ": connected\n\n" {
				t.Fatalf("router continued after error: %q, flushes=%d", base.String(), base.flushes)
			}
			trace := base.snapshot()
			if len(trace) != 3 || trace[0].kind != "deadline" {
				t.Fatalf("deadline reset after FlushError: %+v", trace)
			}
			if !strings.Contains(log.String(), `"status":200`) || !strings.Contains(log.String(), `"bytes":13`) {
				t.Fatalf("abort skipped or corrupted log: %s", log.String())
			}
		})
	}
}

func TestJobStreamRouterRejectsUnsupportedWriter(t *testing.T) {
	for _, flush := range []bool{false, true} {
		t.Run(strconv.FormatBool(flush), func(t *testing.T) {
			s, _, account, job := serverStreamFixture(t, 0)
			r := streamBearerRequest(t, account, time.Now().Add(time.Hour))
			r.URL.Path = "/api/v1/jobs/" + strconv.Itoa(job.ID) + "/stream"
			if flush {
				w := httptest.NewRecorder() // Supports Flush but no write deadline.
				s.newRouter().ServeHTTP(w, r)
				if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "streaming deadlines not supported") || w.Flushed {
					t.Fatal("unsupported deadline writer started SSE")
				}
			} else {
				w := &basicLoggingWriter{header: make(http.Header)}
				s.newRouter().ServeHTTP(w, r)
				if w.code != http.StatusInternalServerError || !strings.Contains(w.body.String(), "streaming not supported") {
					t.Fatalf("unsupported Flush accepted: %d %s", w.code, w.body.String())
				}
			}
		})
	}
}
