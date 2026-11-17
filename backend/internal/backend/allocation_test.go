package backend

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAllocationPolicyOptionalAndMeasured(t *testing.T) {
	if _, err := NewBuildBounds(1, 1, -1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	b, err := NewBuildBounds(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.ReserveAllocation(1, 2048); err != nil {
		t.Fatal("implicit record or byte quota", err)
	}
	if b.Snapshot().MeasuredRequiredBytes == 0 || b.Snapshot().AllocationLimit != 0 {
		t.Fatal(b.Snapshot())
	}
	b, _ = NewBuildBounds(1, 1, 16)
	if err = b.ReserveAllocation(1, 17); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	var capacity *AllocationCapacityError
	if !errors.As(err, &capacity) || capacity.Used != 0 || capacity.Requested != 17 || capacity.Limit != 16 || b.Snapshot().LiveBytes != 0 {
		t.Fatal(err, b.Snapshot())
	}
}

func TestAllocationOwnersSeparatePeakFromFlow(t *testing.T) {
	b, _ := NewBuildBounds(1, 1, 16)
	for range 1000 {
		owner, err := b.OwnAllocation(1, 1)
		if err != nil {
			t.Fatal(err)
		}
		next := owner.Transfer()
		owner.Release()
		if b.Snapshot().LiveBytes != 1 {
			t.Fatal("transfer refunded live object")
		}
		next.Release()
		next.Release()
	}
	s := b.Snapshot()
	if s.LiveBytes != 0 || s.MeasuredRequiredBytes != 1 || s.AllocationBytes != 1000 {
		t.Fatal(s)
	}
}

func TestAllocationCloseRetiredEndsBuildAndLateHandlesAreIdempotent(t *testing.T) {
	var released int64
	ctx := WithBuildAllocation(context.Background(), 0, nil, func(n int64) { released += n })
	b, _ := NewBuildBoundsFromContext(ctx, 1, 1)
	owner, err := b.OwnAllocation(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.ReserveAllocation(1, 17); err != nil {
		t.Fatal(err)
	}
	before := b.Snapshot()
	b.CloseRetired()
	b.CloseRetired()
	owner.Release()
	if released != before.LiveBytes || b.Snapshot().LiveBytes != 0 || b.Snapshot().MeasuredRequiredBytes != before.MeasuredRequiredBytes || b.Snapshot().AllocationBytes != before.AllocationBytes {
		t.Fatal(released, b.Snapshot(), before)
	}
	if owner.Transfer() != nil {
		t.Fatal("retired backing transferred")
	}
	if err = b.ReserveAllocation(0, 1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("terminal build restarted", err)
	}
}

func TestAllocationBorrowedMeasureHasNoNodeFee(t *testing.T) {
	values := make([]int, 70000)
	b, _ := NewBuildBounds(200000, 0, 16)
	n, err := b.MeasureJSON(values)
	if err != nil || n != 140001 || b.Snapshot().MeasuredRequiredBytes != 16 || b.Snapshot().AllocationBytes != 16 {
		t.Fatal(n, err, b.Snapshot())
	}
	data := []byte("[" + strings.TrimSuffix(strings.Repeat("0,", 70000), ",") + "]")
	b, _ = NewBuildBounds(int64(len(data)), int64(len(data)))
	var out []int
	if err = b.DecodeJSON(data, &out); err != nil || len(out) != len(values) {
		t.Fatal(err, len(out))
	}
}

func TestAllocationCloneAndEncoderMeterActualCapacity(t *testing.T) {
	type record struct {
		Text    string
		Numbers []int
		Large   [2048]byte
	}
	in := map[string]any{"record": record{Text: "text", Numbers: []int{1, 2}}, "nested": map[string]any{"text": "value"}}
	required, err := ValueAllocationBytes(in)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewBuildBounds(20000, 20000)
	out, err := b.Clone(in)
	if err != nil || !reflect.DeepEqual(in, out) || b.Snapshot().LiveBytes < required {
		t.Fatal(err, b.Snapshot(), required)
	}
	b.CloseRetired()
	b, _ = NewBuildBounds(20000, 20000)
	n, err := b.MeasureJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	before := b.Snapshot().AllocationBytes
	data, err := b.EncodeJSONMeasured(in, n, n)
	outputCredit, err2 := AllocationBytes(n, 1)
	if err != nil || err2 != nil || int64(len(data)) != n || b.Snapshot().LiveBytes < outputCredit || b.Snapshot().AllocationBytes <= before {
		t.Fatal(err, b.Snapshot(), n)
	}
	data = nil
	b.CloseRetired()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal(b.Snapshot())
	}
}

func TestAllocationContextCallbacksRejectAtomically(t *testing.T) {
	denied := errors.New("root full")
	ctx := WithBuildAllocation(context.Background(), 0, func(int64) error { return denied }, nil)
	b, _ := NewBuildBoundsFromContext(ctx, 1, 1)
	before := b.Snapshot()
	if err := b.ReserveAllocation(0, 8); err != nil || b.Snapshot() != before {
		t.Fatal("zero capacity called allocation hook", err, b.Snapshot())
	}
	if err := b.ReserveAllocation(1, 8); !errors.Is(err, denied) || b.Snapshot() != before {
		t.Fatal(err, b.Snapshot())
	}
	reserved, released := int64(0), int64(0)
	ctx = WithBuildAllocation(context.Background(), 0, func(n int64) error { reserved += n; return nil }, func(n int64) { released += n })
	b, _ = NewBuildBoundsFromContext(ctx, 1, 1)
	owner, err := b.OwnAllocation(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	owner.Release()
	if reserved != 1 || released != reserved {
		t.Fatal(reserved, released)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ = NewBuildBoundsFromContext(ctx, 1, 1)
	if !errors.Is(b.CheckContext(), context.Canceled) || !errors.Is(b.Enter(), context.Canceled) {
		t.Fatal("context ignored")
	}
}

func TestMeasuredWireEnvelopeContextRetiresActualScratch(t *testing.T) {
	var used, peak int64
	ctx := WithBuildAllocation(context.Background(), 0, func(n int64) error { used += n; peak = max(peak, used); return nil }, func(n int64) { used -= n })
	n, err := MeasureWireBuildContext(ctx, func(b *BuildBounds) (any, error) {
		if err := ReserveSlice[any](b, 1); err != nil {
			return nil, err
		}
		return map[string]any{"content": "borrowed"}, nil
	})
	if err != nil || n != 22 || used != 0 || peak == 0 {
		t.Fatal(n, err, used, peak)
	}
}

func TestAllocationWriterChecksOverlappingGrowthPeak(t *testing.T) {
	b, _ := NewBuildBounds(200, 0, 128)
	w := NewBoundedWriter(b, 200)
	if _, err := w.WriteString(strings.Repeat("x", 40)); err != nil {
		t.Fatal(err)
	}
	before := b.Snapshot()
	if _, err := w.WriteString(strings.Repeat("y", 40)); !errors.Is(err, ErrBuildCapacity) || len(w.Bytes()) != 40 || b.Snapshot() != before {
		t.Fatal(err, b.Snapshot())
	}
	w.Release()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal(b.Snapshot())
	}
}

func TestAllocationEncodingSiblingScratchRetiresBetweenValues(t *testing.T) {
	value := map[string]any{"text": "borrowed"}
	values := make([]map[string]any, 70000)
	for i := range values {
		values[i] = value
	}
	current, peak, flow := int64(0), int64(0), int64(0)
	ctx := WithBuildAllocation(context.Background(), 128, func(n int64) error { current += n; flow += n; peak = max(peak, current); return nil }, func(n int64) { current -= n })
	b, err := NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	measured, err := b.MeasureJSON(values)
	if err != nil || measured != 1400001 || current != 0 || peak > 128 || flow <= peak {
		t.Fatal(measured, err, current, peak, flow)
	}
	b.CloseRetired()
}

func TestAllocationJSONInterfaceUsesConcreteHolderWidth(t *testing.T) {
	for _, raw := range []string{"true", "false", "{}", "[]", "\"x\"", "1"} {
		t.Run(raw, func(t *testing.T) {
			var expected int64
			switch raw {
			case "true", "false":
				expected = int64(reflect.TypeFor[bool]().Size())
			case "{}":
				expected = int64(reflect.TypeFor[map[string]any]().Size())
			case "[]":
				expected = 2 * int64(reflect.TypeFor[[]any]().Size())
			default:
				expected = int64(reflect.TypeFor[string]().Size()) + 1
			}
			limit := expected
			if raw == "\"x\"" {
				limit++ // Private string and its explicit unescape scratch coexist.
			}
			b, err := NewBuildBounds(32, 32, limit)
			if err != nil {
				t.Fatal(err)
			}
			value, err := BorrowWireJSON(b, []byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			var decoded any
			// Object/array decoding uses the same bounded decoder with its
			// explicitly provided single borrowed node, so only holders count.
			node := []jsonNode{{start: 0, end: len(raw), child: -1, next: -1, kind: value.Kind()}}
			if err = b.decodeValue([]byte(raw), node, 0, reflect.ValueOf(&decoded).Elem()); err != nil {
				t.Fatal(err, b.Snapshot())
			}
			live := int64(0)
			var want any
			switch raw {
			case "true":
				want = true
			case "false":
				want = false
			case "{}":
				want = map[string]any{}
			case "[]":
				want = []any{}
			case "\"x\"":
				want, live = "x", 1
			case "1":
				want, live = json.Number("1"), 1
			}
			if b.Snapshot().LiveBytes != live || b.Snapshot().MeasuredRequiredBytes != limit || !reflect.DeepEqual(decoded, want) {
				t.Fatal("concrete temporary holder width or lifetime changed", decoded, want, live, limit, b.Snapshot())
			}
			decoded = nil
			b.CloseRetired()
			if b.Snapshot().LiveBytes != 0 {
				t.Fatal(b.Snapshot())
			}
		})
	}
}

func TestAllocationJSONCopiedHoldersDoNotAccumulateAcrossElements(t *testing.T) {
	const count = 100
	raw := "[" + strings.TrimSuffix(strings.Repeat("true,", count), ",") + "]"
	nodes := make([]jsonNode, count+1)
	nodes[0] = jsonNode{start: 0, end: len(raw), child: 1, next: -1, count: count, kind: '['}
	for i := 0; i < count; i++ {
		nodes[i+1] = jsonNode{start: 1 + 5*i, end: 5 + 5*i, child: -1, next: i + 2, kind: 't'}
	}
	nodes[count].next = -1
	backing := count * int64(reflect.TypeFor[any]().Size())
	header := int64(reflect.TypeFor[[]any]().Size())
	peak := backing + 2*header
	for _, fail := range []bool{false, true} {
		limit := peak
		if fail {
			limit--
		}
		b, err := NewBuildBounds(int64(len(raw)), int64(len(raw)), limit)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		err = b.decodeValue([]byte(raw), nodes, 0, reflect.ValueOf(&decoded).Elem())
		if fail {
			if !errors.Is(err, ErrBuildCapacity) || decoded != nil || b.Snapshot().LiveBytes != 0 {
				t.Fatal("failed holder/backing acquisition was not retired", err, decoded, b.Snapshot())
			}
		} else {
			if err != nil {
				t.Fatal("exited bool holders exhausted a legal slice", err, b.Snapshot())
			}
			values := decoded.([]any)
			if len(values) != count || b.Snapshot().LiveBytes != backing || b.Snapshot().MeasuredRequiredBytes != peak {
				t.Fatal("output backing lost or holders accumulated", b.Snapshot())
			}
			for _, value := range values {
				if value != true {
					t.Fatal("decoded value changed", value)
				}
			}
			clear(values)
			values = nil
		}
		decoded = nil
		b.CloseRetired()
		if b.Snapshot().LiveBytes != 0 {
			t.Fatal(b.Snapshot())
		}
	}
}

func TestAllocationMapDoesNotGuessRuntimeBucketCapacity(t *testing.T) {
	b, _ := NewBuildBounds(1, 1, 1)
	m, err := MakeMap[string, int](b, make([]string, 70000))
	if err != nil || m == nil || b.Snapshot().LiveBytes != 0 {
		t.Fatal(err, b.Snapshot())
	}
}

func TestNumericTokenUsesInputEnvelopeWithoutHiddenLengthCap(t *testing.T) {
	text := "0." + strings.Repeat("0", 4000) + "1"
	b, _ := NewBuildBounds(int64(len(text)), int64(len(text)), 16)
	n, err := b.MeasureJSON(json.Number(text))
	if err != nil || n != int64(len(text)) || b.Snapshot().MeasuredRequiredBytes != 16 {
		t.Fatal(n, err, b.Snapshot())
	}
	b, _ = NewBuildBounds(int64(len(text)), int64(len(text)))
	data, err := b.EncodeJSONMeasured(json.Number(text), n, n)
	if err != nil || string(data) != text {
		t.Fatal(err)
	}
	var out float64
	if err = b.DecodeJSON(data, &out); err != nil || out != 0 {
		t.Fatal(err, out)
	}
	for _, invalid := range []string{"", "-", "01", "1.", "1e", "1e+", "+1", "1 2"} {
		if validJSONNumber(invalid) {
			t.Fatal(invalid)
		}
	}
}
