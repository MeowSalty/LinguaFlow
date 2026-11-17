package backend

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"
)

const DefaultMaxInputBytes int64 = 16 << 20
const MaxProviderErrorBytes int64 = 32 << 10

type requestCreditsKey struct{}

type BuildWorkers interface {
	Run(context.Context, func() error) error
}
type buildWorkersKey struct{}
type ownedObserverKey struct{}

func WithOwnedObserver(ctx context.Context, observer OwnedObserver) context.Context {
	return context.WithValue(ctx, ownedObserverKey{}, observer)
}
func OwnedObserverFromContext(ctx context.Context) OwnedObserver {
	observer, _ := ctx.Value(ownedObserverKey{}).(OwnedObserver)
	return observer
}

// OwnedTrace records named, actually allocated payload buffers. It never
// grants credit, and does not claim to measure the Go heap or hidden SDK data.
type OwnedTrace struct {
	observer      OwnedObserver
	category, key string
}

var ownedTraceSequence atomic.Uint64

func NewOwnedTrace(ctx context.Context, category, label string) *OwnedTrace {
	observer := OwnedObserverFromContext(ctx)
	if observer == nil {
		return nil
	}
	return &OwnedTrace{observer: observer, category: category, key: strconv.FormatUint(ownedTraceSequence.Add(1), 10) + "." + label}
}
func (t *OwnedTrace) Set(bytes int64) {
	if t != nil {
		t.observer.ObserveOwned(t.category, t.key, bytes)
	}
}
func (t *OwnedTrace) Clear() { t.Set(0) }

func WithBuildWorkers(ctx context.Context, workers BuildWorkers) context.Context {
	return context.WithValue(ctx, buildWorkersKey{}, workers)
}

// RunBuild covers both typed decoding and consumption so decoded object arenas
// cannot survive the CPU permit. Callers must not nest it on the same context.
func RunBuild(ctx context.Context, fn func() error) error {
	if w, ok := ctx.Value(buildWorkersKey{}).(BuildWorkers); ok {
		return w.Run(ctx, fn)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

type RequestCredits struct{ InputBytes, ResponseBytes int64 }

// WithRequestCredits carries the numeric credit from the Job's joint grant.
// It never owns or releases a ledger lease. Missing credit is only useful for
// standalone provider calls, which still have explicit finite defaults.
func WithRequestCredits(ctx context.Context, inputBytes, responseBytes int64) context.Context {
	return context.WithValue(ctx, requestCreditsKey{}, RequestCredits{inputBytes, responseBytes})
}
func RequestCreditsFromContext(ctx context.Context) RequestCredits {
	if c, ok := ctx.Value(requestCreditsKey{}).(RequestCredits); ok {
		return c
	}
	return RequestCredits{DefaultMaxInputBytes, DefaultMaxResponseBytes}
}

func ResponseBudget(ctx context.Context, configured int64) int64 {
	if c, ok := ctx.Value(requestCreditsKey{}).(RequestCredits); ok {
		return c.ResponseBytes
	}
	if configured > 0 {
		return configured
	}
	return DefaultMaxResponseBytes
}

type WireSizer interface{ MeasureRequest(Request) (int64, error) }
type ContextWireSizer interface {
	MeasureRequestContext(context.Context, Request) (int64, error)
}

func MeasureRequest(b Backend, req Request) (int64, error) {
	return MeasureRequestContext(context.Background(), b, req)
}
func MeasureRequestContext(ctx context.Context, b Backend, req Request) (int64, error) {
	for hops := 0; hops < 64; hops++ {
		if s, ok := b.(ContextWireSizer); ok {
			return s.MeasureRequestContext(ctx, req)
		}
		if s, ok := b.(WireSizer); ok {
			return s.MeasureRequest(req)
		}
		if w, ok := b.(interface{ Backend() Backend }); ok {
			b = w.Backend()
			continue
		}
		// A local/mock Backend with no wire protocol still receives a bounded
		// Request. Real adapters implement WireSizer with their full defaults.
		bounds, err := NewBuildBoundsFromContext(ctx, 0, DefaultMaxInputBytes)
		if err != nil {
			return 0, err
		}
		defer bounds.CloseRetired()
		return bounds.MeasureJSON(req)
	}
	return 0, fmt.Errorf("backend wrapper nesting exceeds limit")
}

// EncodeWireBuild returns one explicitly owned wire buffer. Envelope/cache
// scratch retires immediately after encoding; release follows the final HTTP
// borrower of data. The caller clears its data reference before release.
func EncodeWireBuild(ctx context.Context, build func(*BuildBounds) (any, error)) (data []byte, release func(), err error) {
	var b *BuildBounds
	var envelope any
	var owner *OwnedAllocation
	err = RunBuild(ctx, func() error {
		c := RequestCreditsFromContext(ctx)
		if c.InputBytes < 0 || c.ResponseBytes <= 0 {
			return &BuildCapacityError{"request credit"}
		}
		var e error
		b, e = NewBuildBoundsFromContext(ctx, c.ResponseBytes, c.InputBytes)
		if e != nil {
			return e
		}
		envelope, e = build(b)
		if e != nil {
			return e
		}
		data, e = b.EncodeJSON(envelope, c.InputBytes)
		if e != nil {
			return e
		}
		owner, e = b.RetainedAllocation(int64(cap(data)))
		if e != nil {
			return e
		}
		return owner.DetachBuild()
	})
	envelope = nil
	if b != nil {
		b.CloseRetired()
	}
	if err != nil {
		data = nil
		owner.Release()
		return nil, func() {}, err
	}
	return data, owner.Release, nil
}

func MeasureWireBuild(build func(*BuildBounds) (any, error)) (int64, error) {
	return MeasureWireBuildContext(context.Background(), build)
}

// MeasureWireBuildContext meters actual envelope/cache/sizing workspace, then
// ends those local references before retiring their capacity. CPU and any
// aggregate retry belong to the caller; this helper never waits for capacity.
func MeasureWireBuildContext(ctx context.Context, build func(*BuildBounds) (any, error)) (int64, error) {
	b, err := NewBuildBoundsFromContext(ctx, 0, DefaultMaxInputBytes)
	if err != nil {
		return 0, err
	}
	var v any
	defer func() { v = nil; b.CloseRetired() }()
	v, err = build(b)
	if err != nil {
		return 0, err
	}
	return b.MeasureJSON(v)
}

// ReadBounded checks each future capacity before growing. The one-byte probe
// distinguishes an exact-limit body from a larger response without retaining it.
func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	return ReadBoundedObserved(r, limit, nil)
}
func ReadBoundedObserved(r io.Reader, limit int64, trace *OwnedTrace) ([]byte, error) {
	if limit <= 0 {
		return nil, &BuildCapacityError{"response credit"}
	}
	b, err := NewBuildBounds(limit, 0)
	if err != nil {
		return nil, err
	}
	w := NewBoundedWriter(b, limit)
	var scratch [4096]byte
	for {
		remaining := limit - int64(len(w.data))
		nwant := min(int64(len(scratch)), remaining)
		if nwant == 0 {
			var probe [1]byte
			n, e := r.Read(probe[:])
			if n > 0 {
				return nil, &ResponseLimitError{limit}
			}
			if e == io.EOF {
				return w.Bytes(), nil
			}
			if e != nil {
				return nil, e
			}
			continue
		}
		n, e := r.Read(scratch[:nwant])
		if n > 0 {
			old := w.data
			if _, err = w.Write(scratch[:n]); err != nil {
				return nil, err
			}
			if cap(old) < cap(w.data) {
				trace.Set(int64(cap(old) + cap(w.data)))
				runtime.KeepAlive(old)
			}
			old = nil
			trace.Set(int64(cap(w.data)))
		}
		if e == io.EOF {
			return w.Bytes(), nil
		}
		if e != nil {
			return nil, e
		}
	}
}

// GuardProviderErrors avoids the SDK's ReadAll/error JSON path. A small envelope
// is consumed here; provider error messages are not copied into arbitrary logs.
// Status and Retry-After survive malformed/oversized envelopes unchanged.
func GuardProviderErrors(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	resp, err := next(req)
	if err != nil || resp == nil || resp.StatusCode < 400 {
		return resp, err
	}
	if resp.Body != nil {
		responseCredit := ResponseBudget(req.Context(), 0)
		if p, ok := req.Context().Value(providerAllocationKey{}).(*providerAllocation); ok {
			responseCredit = p.bounds.OutputLimit()
		}
		trace := NewOwnedTrace(req.Context(), "wire", "provider-error")
		defer trace.Clear()
		_, release, readErr := ReadProviderBounded(req.Context(), resp.Body, min(MaxProviderErrorBytes, responseCredit), trace)
		release()
		_ = resp.Body.Close()
		if errors.Is(readErr, ErrBuildCapacity) || responseCredit <= MaxProviderErrorBytes && errors.Is(readErr, ErrResponseTooLarge) {
			return resp, WrapHTTPError("provider request", readErr, resp.StatusCode, resp.Header)
		}
	}
	return resp, WrapHTTPError("provider request", errors.New(http.StatusText(resp.StatusCode)), resp.StatusCode, resp.Header)
}

// DecodeWire decodes only bounded typed fields; it does not create SDK Raw data.
func DecodeWire(data []byte, responseBytes int64, out any) error {
	if int64(len(data)) > responseBytes {
		return &ResponseLimitError{responseBytes}
	}
	b, err := NewBuildBounds(responseBytes, int64(len(data)))
	if err != nil {
		return err
	}
	return b.DecodeJSON(data, out)
}

// ReadSSE is a cumulative decompressed-byte limited, ordered SSE reader. Each
// event is discarded after the callback; no accumulator retains event objects.
func ReadSSE(r io.Reader, limit int64, consume func([]byte) error) error {
	return ReadSSEContext(context.Background(), r, limit, consume)
}

func ReadSSEContext(ctx context.Context, r io.Reader, limit int64, consume func([]byte) error) error {
	if limit <= 0 {
		return &BuildCapacityError{"SSE response credit"}
	}
	limited := &sseLimitReader{r: r, remaining: limit, limit: limit}
	reader := bufio.NewReaderSize(limited, int(min(int64(4096), limit)))
	trace := NewOwnedTrace(ctx, "wire", "sse-event")
	defer trace.Clear()
	readerBytes := int64(max(16, min(int64(4096), limit)))
	trace.Set(readerBytes)
	b, err := providerBounds(ctx, limit)
	if err != nil {
		return err
	}
	event := NewBoundedWriter(b, limit)
	defer event.Release()
	dispatch := func() error {
		if len(event.data) == 0 {
			return nil
		}
		data := bytes.TrimSuffix(event.data, []byte{'\n'})
		if bytes.Equal(data, []byte("[DONE]")) {
			event.data = event.data[:0]
			return nil
		}
		if err := consume(data); err != nil {
			return err
		}
		event.data = event.data[:0]
		return nil
	}
	// A fixed prefix state replaces a full line buffer. Only the current event
	// and useful output may retain R-sized backing across a network wait.
	var prefix [5]byte
	prefixLen, lineBytes := 0, 0
	dataLine, firstData, pendingCR := false, false, false
	var scratch [1]byte
	writeByte := func(c byte) error {
		scratch[0] = c
		write := func() error {
			old := event.data
			_, e := event.Write(scratch[:])
			if e == nil && cap(old) < cap(event.data) {
				trace.Set(readerBytes + int64(cap(old)+cap(event.data)))
				runtime.KeepAlive(old)
				old = nil
				trace.Set(readerBytes + int64(cap(event.data)))
			}
			return e
		}
		if len(event.data) == cap(event.data) {
			return RunBuild(ctx, write)
		}
		return write()
	}
	finishLine := func() error {
		if dataLine {
			if e := writeByte('\n'); e != nil {
				return e
			}
		}
		if lineBytes == 0 || lineBytes == 1 && prefix[0] == '\r' {
			if e := dispatch(); e != nil {
				return e
			}
		}
		prefixLen, lineBytes = 0, 0
		dataLine, firstData, pendingCR = false, false, false
		return nil
	}
	for {
		part, e := reader.ReadSlice('\n')
		for _, c := range part {
			if c == '\n' {
				if err = finishLine(); err != nil {
					return err
				}
				continue
			}
			lineBytes++
			if prefixLen < len(prefix) {
				prefix[prefixLen] = c
				prefixLen++
				if prefixLen == len(prefix) && prefix == [5]byte{'d', 'a', 't', 'a', ':'} {
					dataLine = true
					firstData = true
				}
				continue
			}
			if !dataLine {
				continue
			}
			if firstData {
				firstData = false
				if c == ' ' {
					continue
				}
			}
			if pendingCR {
				if err = writeByte('\r'); err != nil {
					return err
				}
				pendingCR = false
			}
			if c == '\r' {
				pendingCR = true
				continue
			}
			if err = writeByte(c); err != nil {
				return err
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF {
			if lineBytes > 0 {
				if err = finishLine(); err != nil {
					return err
				}
			}
			return dispatch()
		}
		if e != nil {
			return e
		}
	}
}

type sseLimitReader struct {
	r                io.Reader
	remaining, limit int64
}

func (r *sseLimitReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		n, e := r.r.Read(extra[:])
		if n > 0 {
			return 0, &ResponseLimitError{r.limit}
		}
		return 0, e
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, e := r.r.Read(p)
	r.remaining -= int64(n)
	return n, e
}

// ResponseText owns only the useful UTF-8 output, bounded independently of the
// wire event metadata. String adds one final copy, charged before allocation.
type ResponseText struct {
	bounds *BuildBounds
	writer BoundedWriter
	trace  *OwnedTrace
}

func NewResponseText(limit int64) (*ResponseText, error) {
	b, e := NewBuildBounds(limit, 0)
	if e != nil {
		return nil, e
	}
	return &ResponseText{bounds: b, writer: NewBoundedWriter(b, limit)}, nil
}
func NewResponseTextObserved(ctx context.Context, limit int64, label string) (*ResponseText, error) {
	b, e := providerBounds(ctx, limit)
	if e != nil {
		return nil, e
	}
	return &ResponseText{bounds: b, writer: NewBoundedWriter(b, limit), trace: NewOwnedTrace(ctx, "codec", label)}, nil
}
func (t *ResponseText) Append(s string) error {
	old := t.writer.data
	_, e := t.writer.WriteString(s)
	if e == nil && cap(old) < cap(t.writer.data) {
		t.trace.Set(int64(cap(old) + cap(t.writer.data)))
		runtime.KeepAlive(old)
		old = nil
		t.trace.Set(int64(cap(t.writer.data)))
	}
	return e
}
func (t *ResponseText) AppendBytes(p []byte) error {
	old := t.writer.data
	_, e := t.writer.Write(p)
	if e == nil && cap(old) < cap(t.writer.data) {
		t.trace.Set(int64(cap(old) + cap(t.writer.data)))
		runtime.KeepAlive(old)
		old = nil
		t.trace.Set(int64(cap(t.writer.data)))
	}
	return e
}
func (t *ResponseText) ClearObserved() { t.writer.Release(); t.trace.Clear() }
func (t *ResponseText) Len() int       { return len(t.writer.data) }
func (t *ResponseText) String() (string, error) {
	d := t.writer.Bytes()
	owner, e := t.bounds.OwnAllocation(int64(len(d)), 1)
	if e != nil {
		return "", e
	}
	if t.bounds.ctx != nil {
		if current, ok := t.bounds.ctx.Value(providerAllocationKey{}).(*providerAllocation); ok {
			current.mu.Lock()
			if current.output != nil {
				current.mu.Unlock()
				owner.Release()
				return "", &BuildCapacityError{"unfinished provider output"}
			}
			current.output = owner
			current.mu.Unlock()
		}
	}
	out := string(d)
	t.trace.Set(int64(cap(d) + len(out)))
	return out, nil
}

func FinishResponseText(ctx context.Context, t *ResponseText) (string, error) {
	var out string
	err := RunBuild(ctx, func() error { var e error; out, e = t.String(); return e })
	return out, err
}

// ResponseWithPartialText preserves only already parsed model text and known
// usage on the existing response+error path. The immutable string borrows the
// writer's credited backing, so handling a capacity failure allocates no copy.
// Returned text must be cleared before ReleaseProviderResponseOutput.
func ResponseWithPartialText(t *ResponseText, usage Usage) *Response {
	resp := &Response{Usage: usage}
	if t == nil || len(t.writer.data) == 0 {
		return resp
	}
	owner := t.writer.allocation.Transfer()
	if owner == nil {
		return resp
	}
	data := t.writer.data
	t.writer.data, t.writer.allocation = nil, nil
	if t.bounds.ctx != nil {
		if p, ok := t.bounds.ctx.Value(providerAllocationKey{}).(*providerAllocation); ok {
			p.mu.Lock()
			if p.output != nil {
				p.mu.Unlock()
				owner.Release()
				return resp
			}
			p.output = owner
			p.mu.Unlock()
		}
	}
	resp.Text = unsafe.String(unsafe.SliceData(data), len(data))
	return resp
}

func ProviderCallContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}
func EnsureJSONInstruction(s string) string {
	return s + "\n\nRespond with a single valid JSON object and nothing else."
}
func IsSSE(resp *http.Response) bool {
	return strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
}

func ProviderStreamError(provider, errorType string, headers http.Header) error {
	status := 0
	switch errorType {
	case "rate_limit_error", "rate_limit_exceeded":
		status = 429
	case "overloaded_error":
		status = 529
	case "authentication_error":
		status = 401
	case "invalid_request_error":
		status = 400
	}
	if status > 0 {
		return WrapHTTPError(provider+" stream", errors.New("provider stream error"), status, headers)
	}
	return fmt.Errorf("%s: provider stream error", provider)
}
