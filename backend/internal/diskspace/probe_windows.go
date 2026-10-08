//go:build windows

package diskspace

import (
	"context"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"time"
)

func SystemProbe(ctx context.Context, path string) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Observation{}, err
	}
	defer f.Close()
	// Resolve aliases and mounted folders through the actual opened handle.
	buffer := make([]uint16, 32768)
	// VOLUME_NAME_GUID also prevents collisions between volume serial numbers.
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buffer[0], uint32(len(buffer)), 1)
	if err != nil {
		return Observation{}, err
	}
	if n >= uint32(len(buffer)) {
		return Observation{}, errors.New("filesystem path too long")
	}
	resolved := windows.UTF16ToString(buffer[:n])
	volumeEnd := strings.Index(resolved, "}\\")
	if !strings.HasPrefix(resolved, `\\?\Volume{`) || volumeEnd < 0 {
		return Observation{}, errors.New("filesystem volume identity unavailable")
	}
	name, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return Observation{}, err
	}
	var available, total, free uint64
	if err = windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return Observation{}, err
	}
	return Observation{FilesystemID: strings.ToLower(resolved[:volumeEnd+1]), TotalBytes: total, AvailableBytes: available, ObservedAt: time.Now().UTC()}, nil
}
func isSpaceError(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) || errors.Is(err, windows.ERROR_DISK_QUOTA_EXCEEDED)
}
