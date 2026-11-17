package backend

import (
	"context"
	"reflect"
)

// ValueAllocationBytes counts visible, privately-owned string/slice/pointer
// payload capacity. Map buckets and runtime boxing are excluded; this is not a
// complete heap bound or a future clone-workspace reservation.
func ValueAllocationBytes(value any) (int64, error) {
	return ValueAllocationBytesFromContext(context.Background(), value)
}
func ValueAllocationBytesFromContext(ctx context.Context, value any) (int64, error) {
	b, err := NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return 0, err
	}
	v := reflect.ValueOf(value)
	n, err := valueAllocationBytes(v, 0, b)
	b.CloseRetired()
	return n, err
}

func valueAllocationBytes(v reflect.Value, depth int, b *BuildBounds) (int64, error) {
	return visibleAllocationBytes(v, depth, b, false)
}

func visibleAllocationBytes(v reflect.Value, depth int, b *BuildBounds, embedded bool) (int64, error) {
	if err := b.CheckContext(); err != nil {
		return 0, err
	}
	if !v.IsValid() {
		return 0, nil
	}
	if depth >= MaxBuildDepth {
		return 0, &BuildCapacityError{"payload depth or cycle"}
	}
	total := int64(0)
	add := func(n int64, err error) error {
		if err != nil {
			return err
		}
		total, err = CheckedAdd(total, n)
		return err
	}
	child := func(value reflect.Value, included bool) error {
		return add(visibleAllocationBytes(value, depth+1, b, included))
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			if err := child(v.Elem(), embedded); err != nil {
				return 0, err
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			if err := add(AllocationBytes(1, int64(v.Type().Elem().Size()))); err != nil {
				return 0, err
			}
			if err := child(v.Elem(), true); err != nil {
				return 0, err
			}
		}
	case reflect.String:
		if err := add(AllocationBytes(int64(v.Len()), 1)); err != nil {
			return 0, err
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice {
			if v.IsNil() {
				return 0, nil
			}
			if err := add(AllocationBytes(int64(v.Cap()), int64(v.Type().Elem().Size()))); err != nil {
				return 0, err
			}
		} else if !embedded && v.CanAddr() {
			// A top-level array reached through an interface lives in runtime
			// boxing storage. Only an explicit addressable array holder counts;
			// pointer pointees and embedded arrays are already charged above.
			if err := add(AllocationBytes(1, int64(v.Type().Size()))); err != nil {
				return 0, err
			}
		}
		for i := 0; i < v.Len(); i++ {
			if err := child(v.Index(i), true); err != nil {
				return 0, err
			}
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return total, nil
		}
		for i := 0; i < v.NumField(); i++ {
			if err := child(v.Field(i), true); err != nil {
				return 0, err
			}
		}
	case reflect.Map:
		if v.IsNil() {
			return 0, nil
		}
		// Runtime buckets/boxed records are not publicly measurable. Only
		// private payload values and our actual reflected scratch are visited.
		key, value, release, err := measurementMapSlots(b, v.Type().Key(), v.Type().Elem())
		if err != nil {
			return 0, err
		}
		defer release()
		iter := v.MapRange()
		for iter.Next() {
			key.SetIterKey(iter)
			value.SetIterValue(iter)
			if err := child(key, true); err != nil {
				return 0, err
			}
			if err := child(value, true); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func measurementMapSlots(b *BuildBounds, keyType, valueType reflect.Type) (reflect.Value, reflect.Value, func(), error) {
	keyCredit, err := b.OwnAllocation(1, int64(keyType.Size()))
	if err != nil {
		return reflect.Value{}, reflect.Value{}, nil, err
	}
	valueCredit, err := b.OwnAllocation(1, int64(valueType.Size()))
	if err != nil {
		keyCredit.Release()
		return reflect.Value{}, reflect.Value{}, nil, err
	}
	key, value := reflect.New(keyType).Elem(), reflect.New(valueType).Elem()
	return key, value, func() { keyCredit.Release(); valueCredit.Release() }, nil
}
