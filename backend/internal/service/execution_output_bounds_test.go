package service

import (
	"context"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"math"
	"reflect"
	"testing"
)

func TestExecutionOutputTypedSmallFrameOwnsMeasurementAndEncode(t *testing.T) {
	root := inputTestRoot(t)
	ctx := WithExecutionInputBudget(context.Background(), root, resourcebudget.Scope{ActorID: 7, CallID: "typed-frame"}, DefaultExecutionInputBytes)
	fields := make([]reflect.StructField, 64)
	for i := range fields {
		fields[i] = reflect.StructField{Name: fmt.Sprintf("Field%d", i), Type: reflect.TypeFor[bool](), Tag: reflect.StructTag(fmt.Sprintf(`json:"f%d,omitempty"`, i))}
	}
	payload := reflect.New(reflect.StructOf(fields)).Elem().Interface()
	raw, lease, err := MarshalExecutionOutput(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Fatalf("frame %s", raw)
	}
	v := root.Snapshot(7).Instance
	allocation, err := executionOutputAllocationCredit(int64(cap(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if v.PayloadBytes != allocation || v.PayloadBytes <= int64(cap(raw)) || v.BuildWorkers != 0 || v.PrepareOwners != 0 {
		t.Fatalf("scratch did not retire at build end: %+v", v)
	}
	clear(raw)
	raw = nil
	lease.Close()
	if v = root.Snapshot(7).Instance; v != (resourcebudget.Vector{}) {
		t.Fatalf("frame retained: %+v", v)
	}
}

func TestExecutionOutputBoundsCheckedAndFixedNodeLimit(t *testing.T) {
	for _, n := range []int64{-1, math.MaxInt64, 6*DefaultExecutionInputBytes + 1} {
		if _, _, err := executionOutputBounds(n); err == nil {
			t.Fatalf("invalid upper %d accepted", n)
		}
	}
	for _, n := range []int64{0, 2, 4 << 20, 6 * DefaultExecutionInputBytes} {
		b, credit, err := executionOutputBounds(n)
		if err != nil {
			t.Fatal(err)
		}
		s := b.Snapshot()
		if s.NodeLimit > 65536 || credit != 2*n+s.TextLimit+s.NodeLimit*1024 {
			t.Fatalf("unchecked arena n%d %+v credit%d", n, s, credit)
		}
	}
}
