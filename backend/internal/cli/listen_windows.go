//go:build windows

package cli

import (
	"errors"
	"syscall"
)

func isAddressInUse(err error) bool {
	// Winsock reports WSAEADDRINUSE rather than the POSIX errno value.
	const wsaAddressInUse syscall.Errno = 10048
	return errors.Is(err, wsaAddressInUse)
}
