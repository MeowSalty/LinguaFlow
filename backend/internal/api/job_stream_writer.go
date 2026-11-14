package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

const (
	sseWriteBudget = 5 * time.Second
	sseCloseBudget = 5 * time.Second
)

var errJobStreamStopped = errors.New("job stream stopped")

// jobStreamWriter owns transport deadlines only while doing I/O. Pending means
// a replay page has writes that have not completed a successful Flush yet.
type jobStreamWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	logger     *slog.Logger
	now        func() time.Time
	expiresAt  time.Time
	started    bool
	pending    bool
	failed     bool
	expired    bool
}

func (s *jobStreamWriter) begin() (time.Time, error) {
	if s.failed || s.expired {
		return time.Time{}, errJobStreamStopped
	}
	now := s.now()
	deadline := now.Add(sseWriteBudget)
	if !s.expiresAt.IsZero() && s.expiresAt.Before(deadline) {
		deadline = s.expiresAt
	}
	if !deadline.After(now) {
		// This is a local authorization bound, not a transport failure. A
		// preceding replay Write may have cleared its deadline while pending.
		s.expired = true
		return time.Time{}, errJobStreamStopped
	}
	if err := s.controller.SetWriteDeadline(deadline); err != nil {
		if !s.started && errors.Is(err, http.ErrNotSupported) {
			return time.Time{}, err
		}
		s.failed = true
		return time.Time{}, err
	}
	return deadline, nil
}

func (s *jobStreamWriter) within(deadline time.Time) bool {
	if !s.now().Before(deadline) {
		s.failed = true
	}
	return !s.failed
}

func (s *jobStreamWriter) end(deadline time.Time) bool {
	// An expired HTTP/2 stream cannot be recovered by resetting its deadline.
	if !s.within(deadline) {
		return false
	}
	if s.controller.SetWriteDeadline(time.Time{}) != nil {
		s.failed = true
	}
	return !s.failed
}

func (s *jobStreamWriter) write(data []byte) bool {
	s.pending = true
	n, err := s.w.Write(data)
	if err != nil || n != len(data) {
		s.failed = true
	}
	return !s.failed
}

func (s *jobStreamWriter) flush() bool {
	if s.controller.Flush() != nil {
		s.failed = true
		return false
	}
	s.pending = false
	return true
}

func (s *jobStreamWriter) writeEvent(data []byte) bool {
	deadline, err := s.begin()
	return err == nil && s.write(data) && s.end(deadline)
}

func (s *jobStreamWriter) flushPage() bool {
	deadline, err := s.begin()
	return err == nil && s.flush() && s.end(deadline)
}

func (s *jobStreamWriter) writeAndFlush(data []byte, start bool) error {
	deadline, err := s.begin()
	if err != nil {
		return err
	}
	if start {
		s.started = true
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.Header().Set("Connection", "keep-alive")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
	}
	if !s.write(data) || !s.within(deadline) || !s.flush() || !s.end(deadline) {
		return errJobStreamStopped
	}
	return nil
}

// finish is deferred by the handler, including its panic path. Only a healthy,
// fully flushed response may let net/http send its final chunk or END_STREAM.
func (s *jobStreamWriter) finish() {
	if rec := recover(); rec != nil {
		if s.started {
			if rec != http.ErrAbortHandler && s.logger != nil {
				s.logger.Error("job_stream: handler panic", "error", fmt.Sprint(rec), "stack", string(debug.Stack()))
			}
			s.abort()
		}
		panic(rec)
	}
	if s.failed || s.expired || s.pending {
		s.abort()
	}
	if s.started && s.controller.SetWriteDeadline(s.now().Add(sseCloseBudget)) != nil {
		panic(http.ErrAbortHandler)
	}
}

func (s *jobStreamWriter) abort() {
	if s.started && !s.failed {
		// HTTP/1 still flushes its connection buffer while closing after an
		// abort. Expire the write deadline so an unfinished page cannot escape
		// that path or block it. Never reset a stream after an I/O failure.
		_ = s.controller.SetWriteDeadline(s.now().Add(-time.Nanosecond))
	}
	panic(http.ErrAbortHandler)
}

type jobStreamTicker struct {
	ch   <-chan time.Time
	stop func()
}

type jobStreamTiming struct {
	now       func() time.Time
	newTicker func(time.Duration) jobStreamTicker
}

func defaultJobStreamTiming() jobStreamTiming {
	return jobStreamTiming{
		now: time.Now,
		newTicker: func(interval time.Duration) jobStreamTicker {
			ticker := time.NewTicker(interval)
			return jobStreamTicker{ch: ticker.C, stop: ticker.Stop}
		},
	}
}
