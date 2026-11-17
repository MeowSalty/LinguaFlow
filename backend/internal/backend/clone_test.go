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
func TestRubyHandoffCloneRejectsCyclesAndAcceptsLargeRecords(t *testing.T) {
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	b, _ := NewBuildBounds(4096, 4096)
	if out, err := b.Clone(cycle); out != nil || !errors.Is(err, ErrBuildCapacity) {
		t.Fatalf("cycle %v", err)
	}
	type large struct{ Data [2048]byte }
	b, _ = NewBuildBounds(4096, 4096)
	if out, err := b.Clone(&large{}); out == nil || err != nil {
		t.Fatal("large record rejected", err)
	}
}

func TestAllocationCloneStringHoldersEndAfterTypedCopy(t *testing.T) {
	const count = 100
	stringWidth := int64(reflect.TypeFor[string]().Size())
	headerWidth := int64(reflect.TypeFor[[]string]().Size())
	backing := count * stringWidth
	peak := backing + headerWidth + stringWidth
	b, err := NewBuildBounds(1, 1, peak)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	if err = b.CloneInto(make([]string, count), &out); err != nil {
		t.Fatal("exited child holders blocked a legal clone", err)
	}
	if len(out) != count || b.Snapshot().LiveBytes != backing || b.Snapshot().MeasuredRequiredBytes != peak {
		t.Fatal("typed copy did not retire its explicit temporary holder", b.Snapshot())
	}
	out = nil
	b.CloseRetired()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal(b.Snapshot())
	}
}

func TestAllocationCloneNestedTypedHoldersEndAfterCopy(t *testing.T) {
	type item struct {
		Text   string
		Values []string
	}
	const count = 80
	in := make([]item, count)
	for i := range in {
		in[i].Values = []string{"", ""}
	}
	stringWidth := int64(reflect.TypeFor[string]().Size())
	headerWidth := int64(reflect.TypeFor[[]string]().Size())
	itemWidth := int64(reflect.TypeFor[item]().Size())
	backing := count * (itemWidth + 2*stringWidth)
	peak := backing + headerWidth + itemWidth + headerWidth + stringWidth
	b, err := NewBuildBounds(1, 1, peak)
	if err != nil {
		t.Fatal(err)
	}
	var out []item
	if err = b.CloneInto(in, &out); err != nil {
		t.Fatal("nested exited holders blocked a legal clone", err)
	}
	if !reflect.DeepEqual(in, out) || b.Snapshot().LiveBytes != backing || b.Snapshot().MeasuredRequiredBytes != peak {
		t.Fatal("nested temporary holders survived typed copies", b.Snapshot())
	}
	out = nil
	b.CloseRetired()
}

func TestAllocationCloneInterfacesKeepActualBackingAndPointerArrays(t *testing.T) {
	array := &[2]string{"a", "b"}
	inner := []string{"", ""}
	in := []any{inner, "private", array}
	b, err := NewBuildBounds(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.Clone(in)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ValueAllocationBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if want == 0 || b.Snapshot().LiveBytes != want {
		t.Fatal("interface output retained a copied temporary or lost owned backing", want, b.Snapshot())
	}
	inner[0], array[0] = "changed", "changed"
	got := out.([]any)
	if got[0].([]string)[0] != "" || got[2].(*[2]string)[0] != "a" {
		t.Fatal("cloned interface borrowed source backing")
	}
	clear(got)
	got, out = nil, nil
	b.CloseRetired()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal(b.Snapshot())
	}
	implicitArray := [2]int{1, 2}
	if n, err := ValueAllocationBytes(implicitArray); err != nil || n != 0 {
		t.Fatal("implicit array boxing was charged", n, err)
	}
	if n, err := ValueAllocationBytes(&implicitArray); err != nil || n != int64(reflect.TypeFor[[2]int]().Size()) {
		t.Fatal("explicit array pointee lost its capacity", n, err)
	}
}
