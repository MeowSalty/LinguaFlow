package backend

import (
	"reflect"
	"unsafe"
)

// MakeMap uses the known entry hint. Hidden runtime bucket capacity is excluded.
func MakeMap[K comparable, V any](b *BuildBounds, keys []K) (map[K]V, error) {
	if err := b.CheckContext(); err != nil {
		return nil, err
	}
	return make(map[K]V, len(keys)), nil
}
func MakeStringMap(b *BuildBounds, t reflect.Type, keys []string) (reflect.Value, error) {
	if t.Kind() != reflect.Map || t.Key().Kind() != reflect.String {
		return reflect.Value{}, &BuildCapacityError{"bounded map key type"}
	}
	if err := b.CheckContext(); err != nil {
		return reflect.Value{}, err
	}
	return reflect.MakeMapWithSize(t, len(keys)), nil
}

func MapKeys[K comparable, V any](b *BuildBounds, source map[K]V) ([]K, error) {
	if err := ReserveSlice[K](b, int64(len(source))); err != nil {
		return nil, err
	}
	keys := make([]K, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	return keys, nil
}
func MakeMapFor[K comparable, V any, T any](b *BuildBounds, rows []T, key func(T) K) (map[K]V, error) {
	var zero K
	workspace, err := b.OwnAllocation(int64(len(rows)), int64(unsafe.Sizeof(zero)))
	if err != nil {
		return nil, err
	}
	defer workspace.Release()
	keys := make([]K, len(rows))
	for i, row := range rows {
		keys[i] = key(row)
	}
	return MakeMap[K, V](b, keys)
}
