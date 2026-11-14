package api

import (
	"io"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// Keep chi's accounting and optional interface set, but expose FlushError so a
// ResponseController can observe failures through the logging middleware.
func newLoggingResponseWriter(w http.ResponseWriter, protoMajor int) chimiddleware.WrapResponseWriter {
	wrapped := chimiddleware.NewWrapResponseWriter(w, protoMajor)
	if _, ok := wrapped.(http.Flusher); !ok {
		return wrapped
	}
	flusher := &loggingFlusher{WrapResponseWriter: wrapped}
	if pusher, ok := wrapped.(http.Pusher); ok {
		return &struct {
			*loggingFlusher
			http.Pusher
		}{flusher, pusher}
	}
	if hijacker, ok := wrapped.(http.Hijacker); ok {
		if reader, ok := wrapped.(io.ReaderFrom); ok {
			return &struct {
				*loggingFlusher
				http.Hijacker
				io.ReaderFrom
			}{flusher, hijacker, reader}
		}
		return &struct {
			*loggingFlusher
			http.Hijacker
		}{flusher, hijacker}
	}
	return flusher
}

type loggingFlusher struct {
	chimiddleware.WrapResponseWriter
	discard bool
}

func (w *loggingFlusher) FlushError() error {
	if w.Status() == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.discard {
		return nil
	}
	// Unwrap exactly our chi accounting layer. The original writer may itself
	// buffer or compress, and its own FlushError/Flush must still run.
	return http.NewResponseController(w.Unwrap()).Flush()
}

func (w *loggingFlusher) Flush() {
	_ = w.FlushError()
}

func (w *loggingFlusher) Discard() {
	w.discard = true
	w.WrapResponseWriter.Discard()
}
