package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandoffFixtureDiagnosticBoundsMutexWait(t *testing.T) {
	var held sync.Mutex
	held.Lock()
	finished := make(chan struct{})
	started := time.Now()
	diagnostic, complete := boundedHandoffDiagnostic(20*time.Millisecond, func(context.Context) string {
		held.Lock()
		defer held.Unlock()
		defer close(finished)
		return "acquired"
	})
	// Release and join the test-owned diagnostic goroutine before any failure.
	held.Unlock()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("test diagnostic collector did not join after releasing its mutex")
	}
	if complete || !strings.Contains(diagnostic, "including fixture mutexes") || time.Since(started) > time.Second {
		t.Fatalf("blocked diagnostic escaped its wall-clock budget: complete=%v diagnostic=%q elapsed=%s", complete, diagnostic, time.Since(started))
	}
}
