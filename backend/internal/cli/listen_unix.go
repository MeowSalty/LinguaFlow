//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

func isAddressInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }
