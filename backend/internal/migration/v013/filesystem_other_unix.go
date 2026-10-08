//go:build !windows && !linux && !darwin

package v013

import "errors"

func validateLocalVolume(string) error {
	return errors.New("local migration supports Windows, Linux and macOS local filesystems")
}

func renameLocalNoReplace(string, string) error {
	return errors.New("atomic no-replace directory rename is unsupported on this platform")
}
