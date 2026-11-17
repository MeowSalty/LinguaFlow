package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderActualCapacityStandaloneHasNoUnpaidCredit(t *testing.T) {
	var live int64
	ctx := WithBuildAllocation(context.Background(), 16, func(n int64) error { live += n; return nil }, func(n int64) { live -= n })
	ctx, err := WithProviderResponseAllocation(ctx, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := OwnProviderResponseAllocation(ctx, 2, 7)
	if err != nil || live != 14 {
		t.Fatal(err, live)
	}
	if _, err = OwnProviderResponseAllocation(ctx, 3, 1); !errors.Is(err, ErrBuildCapacity) || live != 14 {
		t.Fatal(err, live)
	}
	if err = CloseProviderResponseAllocation(ctx); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	owner.Release()
	if live != 0 {
		t.Fatal(live)
	}
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = WithProviderResponseAllocation(ctx, 1<<20); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("closed meter reused", err)
	}
}

func TestProviderActualWireEncoderRetainsOnlyOutput(t *testing.T) {
	ctx, err := WithProviderResponseAllocation(context.Background(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	data, release, err := EncodeWireBuild(ctx, func(b *BuildBounds) (any, error) {
		if err := ReserveSlice[any](b, 1); err != nil {
			return nil, err
		}
		return map[string]any{"value": []any{"borrowed"}}, nil
	})
	if err != nil || ProviderResponseSnapshot(ctx).LiveBytes != int64(cap(data)) {
		t.Fatal(err, ProviderResponseSnapshot(ctx), cap(data))
	}
	data = nil
	release()
	release()
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProviderActualErrorBodyUsesSharedCapacity(t *testing.T) {
	ctx, err := WithProviderResponseAllocation(WithBuildAllocation(context.Background(), 64, nil, nil), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = GuardProviderErrors(req, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	if err == nil {
		t.Fatal("missing HTTP error")
	}
	if s := ProviderResponseSnapshot(ctx); s.LiveBytes != 0 || s.MeasuredRequiredBytes != 64 {
		t.Fatal(s)
	}
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestProviderBorrowedResponseReleasesAllButReturnedOutput(t *testing.T) {
	body := `{"unknown":[` + strings.TrimSuffix(strings.Repeat("0,", 70000), ",") + `],"message":{"content":"a\n\uD83D\uDE00"}}`
	limit := int64(len(body))
	ctx, err := WithProviderResponseAllocation(context.Background(), limit)
	if err != nil {
		t.Fatal(err)
	}
	raw, release, err := ReadProviderBounded(ctx, strings.NewReader(body), limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	view, err := BorrowWireJSONContext(ctx, raw, limit)
	if err != nil {
		t.Fatal(err)
	}
	text, err := NewResponseTextObserved(ctx, limit, "test")
	if err != nil {
		t.Fatal(err)
	}
	content, err := view.Path("message", "content")
	if err != nil {
		t.Fatal(err)
	}
	if err = content.AppendString(text); err != nil {
		t.Fatal(err)
	}
	out, err := FinishResponseText(ctx, text)
	if err != nil || out != "a\n😀" {
		t.Fatal(out, err)
	}
	view = WireJSONValue{}
	content = WireJSONValue{}
	raw = nil
	release()
	text.ClearObserved()
	want, _ := AllocationBytes(int64(len(out)), 1)
	snapshot := ProviderResponseSnapshot(ctx)
	if snapshot.LiveBytes != want {
		t.Fatal(snapshot, want)
	}
	out = ""
	ReleaseProviderResponseOutput(ctx)
	if s := ProviderResponseSnapshot(ctx); s.LiveBytes != 0 {
		t.Fatal(s)
	}
}

func TestProviderResponseReadFailureReleasesBacking(t *testing.T) {
	ctx, err := WithProviderResponseAllocation(context.Background(), 16)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := ReadProviderBounded(ctx, strings.NewReader(strings.Repeat("x", 17)), 16, nil)
	release()
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatal(err)
	}
	if s := ProviderResponseSnapshot(ctx); s.LiveBytes != 0 {
		t.Fatal(s)
	}
}

func TestProviderSmallResponseKeepsActualBackingSmall(t *testing.T) {
	const limit = 16 << 20
	ctx, err := WithProviderResponseAllocation(context.Background(), limit)
	if err != nil {
		t.Fatal(err)
	}
	raw, release, err := ReadProviderBounded(ctx, strings.NewReader("small"), limit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "small" || cap(raw) > 4096 {
		t.Fatalf("len=%d cap=%d", len(raw), cap(raw))
	}
	raw = nil
	release()
	if s := ProviderResponseSnapshot(ctx); s.LiveBytes != 0 {
		t.Fatal(s)
	}
}

func TestProviderSSEFlowDoesNotBecomeLivePeak(t *testing.T) {
	event := "data: {\"content\":\"x\"}\n\n"
	limit := int64(len(event) * 200)
	ctx, err := WithProviderResponseAllocation(context.Background(), limit)
	if err != nil {
		t.Fatal(err)
	}
	text, err := NewResponseTextObserved(ctx, limit, "test")
	if err != nil {
		t.Fatal(err)
	}
	err = ReadSSEContext(ctx, strings.NewReader(strings.Repeat(event, 200)), limit, func(raw []byte) error {
		view, err := BorrowWireJSONContext(ctx, raw, limit)
		if err != nil {
			return err
		}
		v, err := view.Field("content")
		if err != nil {
			return err
		}
		return v.AppendString(text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.Len() != 200 {
		t.Fatal(text.Len())
	}
	text.ClearObserved()
	s := ProviderResponseSnapshot(ctx)
	if s.LiveBytes != 0 || s.AllocationBytes <= s.MeasuredRequiredBytes {
		t.Fatal(s)
	}
}
