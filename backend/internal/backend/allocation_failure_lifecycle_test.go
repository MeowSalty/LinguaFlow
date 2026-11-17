package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestAllocationDetachToBuildKeepsOriginalMeterAndBacking(t *testing.T) {
	var reserved, released int64
	ctx := WithBuildAllocation(context.Background(), 128, func(n int64) error {
		reserved += n
		return nil
	}, func(n int64) { released += n })
	ctx, err := WithProviderResponseAllocation(ctx, 4096)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBuildBoundsFromContext(ctx, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	if err = b.ReserveAllocation(80, 1); err != nil {
		t.Fatal(err)
	}
	backing := make([]byte, 16)
	backing[0] = 42
	temporary := make([]byte, 64)
	temporary[0] = 43
	owner, err := b.RetainedAllocation(int64(cap(backing)))
	if err != nil {
		t.Fatal(err)
	}
	next, err := owner.DetachToBuild()
	if err != nil {
		t.Fatal(err)
	}
	defer next.CloseRetired()
	if next.meterSource != b.meterSource || next.ctx != b.ctx || next.owned != 0 || next.Snapshot().LiveBytes != 16 || reserved != 80 || released != 0 {
		t.Fatal("stage transfer changed the source or recharged retained backing")
	}
	owner.Release()
	if _, err = owner.DetachToBuild(); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("consumed receipt transferred twice", err)
	}
	temporary = nil
	b.CloseRetired()
	if released != 64 || ProviderResponseSnapshot(ctx).LiveBytes != 16 || backing[0] != 42 {
		t.Fatal("previous stage refunded the surviving backing")
	}
	if err = b.ReserveAllocation(1, 1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("retired stage restarted", err)
	}
	if err = next.ReserveAllocation(12, 1); err != nil {
		t.Fatal(err)
	}
	if reserved != 92 || ProviderResponseSnapshot(ctx).LiveBytes != 28 {
		t.Fatal("next stage did not use the original meter")
	}
	backing = nil
	next.CloseRetired()
	if released != reserved || ProviderResponseSnapshot(ctx).LiveBytes != 0 {
		t.Fatal("stage backing did not retire exactly once")
	}
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
}

func allocationFailureBuild(t *testing.T, limit int64) (*BuildBounds, *OwnedAllocation, []byte, *resourcebudget.RootBudget) {
	t.Helper()
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewRequestAdmission(nil, RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: t.Name()}); err != nil {
		t.Fatal(err)
	}
	ctx := WithBuildOwnership(context.Background(), a, "failure-build")
	ctx = WithBuildAllocation(ctx, limit, func(n int64) error { return a.ClaimBuild("failure-build", n) }, func(n int64) {
		if err := a.ReleaseBuildBytes("failure-build", n); err != nil {
			t.Error(err)
		}
	})
	b, err := NewBuildBoundsFromContext(ctx, 4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.CloseRetired)
	owner, err := b.OwnAllocation(8, 1)
	if err != nil {
		t.Fatal(err)
	}
	backing := make([]byte, 8)
	backing[0] = 42
	return b, owner, backing, root
}

func TestAllocationCloneFailureRetiresOnlyUninstalledPrivateValues(t *testing.T) {
	type invalid struct {
		Text string
		Bad  chan int
	}
	type nested struct{ Value any }
	for _, test := range []struct {
		name          string
		src, previous any
		limit         int64
		capacity      bool
	}{
		{"slice-child", []string{""}, []string{"original"}, 63, true},
		{"struct-child", invalid{Text: "owned"}, invalid{Text: "original"}, 1024, false},
		{"interface-child", nested{Value: make(chan int)}, nested{Value: "original"}, 1024, false},
		{"map-value-after-key", map[string]chan int{"owned": nil}, map[string]chan int{"original": nil}, 1024, false},
		{"pointer-child", &invalid{Text: "owned"}, &invalid{Text: "original"}, 1024, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, previousOwner, previousBacking, root := allocationFailureBuild(t, test.limit)
			dst := reflect.New(reflect.TypeOf(test.src))
			dst.Elem().Set(reflect.ValueOf(test.previous))
			err := b.CloneInto(test.src, dst.Interface())
			if err == nil || test.capacity && !errors.Is(err, ErrBuildCapacity) {
				t.Fatal("failure cause lost", err)
			}
			if !reflect.DeepEqual(dst.Elem().Interface(), test.previous) {
				t.Fatal("failed clone changed caller destination")
			}
			if previousBacking[0] != 42 || b.Snapshot().LiveBytes != 8 || b.owned != 8 || root.Snapshot(1).Instance.PayloadBytes != 8 {
				t.Fatal("failed private clone left a holder or refunded a prior output", b.Snapshot(), b.owned, root.Snapshot(1))
			}
			if _, err = b.RetainedAllocation(1); !errors.Is(err, ErrBuildCapacity) {
				t.Fatal("failed clone left unowned backing", err)
			}
			previousBacking = nil
			previousOwner.Release()
			if b.Snapshot().LiveBytes != 0 || b.owned != 0 || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatal("final ownership did not drain", b.Snapshot(), root.Snapshot(1))
			}
		})
	}
}

func TestAllocationDecodeFailureDistinguishesPublishedPartialValues(t *testing.T) {
	for _, kind := range []string{"interface", "slice", "map-element"} {
		t.Run(kind, func(t *testing.T) {
			b, previousOwner, previousBacking, root := allocationFailureBuild(t, 1024)
			b.stringLimit = 1
			raw := []byte(`["a","long"]`)
			var iface any = "original"
			var slice []string
			var mapped map[string][]string
			dst := reflect.ValueOf(&iface).Elem()
			if kind == "slice" {
				dst = reflect.ValueOf(&slice).Elem()
			}
			if kind == "map-element" {
				raw = []byte(`{"a":["a"],"b":["b","long"]}`)
				dst = reflect.ValueOf(&mapped).Elem()
			}
			count, err := scanJSON(raw, nil, int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			nodes := make([]jsonNode, count)
			if _, err = scanJSON(raw, nodes, int64(len(raw))); err != nil {
				t.Fatal(err)
			}
			err = b.decodeValue(raw, nodes, 0, dst)
			if !errors.Is(err, ErrBuildCapacity) {
				t.Fatal("decoded failure cause changed", err)
			}
			retained := int64(0)
			if kind == "interface" {
				if iface != "original" {
					t.Fatal("unpublished concrete changed caller")
				}
			} else if kind == "slice" {
				if !reflect.DeepEqual(slice, []string{"a", ""}) {
					t.Fatal("installed partial slice changed", slice)
				}
				retained = 2*int64(reflect.TypeFor[string]().Size()) + 1
			} else {
				if !reflect.DeepEqual(mapped, map[string][]string{"a": {"a"}}) {
					t.Fatal("installed partial map changed", mapped)
				}
				retained = int64(reflect.TypeFor[string]().Size()) + 2
			}
			if previousBacking[0] != 42 || b.Snapshot().LiveBytes != 8+retained || b.owned != 8 || root.Snapshot(1).Instance.PayloadBytes != 8+retained {
				t.Fatal("unpublished payload remained or a published partial value was refunded", b.Snapshot(), root.Snapshot(1))
			}
			dst.SetZero()
			iface, slice, mapped = nil, nil, nil
			if err = b.retireUninstalledAllocation(retained); err != nil {
				t.Fatal(err)
			}
			previousBacking = nil
			previousOwner.Release()
			if b.Snapshot().LiveBytes != 0 || b.owned != 0 || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatal("final decoded ownership did not drain", b.Snapshot(), root.Snapshot(1))
			}
		})
	}
}

func TestAllocationDecodeBase64RetiresOnlyTemporaryAndUninstalledBacking(t *testing.T) {
	for _, fail := range []bool{false, true} {
		b, previousOwner, previousBacking, root := allocationFailureBuild(t, 1024)
		raw := []byte(`"YQ=="`)
		if fail {
			raw = []byte(`"%%%%"`)
		}
		dst := []byte("original")
		nodes := []jsonNode{{start: 0, end: len(raw), child: -1, next: -1, kind: '"'}}
		err := b.decodeValue(raw, nodes, 0, reflect.ValueOf(&dst).Elem())
		retained := int64(0)
		if fail {
			var corrupt base64.CorruptInputError
			if !errors.As(err, &corrupt) || string(dst) != "original" {
				t.Fatal("failed base64 changed caller or cause", err, string(dst))
			}
		} else {
			if err != nil || string(dst) != "a" {
				t.Fatal(err, string(dst))
			}
			retained = int64(cap(dst))
		}
		if previousBacking[0] != 42 || b.Snapshot().LiveBytes != 8+retained || b.owned != 8 || root.Snapshot(1).Instance.PayloadBytes != 8+retained {
			t.Fatal("base64 temporary retained or published backing refunded", b.Snapshot(), root.Snapshot(1))
		}
		dst = nil
		if err = b.retireUninstalledAllocation(retained); err != nil {
			t.Fatal(err)
		}
		previousBacking = nil
		previousOwner.Release()
		if b.Snapshot().LiveBytes != 0 || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
			t.Fatal("base64 final ownership did not drain", b.Snapshot(), root.Snapshot(1))
		}
	}
}
