package backend

import (
	"regexp"
	"unsafe"
)

// Returned capture slices expose their actual capacity; regexp's internal
// caches and machines are not observable and have no synthetic allocation fee.
func FindRegexpSubmatch(b *BuildBounds, re *regexp.Regexp, s string) ([]int, error) {
	if err := b.CheckContext(); err != nil {
		return nil, err
	}
	indices := re.FindStringSubmatchIndex(s)
	if err := b.ReserveAllocation(int64(cap(indices)), int64(unsafe.Sizeof(int(0)))); err != nil {
		return nil, err
	}
	return indices, nil
}
func FindRegexpIndex(b *BuildBounds, re *regexp.Regexp, s string) ([]int, error) {
	if err := b.CheckContext(); err != nil {
		return nil, err
	}
	indices := re.FindStringIndex(s)
	if err := b.ReserveAllocation(int64(cap(indices)), int64(unsafe.Sizeof(int(0)))); err != nil {
		return nil, err
	}
	return indices, nil
}
