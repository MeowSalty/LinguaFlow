package backend

import (
	"errors"
	"reflect"
	"testing"
)

func TestRubyHandoffClonePreservesTypesAndPrivateBacking(t *testing.T) {
	type item struct {
		Source  string
		Indices []int
	}
	in := map[string]any{"items": []item{{Source: "source", Indices: []int{1, 2}}}, "ints": []int{3, 4}, "scalar": 5, "bytes": []byte("abc")}
	b, _ := NewBuildBounds(4096, 4096)
	copied, err := b.Clone(in)
	if err != nil {
		t.Fatal(err)
	}
	out := copied.(map[string]any)
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("types lost %T %T", out["items"], out["ints"])
	}
	in["items"].([]item)[0].Indices[0] = 99
	in["bytes"].([]byte)[0] = 'x'
	in["ints"].([]int)[0] = 100
	if out["items"].([]item)[0].Indices[0] != 1 || string(out["bytes"].([]byte)) != "abc" || out["ints"].([]int)[0] != 3 {
		t.Fatal("metadata backing aliases original")
	}
}
func TestRubyHandoffCloneRejectsCyclesAndHugeRecords(t *testing.T) {
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	b, _ := NewBuildBounds(4096, 4096)
	if out, err := b.Clone(cycle); out != nil || !errors.Is(err, ErrBuildCapacity) {
		t.Fatalf("cycle %v", err)
	}
	type large struct{ Data [2048]byte }
	b, _ = NewBuildBounds(4096, 4096)
	if out, err := b.Clone(&large{}); out != nil || !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("large pointer cloned before credit")
	}
}
