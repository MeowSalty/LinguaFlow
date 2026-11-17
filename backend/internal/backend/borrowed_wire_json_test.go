package backend

import (
	"errors"
	"strings"
	"testing"
)

func TestBorrowWireJSONTraversesWithoutArenaOrNodeFee(t *testing.T) {
	data := []byte(`{"unknown":"` + strings.Repeat("x", 70000) + `","items":[` + strings.TrimSuffix(strings.Repeat("0,", 70000), ",") + `],"\u006bey":"first","key":"last"}`)
	b, _ := NewBuildBounds(int64(len(data)), 0, 1)
	value, err := BorrowWireJSON(b, data)
	if err != nil {
		t.Fatal(err)
	}
	field, err := value.Field("key")
	if err != nil || !field.StringEquals("last") {
		t.Fatal(err)
	}
	items, err := value.Field("items")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	if err = items.EachArray(func(item WireJSONValue) error {
		if item.Kind() != '0' {
			t.Fatal(item.Kind())
		}
		count++
		return nil
	}); err != nil || count != 70000 {
		t.Fatal(count, err)
	}
	if s := b.Snapshot(); s.LiveBytes != 0 || s.AllocationBytes != 0 || s.MeasuredRequiredBytes != 0 {
		t.Fatal(s)
	}
}

func TestBorrowWireJSONOwnsOnlySelectedMaterializedString(t *testing.T) {
	data := []byte(`{"content":"a\n\uD83D\uDE00","null":null}`)
	b, _ := NewBuildBounds(int64(len(data)), 0, 32)
	value, err := BorrowWireJSON(b, data)
	if err != nil {
		t.Fatal(err)
	}
	field, err := value.Field("content")
	if err != nil {
		t.Fatal(err)
	}
	text, owner, err := field.OwnedString()
	bytes := int64(len("a\n😀"))
	if err != nil || text != "a\n😀" || b.Snapshot().LiveBytes != bytes || b.Snapshot().MeasuredRequiredBytes != 2*bytes || b.Snapshot().AllocationBytes != 2*bytes {
		t.Fatal(text, err, b.Snapshot())
	}
	text = ""
	owner.Release()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal(b.Snapshot())
	}
	limited, _ := NewBuildBounds(int64(len(data)), 0, 2*bytes-1)
	value, err = BorrowWireJSON(limited, data)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := value.Field("content")
	if err != nil {
		t.Fatal(err)
	}
	if text, owner, err := selected.OwnedString(); !errors.Is(err, ErrBuildCapacity) || text != "" || owner != nil || limited.Snapshot().LiveBytes != 0 {
		t.Fatal("materialization accepted without capacity for both actual backings", text, err, limited.Snapshot())
	}
	missing, err := value.Field("missing")
	if err != nil || !missing.IsNull() {
		t.Fatal(err)
	}
	number, err := missing.Int64()
	if err != nil || number != 0 {
		t.Fatal(number, err)
	}
}
