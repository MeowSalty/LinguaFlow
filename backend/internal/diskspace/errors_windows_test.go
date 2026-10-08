//go:build windows

package diskspace

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"golang.org/x/sys/windows"
	"os"
	"testing"
)

func TestPlatformSpaceErrors(t *testing.T) {
	for _, cause := range []error{windows.ERROR_DISK_FULL, windows.ERROR_HANDLE_DISK_FULL, windows.ERROR_DISK_QUOTA_EXCEEDED} {
		err := Classify(&os.PathError{Op: "write", Path: "secret", Err: cause})
		if !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
			t.Fatal(err)
		}
	}
}
