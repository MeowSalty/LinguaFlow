package backend

import "unsafe"

// AppendNode credits the exact new backing capacity before make/copy. It never
// relies on the runtime append growth heuristic or credits only the used length.
func AppendNode[T any](b *BuildBounds, s []T, v T) ([]T, error) {
	if len(s) == cap(s) {
		capacity := max(1, cap(s)*2)
		var zero T
		if err := b.ReserveNodes(int64(capacity), int64(unsafe.Sizeof(zero))); err != nil {
			return nil, err
		}
		out := make([]T, len(s), capacity)
		copy(out, s)
		s = out
	}
	return append(s, v), nil
}
