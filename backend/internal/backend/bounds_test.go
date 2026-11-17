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
)

func TestBuildBoundsArithmeticAndAllocationGuards(t *testing.T) {
	for _, c := range [][2]int64{{-1, 0}, {0, -1}, {math.MaxInt64, 1}} {
		if _, err := NewBuildBounds(c[0], c[1]); !errors.Is(err, ErrBuildCapacity) {
			t.Fatalf("budget accepted %v: %v", c, err)
		}
	}
	b, _ := NewBuildBounds(1024, 0, 8192)
	if err := b.ReserveText(1, 8193); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("capacity not charged")
	}
	if err := b.ReserveAllocation(math.MaxInt64, 2); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("allocation overflow")
	}
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal("rejected charge mutated budget")
	}
	if err := b.ReserveAllocation(1, 2048); err != nil {
		t.Fatal("large record rejected", err)
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

func TestBoundedJSONRejectsDepthAndExplicitCapacityBeforeAllocation(t *testing.T) {
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
	b, _ := NewBuildBounds(8, 0, 1)
	var out []int
	if err := b.DecodeJSON([]byte(`[0,1,2]`), &out); !errors.Is(err, ErrBuildCapacity) || out != nil {
		t.Fatalf("arena preflight %v", err)
	}
	type large struct {
		Data [2048]byte `json:"data"`
	}
	b, _ = NewBuildBounds(16384, 16384)
	in := large{}
	data, err := b.EncodeJSON(in, 16384)
	if err != nil {
		t.Fatal(err)
	}
	var dst *large
	if err := b.DecodeJSON(data, &dst); err != nil || dst == nil {
		t.Fatal("large record rejected", err)
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
	if b.Snapshot().LiveBytes != 16 || cap(w.Bytes()) != 16 {
		t.Fatal("writer capacity accounting")
	}
}

func TestBoundedJSONRawUsesRemainingDepthWithoutGraphQuota(t *testing.T) {
	raw := json.RawMessage(strings.Repeat("[", 63) + "0" + strings.Repeat("]", 63))
	b, _ := NewBuildBounds(4096, 4096)
	if _, err := b.EncodeJSON([]json.RawMessage{raw}, 4096); !errors.Is(err, ErrBuildCapacity) {
		t.Fatalf("outer plus raw depth accepted: %v", err)
	}
	b, _ = NewBuildBounds(32, 0)
	if _, err := b.MeasureJSON([]json.RawMessage{json.RawMessage(`[0,1]`), json.RawMessage(`[2,3]`)}); err != nil {
		t.Fatalf("borrowed raw graph rejected: %v", err)
	}
	b, _ = NewBuildBounds(4096, 4096)
	if _, err := b.EncodeJSON(raw, 4096); err != nil {
		t.Fatalf("valid depth64 raw rejected: %v", err)
	}
}

func TestBoundedJSONNumberPreflightAndTime(t *testing.T) {
	for _, n := range []string{"true", "null", "{}", `"x"`, "NaN", "Inf", "+1", "01", "1e"} {
		b, _ := NewBuildBounds(4096, 4096)
		if _, err := b.EncodeJSON(json.Number(n), 4096); err == nil {
			t.Fatalf("invalid Number accepted %q", n)
		}
		if b.Snapshot().TextBytes != 0 {
			t.Fatal("invalid Number copied before rejection")
		}
	}
	longNumber := strings.Repeat("9", 65)
	numberBounds, _ := NewBuildBounds(4096, 4096)
	if data, err := numberBounds.EncodeJSON(json.Number(longNumber), 4096); err != nil || string(data) != longNumber {
		t.Fatalf("legal borrowed Number changed: %s %v", data, err)
	}
	b, _ := NewBuildBounds(4096, 4096)
	if _, err := b.EncodeJSON(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), 4096); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("unbounded RFC3339 year accepted")
	}
	if c, err := textAllocationCredit(32768); err != nil || c != 32768 {
		t.Fatal("explicit payload capacity changed by allocator overhead", c, err)
	}
}
