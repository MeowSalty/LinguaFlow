package pipeline

import (
	"testing"
	"time"
)

func TestPauseGateLinearizesDispatch(t *testing.T) {
	t.Run("pause first", func(t *testing.T) {
		gate := NewPauseGate()
		gate.Pause()
		called := false
		if gate.TryStartDispatch(func() bool { called = true; return true }) || called || gate.Inflight() != 0 {
			t.Fatal("closed gate invoked admission or consumed capacity")
		}
	})
	t.Run("dispatch first", func(t *testing.T) {
		gate := NewPauseGate()
		entered := make(chan struct{})
		release := make(chan struct{})
		dispatched := make(chan bool, 1)
		go func() { dispatched <- gate.TryStartDispatch(func() bool { close(entered); <-release; return true }) }()
		<-entered
		paused := make(chan struct{})
		go func() { gate.Pause(); close(paused) }()
		close(release)
		if !<-dispatched {
			t.Fatal("admitted request was lost")
		}
		select {
		case <-paused:
		case <-time.After(time.Second):
			t.Fatal("pause did not complete")
		}
		if gate.Inflight() != 1 {
			t.Fatal("pause cancelled previously admitted work")
		}
		gate.ReleaseInflight()
		if gate.Inflight() != 0 {
			t.Fatal("request did not drain")
		}
	})
}
