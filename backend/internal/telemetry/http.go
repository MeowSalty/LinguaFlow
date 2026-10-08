package telemetry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// HTTPClientFactory is a runtime dependency, never part of persisted backend options.
type HTTPClientFactory interface {
	Client(provider, operation string) *http.Client
}

// ClientFor keeps standalone CLI/test construction compatible without global state.
func ClientFor(factory HTTPClientFactory, provider, operation string) *http.Client {
	if factory == nil {
		return nil
	}
	return factory.Client(provider, operation)
}

type HTTPClients struct {
	clients [4][2]*http.Client
	base    http.RoundTripper
	cancel  context.CancelFunc
	mu      sync.Mutex
	active  int
	idle    chan struct{}
}

func NewHTTPClients(collector *Collector, transports ...http.RoundTripper) *HTTPClients {
	var base http.RoundTripper = http.DefaultTransport.(*http.Transport).Clone()
	if len(transports) > 0 && transports[0] != nil {
		base = transports[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	f := &HTTPClients{base: base, cancel: cancel, idle: idle}
	for p, provider := range providers {
		for o, operation := range operations {
			f.clients[p][o] = &http.Client{Transport: &requestTransport{base: base, collector: collector, provider: provider, operation: operation, lifetime: ctx, owner: f}}
		}
	}
	return f
}

func (f *HTTPClients) Client(provider, operation string) *http.Client {
	return f.clients[dimension(provider, providers[:], 3)][dimension(operation, operations[:], 0)]
}

func (f *HTTPClients) CloseIdleConnections() {
	if closer, ok := f.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (f *HTTPClients) Shutdown() { f.cancel(); f.CloseIdleConnections() }

// Wait joins the transport lifetimes, including response bodies. Call Shutdown
// first to stop admission; a context bounds misbehaving upstream transports.
func (f *HTTPClients) Wait(ctx context.Context) error {
	f.mu.Lock()
	idle := f.idle
	f.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *HTTPClients) begin(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.active == 0 {
		f.idle = make(chan struct{})
	}
	f.active++
	return nil
}

func (f *HTTPClients) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active--
	if f.active == 0 {
		close(f.idle)
	}
}

type requestTransport struct {
	base                http.RoundTripper
	collector           *Collector
	provider, operation string
	lifetime            context.Context
	owner               *HTTPClients
}

func (t *requestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if err := t.lifetime.Err(); err != nil {
		return nil, err
	}
	if err := t.owner.begin(t.lifetime); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(req.Context())
	stopLifetime := context.AfterFunc(t.lifetime, cancel)
	cleanup := func() { stopLifetime(); cancel(); t.owner.end() }
	start := time.Now()
	finish := func(string, time.Duration) {}
	if t.collector != nil {
		finish = t.collector.begin(t.provider, t.operation)
	}
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		finish(classify(ctx, 0, err), time.Since(start))
		cleanup()
		return resp, err
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	b := &measuredBody{ReadCloser: resp.Body, ctx: ctx, status: resp.StatusCode, start: start, finish: finish, cleanup: cleanup}
	resp.Body = b
	stop := context.AfterFunc(ctx, func() { b.end(ctx.Err()); _ = b.closeBody() })
	b.setStop(stop)
	if b.ReadCloser == http.NoBody {
		b.end(nil)
	}
	return resp, nil
}

func classify(ctx context.Context, status int, err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return "cancelled"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "transport_error"
	}
	if status >= 400 && status <= 599 {
		return "http_error"
	}
	return "success"
}

type measuredBody struct {
	io.ReadCloser
	ctx       context.Context
	status    int
	start     time.Time
	finish    func(string, time.Duration)
	cleanup   func()
	once      sync.Once
	closeOnce sync.Once
	closeErr  error
	mu        sync.Mutex
	stop      func() bool
	ended     bool
}

func (b *measuredBody) setStop(stop func() bool) {
	b.mu.Lock()
	ended := b.ended
	if !ended {
		b.stop = stop
	}
	b.mu.Unlock()
	if ended {
		stop()
	}
}

func (b *measuredBody) end(err error) {
	b.once.Do(func() {
		outcome := classify(b.ctx, b.status, err)
		b.mu.Lock()
		b.ended = true
		stop := b.stop
		b.mu.Unlock()
		if stop != nil {
			stop()
		}
		// Release the underlying stream before signalling the server's join barrier.
		// The outcome was fixed above, so cleanup cancellation cannot overwrite it.
		_ = b.closeBody()
		b.finish(outcome, time.Since(b.start))
		b.cleanup()
	})
}

func (b *measuredBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.end(err)
	}
	return n, err
}

func (b *measuredBody) closeBody() error {
	b.closeOnce.Do(func() { b.closeErr = b.ReadCloser.Close() })
	return b.closeErr
}

func (b *measuredBody) Close() error { err := b.closeBody(); b.end(err); return err }
