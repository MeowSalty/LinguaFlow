package prompt

import (
	"errors"
	"reflect"
)

var errTemplateComparison = errors.New("invalid or non-comparable template values")

func comparisonValue(v reflect.Value) reflect.Value {
	for v.IsValid() && v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

func comparisonKind(v reflect.Value) reflect.Kind {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.Int
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflect.Uint
	case reflect.Float32, reflect.Float64:
		return reflect.Float64
	case reflect.Complex64, reflect.Complex128:
		return reflect.Complex128
	case reflect.Bool, reflect.String:
		return v.Kind()
	default:
		return reflect.Invalid
	}
}

func comparisonNil(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// The standard template equality error formats the entire uncomparable value.
// Keep its successful comparison semantics, using Equal without Interface copies
// and a finite error for invalid comparisons instead of rendering that value.
func templateEqualBounded(first reflect.Value, rest ...reflect.Value) (bool, error) {
	if len(rest) == 0 {
		return false, errTemplateComparison
	}
	first = comparisonValue(first)
	for _, next := range rest {
		next = comparisonValue(next)
		a, z := comparisonKind(first), comparisonKind(next)
		var equal bool
		if a != z {
			switch {
			case a == reflect.Int && z == reflect.Uint:
				equal = first.Int() >= 0 && uint64(first.Int()) == next.Uint()
			case a == reflect.Uint && z == reflect.Int:
				equal = next.Int() >= 0 && first.Uint() == uint64(next.Int())
			case first.IsValid() && next.IsValid():
				return false, errTemplateComparison
			}
		} else {
			switch a {
			case reflect.Bool:
				equal = first.Bool() == next.Bool()
			case reflect.Int:
				equal = first.Int() == next.Int()
			case reflect.Uint:
				equal = first.Uint() == next.Uint()
			case reflect.Float64:
				equal = first.Float() == next.Float()
			case reflect.Complex128:
				equal = first.Complex() == next.Complex()
			case reflect.String:
				equal = first.String() == next.String()
			default:
				if first.Kind() != next.Kind() && first.IsValid() && next.IsValid() {
					return false, errTemplateComparison
				}
				if comparisonNil(first) || comparisonNil(next) {
					equal = comparisonNil(first) == comparisonNil(next)
				} else if first.Type() != next.Type() {
					equal = false
				} else if !first.Comparable() || !next.Comparable() {
					return false, errTemplateComparison
				} else {
					equal = first.Equal(next)
				}
			}
		}
		if equal {
			return true, nil
		}
	}
	return false, nil
}

func templateNotEqualBounded(a, z reflect.Value) (bool, error) {
	equal, err := templateEqualBounded(a, z)
	return !equal, err
}
