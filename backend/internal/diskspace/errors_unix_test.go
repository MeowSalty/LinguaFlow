//go:build linux || darwin

package diskspace

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"os"
	"syscall"
	"testing"
)

func TestPlatformSpaceErrors(t *testing.T) {
	for _, cause := range []error{syscall.ENOSPC, syscall.EDQUOT} {
		err := Classify(&os.PathError{Op: "write", Path: "secret", Err: cause})
		if !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
			t.Fatal(err)
		}
	}
}
