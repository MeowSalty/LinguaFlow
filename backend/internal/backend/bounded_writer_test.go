package backend

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestBoundedWriterLengthAddressableBoundary(t *testing.T) {
	cases := []struct {
		name              string
		current, incoming int
		want              int
		reject            bool
	}{
		{name: "empty"},
		{name: "maximum input", incoming: math.MaxInt, want: math.MaxInt},
		{name: "exact fit", current: math.MaxInt - 1, incoming: 1, want: math.MaxInt},
		{name: "empty at maximum", current: math.MaxInt, want: math.MaxInt},
		{name: "one byte overflow", current: math.MaxInt, incoming: 1, reject: true},
		{name: "large input overflow", current: 1, incoming: math.MaxInt, reject: true},
		{name: "both maximum", current: math.MaxInt, incoming: math.MaxInt, reject: true},
		{name: "negative current", current: -1, reject: true},
		{name: "negative input", incoming: -1, reject: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := boundedWriterLength(c.current, c.incoming)
			if c.reject {
				var capacity *BuildCapacityError
				if got != 0 || !errors.Is(err, ErrBuildCapacity) || !errors.As(err, &capacity) || capacity.Resource != "addressable bytes" || !capacity.Permanent() {
					t.Fatalf("got length=%d err=%v, want permanent addressable capacity error", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got length=%d err=%v, want %d", got, err, c.want)
			}
		})
	}
}

func TestBoundedWriterCapacityAddressableBoundary(t *testing.T) {
	cases := []struct {
		name              string
		current, required int
		limit             int64
		want              int
	}{
		{name: "minimum", required: 1, limit: 1024, want: 64},
		{name: "small limit", required: 1, limit: 16, want: 16},
		{name: "double", current: 64, required: 65, limit: 1024, want: 128},
		{name: "limit clips growth", current: 64, required: 65, limit: 100, want: 100},
		{name: "large incoming", current: 64, required: 1000, limit: 1024, want: 1000},
		{name: "last safe double", current: math.MaxInt / 2, required: math.MaxInt/2 + 1, limit: math.MaxInt64, want: math.MaxInt - 1},
		{name: "double would overflow", current: math.MaxInt/2 + 1, required: math.MaxInt/2 + 2, limit: math.MaxInt64, want: math.MaxInt},
		{name: "exact addressable capacity", current: math.MaxInt - 1, required: math.MaxInt, limit: math.MaxInt64, want: math.MaxInt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := boundedWriterCapacity(c.current, c.required, c.limit)
			if got != c.want || got < c.required || int64(got) > c.limit {
				t.Fatalf("got capacity=%d, want %d within [%d,%d]", got, c.want, c.required, c.limit)
			}
		})
	}
}

func TestBoundedWriterPrepareRejectsLengthBeforeAllocation(t *testing.T) {
	b, err := NewBuildBounds(1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	w := NewBoundedWriter(b, math.MaxInt64)
	if _, err := w.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	before := b.Snapshot()
	beforeCapacity := cap(w.Bytes())
	err = w.prepareWrite(math.MaxInt)
	var capacity *BuildCapacityError
	if !errors.Is(err, ErrBuildCapacity) || !errors.As(err, &capacity) {
		t.Fatalf("huge incoming length accepted: %v", err)
	}
	wantResource := "addressable bytes"
	if int64(math.MaxInt) == math.MaxInt64 {
		wantResource = "encoded bytes" // Existing business-limit check wins on 64-bit.
	}
	if capacity.Resource != wantResource || string(w.Bytes()) != "x" || cap(w.Bytes()) != beforeCapacity || b.Snapshot() != before {
		t.Fatalf("rejection resource/state: err=%v bounds=%+v", err, b.Snapshot())
	}
}

func TestBoundedWriterMethodsGrowAndRejectWithoutMutation(t *testing.T) {
	methods := map[string]func(*BoundedWriter, string) (int, error){
		"Write":       func(w *BoundedWriter, s string) (int, error) { return w.Write([]byte(s)) },
		"WriteString": (*BoundedWriter).WriteString,
	}
	for name, write := range methods {
		t.Run(name, func(t *testing.T) {
			b, err := NewBuildBounds(1024, 0)
			if err != nil {
				t.Fatal(err)
			}
			w := NewBoundedWriter(b, 96)
			for _, s := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 16)} {
				if n, err := write(w, s); err != nil || n != len(s) {
					t.Fatalf("write n=%d err=%v", n, err)
				}
			}
			want := strings.Repeat("a", 40) + strings.Repeat("b", 40) + strings.Repeat("c", 16)
			if string(w.Bytes()) != want || cap(w.Bytes()) != 96 || b.Snapshot().TextBytes != 192 {
				t.Fatalf("growth bytes=%q capacity=%d bounds=%+v", w.Bytes(), cap(w.Bytes()), b.Snapshot())
			}
			before := b.Snapshot()
			if n, err := write(w, "x"); n != 0 || !errors.Is(err, ErrBuildCapacity) || string(w.Bytes()) != want || cap(w.Bytes()) != 96 || b.Snapshot() != before {
				t.Fatalf("limit rejection n=%d err=%v bounds=%+v", n, err, b.Snapshot())
			}

			w = NewBoundedWriter(b, 10000)
			if _, err := write(w, "retained"); err != nil {
				t.Fatal(err)
			}
			before = b.Snapshot()
			beforeCapacity := cap(w.Bytes())
			if n, err := write(w, strings.Repeat("z", 8192)); n != 0 || !errors.Is(err, ErrBuildCapacity) || string(w.Bytes()) != "retained" || cap(w.Bytes()) != beforeCapacity || b.Snapshot() != before {
				t.Fatalf("budget rejection n=%d err=%v bounds=%+v", n, err, b.Snapshot())
			}
		})
	}
}
