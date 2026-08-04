package backend

import (
	"context"
	"sync"
	"testing"
)

type fakeMeteredBackend struct {
	name   string
	resp   *Response
	err    error
	mu     sync.Mutex
	called int
}

func (f *fakeMeteredBackend) Name() string { return f.name }

func (f *fakeMeteredBackend) Translate(_ context.Context, _ Request) (*Response, error) {
	f.mu.Lock()
	f.called++
	f.mu.Unlock()
	return f.resp, f.err
}

func (f *fakeMeteredBackend) Close() error { return nil }

func TestMeteredBackendCountsSuccessfulCalls(t *testing.T) {
	inner := &fakeMeteredBackend{
		name: "test",
		resp: &Response{Text: "ok", Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	}
	m := NewMeteredBackend(inner)

	for i := 0; i < 3; i++ {
		resp, err := m.Translate(context.Background(), Request{})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Text != "ok" {
			t.Fatalf("got %q want %q", resp.Text, "ok")
		}
	}

	metrics := m.Metrics()
	if metrics.APICalls != 3 {
		t.Fatalf("APICalls = %d, want 3", metrics.APICalls)
	}
	if metrics.InputTokens != 30 {
		t.Fatalf("InputTokens = %d, want 30", metrics.InputTokens)
	}
	if metrics.OutputTokens != 15 {
		t.Fatalf("OutputTokens = %d, want 15", metrics.OutputTokens)
	}
}

func TestMeteredBackendCountsFailedAttemptsWithoutTokens(t *testing.T) {
	inner := &fakeMeteredBackend{
		name: "test",
		resp: &Response{Text: "ok", Usage: Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	}
	m := NewMeteredBackend(inner)

	// first call — return an error
	inner.err = assertError("fail")
	_, err := m.Translate(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected error")
	}

	// second call — success
	inner.err = nil
	resp, err := m.Translate(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" {
		t.Fatalf("got %q want %q", resp.Text, "ok")
	}

	metrics := m.Metrics()
	if metrics.APICalls != 2 {
		t.Fatalf("APICalls = %d, want 2", metrics.APICalls)
	}
	if metrics.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want 10", metrics.InputTokens)
	}
	if metrics.OutputTokens != 5 {
		t.Fatalf("OutputTokens = %d, want 5", metrics.OutputTokens)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }

func TestMeteredBackendConcurrent(t *testing.T) {
	const goroutines = 50
	inner := &fakeMeteredBackend{
		name: "test",
		resp: &Response{Text: "ok", Usage: Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}},
	}
	m := NewMeteredBackend(inner)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Translate(context.Background(), Request{})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	metrics := m.Metrics()
	if metrics.APICalls != goroutines {
		t.Fatalf("APICalls = %d, want %d", metrics.APICalls, goroutines)
	}
	if metrics.InputTokens != int64(goroutines*1) {
		t.Fatalf("InputTokens = %d, want %d", metrics.InputTokens, goroutines*1)
	}
	if metrics.OutputTokens != int64(goroutines*2) {
		t.Fatalf("OutputTokens = %d, want %d", metrics.OutputTokens, goroutines*2)
	}
}
