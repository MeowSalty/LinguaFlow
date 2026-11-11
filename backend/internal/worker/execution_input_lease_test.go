package worker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/telemetry"
)

type inputPressureTransport struct {
	root    *resourcebudget.RootBudget
	scope   resourcebudget.Scope
	once    sync.Once
	err     error
	trigger int32
	closed  atomic.Int32
}
type inputPressureBody struct {
	io.ReadCloser
	after func()
}

func (b *inputPressureBody) Close() error { err := b.ReadCloser.Close(); b.after(); return err }
func (t *inputPressureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	r.Body = &inputPressureBody{ReadCloser: r.Body, after: func() {
		if t.closed.Add(1) < max(int32(1), t.trigger) {
			return
		}
		t.once.Do(func() {
			s := t.root.Snapshot(t.scope.ActorID)
			n := s.Policy.Instance.MaxPayloadBytes - s.Instance.PayloadBytes
			if n > 0 {
				t.err = t.root.Claim(t.scope, "pressure", resourcebudget.Vector{PayloadBytes: n})
			}
		})
	}}
	return r, nil
}
func TestExecutionInputCapacityAfterMainRetainsUsage(t *testing.T) {
	for _, trigger := range []int32{1, 2} {
		t.Run(fmt.Sprintf("after_%d_HTTP", trigger), func(t *testing.T) {
			f := newRubyEntryFixture(t, false)
			if trigger == 1 {
				f.snapshot.Strategy.Ruby.Enabled = false
				f.snapshot.RubyRetry = nil
			}
			var err error
			f.snapshot, err = execution.Resolve(*f.snapshot)
			if err != nil {
				t.Fatal(err)
			}
			l := resourcebudget.Limits{MaxUnconfirmedMembers: 65536, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 16, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: l, Instance: l})
			if err != nil {
				t.Fatal(err)
			}
			f.factory.rootBudget = root
			f.factory.maxResponseBytes = 1024
			pressure := &inputPressureTransport{root: root, scope: resourcebudget.Scope{ActorID: 99, CallID: "pressure"}, trigger: trigger}
			clients := telemetry.NewHTTPClients(nil, pressure)
			defer clients.Shutdown()
			f.factory.httpClients = clients
			runner := NewQuickTranslateRunner(f.factory.logger, f.client, f.factory.limiterPool)
			runner.factory = f.factory
			ctx, cancel := context.WithTimeout(service.WithExecutionActor(context.Background(), 7), 5*time.Second)
			defer cancel()
			ctx = service.WithExecutionInputBudget(ctx, root, service.ExecutionScope(ctx), service.DefaultExecutionInputBytes)
			result, err := runner.Run(ctx, service.QuickTranslateRunnerInput{Snapshot: f.snapshot, SourceLang: "en", TargetLang: "zh", SourceText: rubyEntrySource, Format: "txt", Glossary: glossary.Nop{}})
			if !isExecutionCapacity(err) {
				t.Fatalf("expected capacity failure after paid HTTP, got result=%+v err=%v", result, err)
			}
			if result == nil || result.TargetText != "" || result.Collector != nil {
				t.Fatal("failed output leaked a draft or discarded finite metrics")
			}
			var calls, in, out int64
			for _, m := range result.Metrics {
				calls += m.APICalls
				in += m.InputTokens
				out += m.OutputTokens
			}
			if calls != int64(trigger) || in != 7*int64(trigger) || out != 11*int64(trigger) {
				t.Fatalf("actual paid usage lost or duplicated: calls=%d in=%d out=%d", calls, in, out)
			}
			if pressure.err != nil {
				t.Fatal(pressure.err)
			}
			if err = root.Transfer(pressure.scope, "pressure", resourcebudget.Vector{}); err != nil {
				t.Fatal(err)
			}
			if got := root.Snapshot(7).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("input/runtime leak after joined failure: %+v", got)
			}
			f.mu.Lock()
			actual := len(f.requests)
			f.mu.Unlock()
			if actual != int(trigger) {
				t.Fatalf("new HTTP dispatched after capacity error: %d", actual)
			}
			t.Logf("real main HTTP=%d; finite usage calls=%d input=%d output=%d; no draft; all roots retired", actual, calls, in, out)
		})
	}
}

func TestExecutionInputBodyBackingIsBorrowedOnce(t *testing.T) {
	f := newRubyEntryFixture(t, false)
	r := NewQuickTranslateRunner(f.factory.logger, f.client, f.factory.limiterPool)
	r.factory = f.factory
	l := resourcebudget.Limits{MaxUnconfirmedMembers: 65536, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 16, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: l, Instance: l})
	if err != nil {
		t.Fatal(err)
	}
	r.factory.rootBudget = root
	ctx := service.WithExecutionInputBudget(context.Background(), root, resourcebudget.Scope{ActorID: 7, CallID: "owned"}, service.DefaultExecutionInputBytes)
	body, err := service.NewExecutionInputLease(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, direct, err := prepareWorkerInputs(ctx, r, nil, "some source backing", 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	directCredit := root.Snapshot(7).Instance.PayloadBytes - 1024
	direct.Close()
	_, borrowed, err := prepareWorkerInputs(service.WithExecutionBodyOwnership(ctx), r, nil, "some source backing", 1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	borrowedCredit := root.Snapshot(7).Instance.PayloadBytes - 1024
	if borrowedCredit >= directCredit {
		t.Fatalf("actual root double charged borrowed bytes: %d %d", borrowedCredit, directCredit)
	}
	borrowed.Close()
	body.Close()
	service.ExecutionInputOwner(ctx).ReleaseExecutionInputs()
	if root.Snapshot(7).Instance != (resourcebudget.Vector{}) {
		t.Fatal("borrowed ownership leaked")
	}
}
