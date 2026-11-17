package backend

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"unsafe"
)

func TestAllocationReflectionFieldsExcludeLibraryIndexBacking(t *testing.T) {
	type tiny struct{ Flag bool }
	b, err := NewBuildBounds(64, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	var out tiny
	if err = b.CloneInto(tiny{true}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Flag || b.Snapshot().LiveBytes != 0 || b.Snapshot().MeasuredRequiredBytes != 1 {
		t.Fatalf("typed holder/value: %+v %+v", out, b.Snapshot())
	}
	b.CloseRetired()
	b, err = NewBuildBounds(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := b.fieldsFor(reflect.TypeFor[tiny]())
	if err != nil {
		t.Fatal(err)
	}
	want := int64(unsafe.Sizeof(decoderField{})) + int64(unsafe.Sizeof(decoderFieldCache{}))
	if len(fields) != 1 || fields[0].index != 0 || b.Snapshot().LiveBytes != want || b.Snapshot().AllocationBytes != want {
		t.Fatalf("explicit field cache only: %+v want=%d", b.Snapshot(), want)
	}
	b.CloseRetired()
}

func TestAllocationBuildUserCounterRetiresAndPreservesOutput(t *testing.T) {
	ctx := WithBuildAllocation(context.Background(), 64, nil, nil)
	b, err := NewBuildBoundsFromContext(ctx, 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := b.OwnAllocation(8, 1)
	if err != nil {
		t.Fatal(err)
	}
	backing := make([]byte, 8)
	for i := 0; i < 100; i++ {
		text, err := BuildUserWithCredits(b, "", 64, nil, func(w io.Writer) error { _, err := io.WriteString(w, "ruby"); return err })
		if err != nil {
			t.Fatal(err)
		}
		if text != "ruby" || b.Snapshot().LiveBytes != 12 {
			t.Fatalf("iteration=%d text=%q snapshot=%+v", i, text, b.Snapshot())
		}
		output, err := b.RetainedAllocation(int64(len(text)))
		if err != nil {
			t.Fatal(err)
		}
		text = ""
		output.Release()
		if b.Snapshot().LiveBytes != 8 {
			t.Fatal("temporary counter or writer accumulated")
		}
	}
	cause := errors.New("write failed")
	for _, failure := range []string{"first", "claim", "second", "escaping", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			claim := func(PromptMeasure) error {
				if failure == "claim" {
					return cause
				}
				return nil
			}
			if failure == "cancel" {
				claim = func(PromptMeasure) error { return context.Canceled }
			}
			text, err := BuildUserWithCredits(b, "", 64, claim, func(w io.Writer) error {
				calls++
				if failure == "first" && calls == 1 || failure == "second" && calls == 2 {
					return cause
				}
				value := "ruby"
				if failure == "escaping" && calls == 2 {
					value = "\"uby"
				}
				_, err := io.WriteString(w, value)
				return err
			})
			if err == nil || text != "" || b.Snapshot().LiveBytes != 8 {
				t.Fatalf("output=%q error=%v snapshot=%+v", text, err, b.Snapshot())
			}
			if failure == "first" || failure == "claim" || failure == "second" {
				if !errors.Is(err, cause) {
					t.Fatal("original cause lost")
				}
			}
		})
	}
	clear(backing)
	backing = nil
	seed.Release()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal("owned backing remains")
	}
	b.CloseRetired()
}
