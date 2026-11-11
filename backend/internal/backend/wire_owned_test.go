package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type wireOwnedProbe struct {
	current, peak int64
	objects       map[string]int64
}

func (p *wireOwnedProbe) ObserveOwned(category, key string, n int64) {
	if p.objects == nil {
		p.objects = map[string]int64{}
	}
	name := category + ":" + key
	p.current -= p.objects[name]
	p.current += n
	p.objects[name] = n
	if p.current > p.peak {
		p.peak = p.current
	}
}

func TestOwnedWireReadActualCapacityAndClear(t *testing.T) {
	p := &wireOwnedProbe{}
	ctx := WithOwnedObserver(context.Background(), p)
	trace := NewOwnedTrace(ctx, "wire", "test-http")
	data, e := ReadBoundedObserved(strings.NewReader(strings.Repeat("x", 5000)), 10<<10, trace)
	if e != nil || int64(cap(data)) != p.current || p.peak <= p.current {
		t.Fatalf("len=%d cap=%d current=%d peak=%d err=%v", len(data), cap(data), p.current, p.peak, e)
	}
	trace.Clear()
	if p.current != 0 {
		t.Fatal("buffer observation survived owner close")
	}
}

func TestOwnedResponseTextActualBuffersAndFinalCopy(t *testing.T) {
	p := &wireOwnedProbe{}
	ctx := WithOwnedObserver(context.Background(), p)
	text, e := NewResponseTextObserved(ctx, 10<<10, "test-text")
	if e != nil {
		t.Fatal(e)
	}
	if e = text.Append(strings.Repeat("x", 5000)); e != nil {
		t.Fatal(e)
	}
	if e = text.Append(strings.Repeat("y", 1000)); e != nil {
		t.Fatal(e)
	}
	out, e := FinishResponseText(ctx, text)
	if e != nil {
		t.Fatal(e)
	}
	if p.current != int64(cap(text.writer.data)+len(out)) {
		t.Fatalf("current=%d cap=%d output=%d", p.current, cap(text.writer.data), len(out))
	}
	text.ClearObserved()
	if p.current != 0 {
		t.Fatal("text buffer observation survived clear")
	}
}

func TestOwnedSSEEventBufferCloses(t *testing.T) {
	p := &wireOwnedProbe{}
	ctx := WithOwnedObserver(context.Background(), p)
	e := ReadSSEContext(ctx, strings.NewReader("data: "+strings.Repeat("x", 5000)+"\n\n"), 10<<10, func([]byte) error { return nil })
	if e != nil || p.peak < 5000 || p.current != 0 {
		t.Fatalf("current=%d peak=%d err=%v", p.current, p.peak, e)
	}
}

func TestOwnedProviderErrorEnvelopeCloses(t *testing.T) {
	p := &wireOwnedProbe{}
	ctx := WithOwnedObserver(WithRequestCredits(context.Background(), 1024, 64), p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := GuardProviderErrors(req, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"2"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 1024)))}, nil
	})
	if resp.StatusCode != http.StatusTooManyRequests || !errors.Is(err, ErrResponseTooLarge) || p.peak <= 0 || p.peak > 128 || p.current != 0 {
		t.Fatalf("status=%d current=%d peak=%d err=%v", resp.StatusCode, p.current, p.peak, err)
	}
}
