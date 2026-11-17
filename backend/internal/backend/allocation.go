package backend

import "unsafe"

// ReserveSlice accounts for an explicit slice backing of exactly count elements.
// Runtime map buckets and allocation classes are deliberately excluded.
func ReserveSlice[T any](b *BuildBounds, count int64) error {
	var value T
	return b.ReserveAllocation(count, int64(unsafe.Sizeof(value)))
}
