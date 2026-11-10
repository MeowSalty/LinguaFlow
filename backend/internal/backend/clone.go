package backend

import (
	"errors"
	"reflect"
	"strings"
)

// Clone preserves the exact concrete types in parser metadata. Unlike a JSON
// round trip, []ruby.Item, []int and integers retain their semantics. Every
// private allocation is charged before it is created; callbacks are never used.
func (b *BuildBounds) Clone(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	out, err := b.cloneValue(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	if err := b.ReserveNodes(1, int64(out.Type().Size())); err != nil {
		return nil, err
	}
	return out.Interface(), nil
}
func (b *BuildBounds) CloneInto(src, dst any) error {
	v := reflect.ValueOf(dst)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return errors.New("clone destination must be a nonnil pointer")
	}
	s := reflect.ValueOf(src)
	if !s.IsValid() {
		v.Elem().SetZero()
		return nil
	}
	if !s.Type().AssignableTo(v.Elem().Type()) {
		return errors.New("clone destination type mismatch")
	}
	out, err := b.cloneValue(s)
	if err != nil {
		return err
	}
	v.Elem().Set(out)
	return nil
}
func (b *BuildBounds) cloneValue(v reflect.Value) (reflect.Value, error) {
	if err := b.Enter(); err != nil {
		return reflect.Value{}, err
	}
	defer b.Leave()
	if !v.IsValid() {
		return v, nil
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		if err := b.ReserveNodes(1, int64(v.Type().Size())); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		child, err := b.cloneValue(v.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		if err := b.ReserveNodes(1, int64(child.Type().Size())); err != nil {
			return reflect.Value{}, err
		}
		out.Set(child)
		return out, nil
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		if err := b.ReserveNodes(1, int64(v.Type().Elem().Size())); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type().Elem())
		child, err := b.cloneValue(v.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		out.Elem().Set(child)
		return out, nil
	case reflect.String:
		if err := b.ReserveText(int64(v.Len()), int64(v.Len())); err != nil {
			return reflect.Value{}, err
		}
		if err := b.ReserveNodes(1, int64(v.Type().Size())); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		out.SetString(strings.Clone(v.String()))
		return out, nil
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		if v.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, errors.New("bounded metadata map keys must be strings")
		}
		if v.Type().Elem().Size() > uintptr(MaxNodeAllocationBytes) {
			return reflect.Value{}, &BuildCapacityError{"clone map reflection value"}
		}
		if err := b.ReserveNodes(int64(v.Len())+1, MaxNodeAllocationBytes); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			if err := b.ReserveNodes(1, int64(v.Type().Key().Size())+int64(v.Type().Elem().Size())); err != nil {
				return reflect.Value{}, err
			}
			k, err := b.cloneValue(it.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			value, err := b.cloneValue(it.Value())
			if err != nil {
				return reflect.Value{}, err
			}
			if v.Type().Elem().Size() > 128 {
				if err := b.ReserveNodes(1, int64(v.Type().Elem().Size())); err != nil {
					return reflect.Value{}, err
				}
			}
			out.SetMapIndex(k, value)
		}
		return out, nil
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type()), nil
		}
		if err := b.ReserveNodes(1, int64(v.Type().Size())); err != nil {
			return reflect.Value{}, err
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if err := b.ReserveText(int64(v.Len()), int64(v.Len())); err != nil {
				return reflect.Value{}, err
			}
		} else {
			if err := b.ReserveNodes(int64(v.Len()), int64(v.Type().Elem().Size())); err != nil {
				return reflect.Value{}, err
			}
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		if v.Type().Elem().Kind() == reflect.Uint8 {
			reflect.Copy(out, v)
			return out, nil
		}
		for i := 0; i < v.Len(); i++ {
			child, err := b.cloneValue(v.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(child)
		}
		return out, nil
	case reflect.Array:
		if err := b.ReserveNodes(int64(v.Len()), int64(v.Type().Elem().Size())); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			child, err := b.cloneValue(v.Index(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(child)
		}
		return out, nil
	case reflect.Struct:
		if v.Type() != timeType {
			for i := 0; i < v.NumField(); i++ {
				if err := b.ReserveNodes(1, 64); err != nil {
					return reflect.Value{}, err
				}
				if v.Type().Field(i).PkgPath != "" {
					return reflect.Value{}, errors.New("unsupported private metadata field")
				}
			}
		}
		if err := b.ReserveNodes(1, int64(v.Type().Size())); err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(v.Type()).Elem()
		if v.Type() == timeType {
			out.Set(v)
			return out, nil
		}
		for i := 0; i < v.NumField(); i++ {
			child, err := b.cloneValue(v.Field(i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Field(i).Set(child)
		}
		return out, nil
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return v, nil
	default:
		return reflect.Value{}, errors.New("unsupported bounded metadata type")
	}
}
