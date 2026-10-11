package backend_test

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type countingRequestLimiter struct{ waits int }

func (l *countingRequestLimiter) Wait(context.Context) error { l.waits++; return nil }
func (*countingRequestLimiter) Close()                       {}

type admittedTestBackend struct{ calls int }

func (*admittedTestBackend) Name() string { return "test" }
func (*admittedTestBackend) Close() error { return nil }
func (b *admittedTestBackend) Translate(context.Context, backend.Request) (*backend.Response, error) {
	b.calls++
	return &backend.Response{Text: "ok"}, nil
}

func TestAdmittedRequestDoesNotChargeRPMTwice(t *testing.T) {
	limiter := &countingRequestLimiter{}
	inner := &admittedTestBackend{}
	b := backend.NewRateLimitedBackend(inner, limiter)
	if _, err := b.Translate(context.Background(), backend.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Translate(backend.WithAdmittedRequest(context.Background()), backend.Request{}); err != nil {
		t.Fatal(err)
	}
	if limiter.waits != 1 || inner.calls != 2 {
		t.Fatalf("waits=%d calls=%d", limiter.waits, inner.calls)
	}
}
