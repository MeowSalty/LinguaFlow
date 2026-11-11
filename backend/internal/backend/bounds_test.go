package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestBuildBoundsArithmeticAndAllocationGuards(t *testing.T) {
	for _, c := range [][2]int64{{-1, 0}, {0, -1}, {math.MaxInt64, 1}, {math.MaxInt64/8 + 1, 0}} {
		if _, err := NewBuildBounds(c[0], c[1]); !errors.Is(err, ErrBuildCapacity) {
			t.Fatalf("budget accepted %v: %v", c, err)
		}
	}
	b, _ := NewBuildBounds(1024, 0)
	if err := b.ReserveText(1, 8193); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("capacity not charged")
	}
	if err := b.ReserveNodes(1, MaxNodeAllocationBytes+1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("unbounded node")
	}
	if b.Snapshot().TextBytes != 0 || b.Snapshot().Nodes != 0 {
		t.Fatal("rejected charge mutated budget")
	}
	if unsafe.Sizeof(jsonNode{}) > uintptr(MaxNodeAllocationBytes) || unsafe.Sizeof(reflect.Value{}) > uintptr(MaxNodeAllocationBytes) {
		t.Fatal("arena records exceed E")
	}
	// Map values >128 bytes are indirect. The largest inline key/value group
	// (8 slots, controls/header and allocator rounding) is <1024 per entry.
	group := int64(8*(128+128) + 8 + 64)
	if (group+255)/8 > MaxNodeAllocationBytes {
		t.Fatal("map bucket credit not covered")
	}
}

func TestBoundedJSONRoundTripAndExactWireMeasurement(t *testing.T) {
	type nested struct {
		Text    string          `json:"text"`
		At      time.Time       `json:"at"`
		Numbers []int64         `json:"numbers"`
		Raw     json.RawMessage `json:"raw"`
		Meta    map[string]any  `json:"meta"`
	}
	in := nested{Text: "\x00\n\\\"汉字😀", At: time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC), Numbers: []int64{0, 1, -9}, Raw: json.RawMessage(`{"escaped":"\ud83d\ude00"}`), Meta: map[string]any{"array": []any{true, nil, "value"}, "number": json.Number("12")}}
	b, _ := NewBuildBounds(4096, 4096)
	n, err := b.MeasureJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	data, err := b.EncodeJSON(in, n)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != n || !json.Valid(data) {
		t.Fatalf("invalid measured JSON %q", data)
	}
	b, _ = NewBuildBounds(4096, 4096)
	var out nested
	if err = b.DecodeJSON(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Text != in.Text || !out.At.Equal(in.At) || !reflect.DeepEqual(out.Numbers, in.Numbers) || !bytes.Equal(out.Raw, in.Raw) {
		t.Fatalf("roundtrip %v", out)
	}
	b, _ = NewBuildBounds(4096, 4096)
	if _, err = b.EncodeJSON(in, n-1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("over-credit encode")
	}
}

func TestBoundedJSONControlEscapesMatchCanonicalMarshal(t *testing.T) {
	controls := make([]byte, 32)
	for i := range controls {
		controls[i] = byte(i)
	}
	values := []string{string(controls), "\"\\<>&汉字\u2028\u2029"}
	for i := range controls {
		values = append(values, string(controls[i:i+1]))
	}
	for _, value := range values {
		want, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if n := MeasureJSONStringBytes([]byte(value)); n != int64(len(want)) {
			t.Fatalf("byte control measurement %q got=%d want=%d", value, n, len(want))
		}
		for _, input := range []any{value, WireString{value[:len(value)/2], value[len(value)/2:]}} {
			b, _ := NewBuildBounds(4096, 4096)
			n, err := b.MeasureJSON(input)
			if err != nil || n != int64(len(want)) {
				t.Fatalf("control measurement %q got=%d want=%d err=%v", value, n, len(want), err)
			}
			got, err := b.EncodeJSON(input, n)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("control encoding %q got=%s want=%s err=%v", value, got, want, err)
			}
		}
	}
}

func TestBoundedJSONRejectsDepthNodesBeforeAllocation(t *testing.T) {
	for _, data := range []string{strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65), `{"x":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + "}", `[1,]`, `{"x":true,}`, `"\uqqqq"`, `{"x":01}`} {
		b, _ := NewBuildBounds(4096, 4096)
		var out any
		if err := b.DecodeJSON([]byte(data), &out); err == nil {
			t.Fatalf("accepted %s", data)
		}
		if out != nil {
			t.Fatal("invalid preflight allocated destination")
		}
	}
	b, _ := NewBuildBounds(8, 0)
	var out []int
	if err := b.DecodeJSON([]byte(`[0,1,2]`), &out); !errors.Is(err, ErrBuildCapacity) || out != nil {
		t.Fatalf("node preflight %v", err)
	}
	type large struct {
		Data [2048]byte `json:"data"`
	}
	b, _ = NewBuildBounds(4096, 4096)
	var dst *large
	if err := b.DecodeJSON([]byte(`{"data":[]}`), &dst); !errors.Is(err, ErrBuildCapacity) || dst != nil {
		t.Fatal("large pointer allocated before credit")
	}
}

func TestBoundedWriterCannotCopyPromptBeforeCredit(t *testing.T) {
	b, _ := NewBuildBounds(1024, 0)
	w := NewBoundedWriter(b, 16)
	if _, err := w.WriteString(strings.Repeat("x", 17)); !errors.Is(err, ErrBuildCapacity) || len(w.Bytes()) != 0 || b.Snapshot().TextBytes != 0 {
		t.Fatal("over-limit writer allocated")
	}
	if _, err := w.WriteString("1234567890123456"); err != nil {
		t.Fatal(err)
	}
	if b.Snapshot().TextBytes != 16 || cap(w.Bytes()) != 16 {
		t.Fatal("writer capacity accounting")
	}
}

func TestBoundedJSONRawUsesRemainingDepthAndAggregateNodes(t *testing.T) {
	raw := json.RawMessage(strings.Repeat("[", 63) + "0" + strings.Repeat("]", 63))
	b, _ := NewBuildBounds(4096, 4096)
	if _, err := b.EncodeJSON([]json.RawMessage{raw}, 4096); !errors.Is(err, ErrBuildCapacity) {
		t.Fatalf("outer plus raw depth accepted: %v", err)
	}
	b, _ = NewBuildBounds(32, 0) // Five JSON nodes: root + two three-node raws exceed it.
	if _, err := b.MeasureJSON([]json.RawMessage{json.RawMessage(`[0,1]`), json.RawMessage(`[2,3]`)}); !errors.Is(err, ErrBuildCapacity) {
		t.Fatalf("aggregate raw nodes accepted: %v", err)
	}
	b, _ = NewBuildBounds(4096, 4096)
	if _, err := b.EncodeJSON(raw, 4096); err != nil {
		t.Fatalf("valid depth64 raw rejected: %v", err)
	}
}

func TestBoundedJSONNumberPreflightAndTime(t *testing.T) {
	for _, n := range []string{"true", "null", "{}", `"x"`, "NaN", "Inf", "+1", "01", "1e", strings.Repeat("9", 65)} {
		b, _ := NewBuildBounds(4096, 4096)
		if _, err := b.EncodeJSON(json.Number(n), 4096); err == nil {
			t.Fatalf("invalid Number accepted %q", n)
		}
		if b.Snapshot().TextBytes != 0 {
			t.Fatal("invalid Number copied before rejection")
		}
	}
	b, _ := NewBuildBounds(4096, 4096)
	if _, err := b.EncodeJSON(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), 4096); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("unbounded RFC3339 year accepted")
	}
	if c, err := textAllocationCredit(32768); err != nil || c <= 32768 {
		t.Fatal("pointer-array malloc header not covered")
	}
}
