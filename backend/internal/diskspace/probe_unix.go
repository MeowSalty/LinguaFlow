//go:build linux || darwin

package diskspace

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"math"
	"os"
	"syscall"
	"time"
)

func SystemProbe(ctx context.Context, path string) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return Observation{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Observation{}, err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Observation{}, errors.New("filesystem identity unavailable")
	}
	block := uint64(stat.Bsize)
	if stat.Bsize <= 0 || uint64(stat.Blocks) > math.MaxUint64/block || uint64(stat.Bavail) > math.MaxUint64/block {
		return Observation{}, errors.New("filesystem size overflow")
	}
	return Observation{FilesystemID: fmt.Sprintf("dev:%d", native.Dev), TotalBytes: uint64(stat.Blocks) * block, AvailableBytes: uint64(stat.Bavail) * block, ObservedAt: time.Now().UTC()}, nil
}
func isSpaceError(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
