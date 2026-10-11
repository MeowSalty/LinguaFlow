package backend

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const DefaultMaxResponseBytes int64 = 1 << 20

// ErrResponseTooLarge is a capacity failure, never a reason to retry a model.
var ErrResponseTooLarge = errors.New("backend response exceeds byte limit")

type ResponseLimitError struct{ Limit int64 }

func (e *ResponseLimitError) Error() string {
	return fmt.Sprintf("%s (%d bytes)", ErrResponseTooLarge, e.Limit)
}
func (e *ResponseLimitError) Unwrap() error { return ErrResponseTooLarge }
func (*ResponseLimitError) Permanent() bool { return true }

// RequestTimeoutError identifies an expired provider-call timeout while its
// caller context remains live. Parent cancellation/deadlines are never wrapped.
type RequestTimeoutError struct{ Err error }

func (e *RequestTimeoutError) Error() string {
	return fmt.Sprintf("backend request timed out: %v", e.Err)
}
func (e *RequestTimeoutError) Unwrap() error { return e.Err }

type responseMetadataKey struct{}
type responseMetadata struct {
	mu            sync.Mutex
	status        int
	retryAfter    time.Duration
	limitExceeded bool
}

// LimitResponseClient copies client and bounds the bytes consumed after HTTP
// decompression, including all SSE events. Redirects are surfaced as responses:
// following them would be another unaccounted external request.
func LimitResponseClient(client *http.Client, limit int64) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	copyClient.Transport = &responseLimitTransport{inner: transport, limit: limit}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copyClient
}

type responseLimitTransport struct {
	inner http.RoundTripper
	limit int64
}

func (t *responseLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if meta, ok := req.Context().Value(responseMetadataKey{}).(*responseMetadata); ok {
		meta.mu.Lock()
		meta.status = resp.StatusCode
		meta.retryAfter = ParseRetryAfter(resp.Header, time.Now())
		meta.mu.Unlock()
	}
	if resp.Body == nil {
		return resp, nil
	}
	body := resp.Body
	// The standard transport usually already decoded gzip. Explicit encodings
	// and injected transports must obey the same limit on decompressed bytes.
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		decoded, decodeErr := gzip.NewReader(body)
		if decodeErr != nil {
			_ = body.Close()
			return nil, fmt.Errorf("decode backend gzip response: %w", decodeErr)
		}
		body = &decodedResponseBody{Reader: decoded, decoded: decoded, original: body}
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		resp.Uncompressed = true
	}
	meta, _ := req.Context().Value(responseMetadataKey{}).(*responseMetadata)
	resp.Body = &limitedResponseBody{body: body, remaining: t.limit, limit: t.limit, metadata: meta}
	return resp, nil
}

func (t *responseLimitTransport) CloseIdleConnections() {
	if closer, ok := t.inner.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type decodedResponseBody struct {
	io.Reader
	decoded  io.Closer
	original io.Closer
}

func (b *decodedResponseBody) Close() error {
	return errors.Join(b.decoded.Close(), b.original.Close())
}

type limitedResponseBody struct {
	body      io.ReadCloser
	remaining int64
	limit     int64
	exceeded  bool
	metadata  *responseMetadata
}

func (b *limitedResponseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.exceeded {
		return 0, &ResponseLimitError{Limit: b.limit}
	}
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.body.Read(extra[:])
		if n > 0 {
			b.exceeded = true
			if b.metadata != nil {
				b.metadata.mu.Lock()
				b.metadata.limitExceeded = true
				b.metadata.mu.Unlock()
			}
			return 0, &ResponseLimitError{Limit: b.limit}
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	return n, err
}
func (b *limitedResponseBody) Close() error { return b.body.Close() }

type boundedBackend struct {
	inner Backend
	limit int64
}

func (b *boundedBackend) Name() string     { return b.inner.Name() }
func (b *boundedBackend) Close() error     { return b.inner.Close() }
func (b *boundedBackend) Backend() Backend { return b.inner }
func (b *boundedBackend) Translate(ctx context.Context, req Request) (*Response, error) {
	meta := &responseMetadata{}
	ctx = context.WithValue(ctx, responseMetadataKey{}, meta)
	resp, err := b.inner.Translate(ctx, req)
	meta.mu.Lock()
	exceeded := meta.limitExceeded
	meta.mu.Unlock()
	// Some stream decoders replace a reader error with a parse error containing
	// the partial body. Restore the capacity classification without exposing it.
	if exceeded {
		return nil, &ResponseLimitError{Limit: b.limit}
	}
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			err = &RequestTimeoutError{Err: err}
		}
		meta.mu.Lock()
		defer meta.mu.Unlock()
		// Google APIError omits headers. Capturing at transport also covers SDK
		// errors that cannot decode the provider's error envelope.
		if meta.status >= 400 {
			err = &StatusError{StatusCode: meta.status, RetryAfter: meta.retryAfter, Err: err}
		}
		return nil, err
	}
	if resp != nil && int64(len(resp.Text)) > b.limit {
		return nil, &ResponseLimitError{Limit: b.limit}
	}
	return resp, nil
}
