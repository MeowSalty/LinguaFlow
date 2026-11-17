package backend

import (
	"errors"
	"reflect"
	"strings"
	"unsafe"
)

// Clone preserves the exact concrete types in parser metadata. Unlike a JSON
// round trip, []ruby.Item, []int and integers retain their semantics. Every
// private allocation is charged before it is created. Custom clone or marshal
// hooks are never invoked.
func (b *BuildBounds) Clone(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	out, err := b.cloneValue(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	value := out.value.Interface()
	// Interface conversion copies addressable holders. Unaddressable slice
	// headers may instead be retained by the returned interface.
	if err = out.finishHolder(!out.value.CanAddr()); err != nil {
		return nil, err
	}
	return value, nil
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
	return out.copyTo(v.Elem())
}

type clonedValue struct {
	value        reflect.Value
	holder       *OwnedAllocation
	payloadBytes int64
}

func (c *clonedValue) retireFailed(b *BuildBounds) error {
	c.value = reflect.Value{}
	c.holder.Release()
	c.holder = nil
	bytes := c.payloadBytes
	c.payloadBytes = 0
	return b.retireUninstalledAllocation(bytes)
}

// The byte count identifies only explicit backing made for a discarded private
// value. It is never a difference of global snapshots or another owner's credit.
func (b *BuildBounds) retireUninstalledAllocation(bytes int64) error {
	if bytes == 0 {
		return nil
	}
	allocation, err := b.RetainedAllocation(bytes)
	if err != nil {
		return err
	}
	allocation.Release()
	return nil
}

func (c *clonedValue) finishHolder(keep bool) error {
	c.value = reflect.Value{}
	if c.holder == nil {
		return nil
	}
	var err error
	if keep {
		err = c.holder.ReturnToBuild()
		if err == nil {
			c.payloadBytes += c.holder.bytes
		}
	} else {
		c.holder.Release()
	}
	c.holder = nil
	return err
}

// A concrete destination copies the reflected value into its existing slot.
// An interface can keep an unaddressable holder, notably reflect.MakeSlice's
// header. Addressable holders are copied by reflect's interface conversion.
func (c *clonedValue) copyTo(dst reflect.Value) error {
	keep := dst.Kind() == reflect.Interface && !c.value.CanAddr()
	dst.Set(c.value)
	return c.finishHolder(keep)
}

func (b *BuildBounds) cloneValue(v reflect.Value) (result clonedValue, resultErr error) {
	if err := b.Enter(); err != nil {
		return clonedValue{}, err
	}
	defer b.Leave()
	if !v.IsValid() {
		return clonedValue{value: v}, nil
	}
	var holder *OwnedAllocation
	var err error
	var out reflect.Value
	var payloadBytes int64
	var pending, pendingKey *clonedValue
	defer func() {
		if resultErr == nil {
			return
		}
		out = reflect.Value{}
		if pending != nil {
			resultErr = errors.Join(resultErr, pending.retireFailed(b))
		}
		if pendingKey != nil {
			resultErr = errors.Join(resultErr, pendingKey.retireFailed(b))
		}
		holder.Release()
		resultErr = errors.Join(resultErr, b.retireUninstalledAllocation(payloadBytes))
	}()
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return clonedValue{value: reflect.Zero(v.Type())}, nil
		}
		if holder, err = b.OwnAllocation(1, int64(v.Type().Size())); err != nil {
			return clonedValue{}, err
		}
		out = reflect.New(v.Type()).Elem()
		child, err := b.cloneValue(v.Elem())
		if err != nil {
			return clonedValue{}, err
		}
		pending = &child
		if err = child.copyTo(out); err != nil {
			return clonedValue{}, err
		}
		payloadBytes += child.payloadBytes
		pending = nil
		return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
	case reflect.Pointer:
		if v.IsNil() {
			return clonedValue{value: reflect.Zero(v.Type())}, nil
		}
		if err := b.ReserveAllocation(1, int64(v.Type().Elem().Size())); err != nil {
			return clonedValue{}, err
		}
		payloadBytes = int64(v.Type().Elem().Size())
		out = reflect.New(v.Type().Elem())
		child, err := b.cloneValue(v.Elem())
		if err != nil {
			return clonedValue{}, err
		}
		pending = &child
		if err = child.copyTo(out.Elem()); err != nil {
			return clonedValue{}, err
		}
		payloadBytes += child.payloadBytes
		pending = nil
		return clonedValue{value: out, payloadBytes: payloadBytes}, nil
	case reflect.String:
		if err := b.ReserveText(int64(v.Len()), int64(v.Len())); err != nil {
			return clonedValue{}, err
		}
		payloadBytes = int64(v.Len())
		if holder, err = b.OwnAllocation(1, int64(v.Type().Size())); err != nil {
			return clonedValue{}, err
		}
		out = reflect.New(v.Type()).Elem()
		out.SetString(strings.Clone(v.String()))
		return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
	case reflect.Map:
		if v.IsNil() {
			return clonedValue{value: reflect.Zero(v.Type())}, nil
		}
		if v.Type().Key().Kind() != reflect.String {
			return clonedValue{}, errors.New("bounded metadata map keys must be strings")
		}
		keyCredit, err := b.OwnAllocation(1, int64(v.Type().Key().Size()))
		if err != nil {
			return clonedValue{}, err
		}
		defer keyCredit.Release()
		valueCredit, err := b.OwnAllocation(1, int64(v.Type().Elem().Size()))
		if err != nil {
			return clonedValue{}, err
		}
		defer valueCredit.Release()
		keySlot, valueSlot := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		defer func() { keySlot, valueSlot = reflect.Value{}, reflect.Value{} }()
		keysCredit, err := b.OwnAllocation(int64(v.Len()), int64(unsafe.Sizeof("")))
		if err != nil {
			return clonedValue{}, err
		}
		defer keysCredit.Release()
		keys := make([]string, 0, v.Len())
		defer func() { keys = nil }()
		keyIter := v.MapRange()
		for keyIter.Next() {
			keySlot.SetIterKey(keyIter)
			keys = append(keys, keySlot.String())
		}
		out, err = MakeStringMap(b, v.Type(), keys)
		if err != nil {
			return clonedValue{}, err
		}
		it := v.MapRange()
		for it.Next() {
			keySlot.SetIterKey(it)
			valueSlot.SetIterValue(it)
			k, err := b.cloneValue(keySlot)
			if err != nil {
				return clonedValue{}, err
			}
			pendingKey = &k
			value, err := b.cloneValue(valueSlot)
			if err != nil {
				return clonedValue{}, err
			}
			pending = &value
			out.SetMapIndex(k.value, value.value)
			// SetMapIndex copies these typed slots. Any concrete value kept by
			// an interface was already retained while building its child.
			if err = k.finishHolder(false); err != nil {
				return clonedValue{}, err
			}
			if err = value.finishHolder(false); err != nil {
				return clonedValue{}, err
			}
			payloadBytes += k.payloadBytes + value.payloadBytes
			pendingKey, pending = nil, nil
		}
		return clonedValue{value: out, payloadBytes: payloadBytes}, nil
	case reflect.Slice:
		if v.IsNil() {
			return clonedValue{value: reflect.Zero(v.Type())}, nil
		}
		if holder, err = b.OwnAllocation(1, int64(v.Type().Size())); err != nil {
			return clonedValue{}, err
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if err := b.ReserveText(int64(v.Len()), int64(v.Len())); err != nil {
				return clonedValue{}, err
			}
			payloadBytes = int64(v.Len())
		} else {
			if err := b.ReserveAllocation(int64(v.Len()), int64(v.Type().Elem().Size())); err != nil {
				return clonedValue{}, err
			}
			payloadBytes = int64(v.Len()) * int64(v.Type().Elem().Size())
		}
		// Use one explicitly owned, addressable header. Set copies the
		// MakeSlice result into it; a later interface conversion copies this
		// temporary header rather than retaining its original storage.
		out = reflect.New(v.Type()).Elem()
		out.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
		if v.Type().Elem().Kind() == reflect.Uint8 {
			reflect.Copy(out, v)
			return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
		}
		for i := 0; i < v.Len(); i++ {
			child, err := b.cloneValue(v.Index(i))
			if err != nil {
				return clonedValue{}, err
			}
			pending = &child
			if err = child.copyTo(out.Index(i)); err != nil {
				return clonedValue{}, err
			}
			payloadBytes += child.payloadBytes
			pending = nil
		}
		return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
	case reflect.Array:
		if holder, err = b.OwnAllocation(int64(v.Len()), int64(v.Type().Elem().Size())); err != nil {
			return clonedValue{}, err
		}
		out = reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			child, err := b.cloneValue(v.Index(i))
			if err != nil {
				return clonedValue{}, err
			}
			pending = &child
			if err = child.copyTo(out.Index(i)); err != nil {
				return clonedValue{}, err
			}
			payloadBytes += child.payloadBytes
			pending = nil
		}
		return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
	case reflect.Struct:
		if v.Type() != timeType {
			for i := 0; i < v.NumField(); i++ {
				field := v.Type().Field(i)
				if field.PkgPath != "" {
					return clonedValue{}, errors.New("unsupported private metadata field")
				}
			}
		}
		if holder, err = b.OwnAllocation(1, int64(v.Type().Size())); err != nil {
			return clonedValue{}, err
		}
		out = reflect.New(v.Type()).Elem()
		if v.Type() == timeType {
			out.Set(v)
			return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
		}
		for i := 0; i < v.NumField(); i++ {
			child, err := b.cloneValue(v.Field(i))
			if err != nil {
				return clonedValue{}, err
			}
			pending = &child
			if err = child.copyTo(out.Field(i)); err != nil {
				return clonedValue{}, err
			}
			payloadBytes += child.payloadBytes
			pending = nil
		}
		return clonedValue{value: out, holder: holder, payloadBytes: payloadBytes}, nil
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return clonedValue{value: v}, nil
	default:
		return clonedValue{}, errors.New("unsupported bounded metadata type")
	}
}
