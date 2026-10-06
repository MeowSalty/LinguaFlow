package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

type historyStreamRecorder struct {
	*httptest.ResponseRecorder
	connected chan struct{}
	resume    chan struct{}
	once      sync.Once
}

func (w *historyStreamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.connected); <-w.resume })
}

func TestTaskHistoryDeleteClosesAnExistingStream(t *testing.T) {
	s, c, u := jobStreamTestServer(t, 16, 10, 20)
	job, err := seedJobEventsN(t, c, u, 3)
	if err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOne(job).SetStatus("completed").ExecX(t.Context())
	s.taskLifecycle = &tasklife.Coordinator{}
	s.taskHistory = service.NewTaskHistoryService(c, s.projectSvc, s.taskLifecycle, s.eventBroker)
	s.taskHistory.SetReady(true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	w := &historyStreamRecorder{ResponseRecorder: httptest.NewRecorder(), connected: make(chan struct{}), resume: make(chan struct{})}
	r := withAuthUser(httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx), u)
	done := make(chan struct{})
	go func() { defer close(done); s.handleJobStream(w, r, job.ID) }()
	select {
	case <-w.connected:
	case <-ctx.Done():
		t.Fatal("stream did not connect")
	}
	deleted := httptest.NewRecorder()
	s.DeleteJobHistory(deleted, withAuthUser(httptest.NewRequest(http.MethodDelete, "/job", nil), u), job.ID)
	close(w.resume)
	if deleted.Code != 204 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("deleted task stream remained open")
	}
	next := httptest.NewRecorder()
	s.handleJobStream(next, withAuthUser(httptest.NewRequest(http.MethodGet, "/stream", nil), u), job.ID)
	if next.Code != 404 {
		t.Fatalf("new stream after deletion: %d %s", next.Code, next.Body.String())
	}
	if events, err := s.eventBroker.Replay(t.Context(), job.ID, 0, 20); err != nil || len(events) != 0 {
		t.Fatalf("replay after deletion: %+v %v", events, err)
	}
}

func TestTaskHistoryNewSubscriptionWaitsForDeleteGuard(t *testing.T) {
	s, c, u := jobStreamTestServer(t, 16, 10, 20)
	job, err := seedJobEventsN(t, c, u, 1)
	if err != nil {
		t.Fatal(err)
	}
	s.taskLifecycle = &tasklife.Coordinator{}
	guard, err := s.taskLifecycle.Lock(t.Context(), "translation", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	r := withAuthUser(httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(ctx), u)
	done := make(chan struct{})
	go func() { defer close(done); s.handleJobStream(w, r, job.ID) }()
	c.SSEEvent.Delete().Where(sseevent.JobIDEQ(job.ID)).ExecX(t.Context())
	if err := c.Job.DeleteOne(job).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.eventBroker.CloseJob(job.ID)
	guard.Release()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("subscription failed to recheck deleted task")
	}
	if w.Code != 404 {
		t.Fatalf("stream crossed delete guard: %d %s", w.Code, w.Body.String())
	}
}
