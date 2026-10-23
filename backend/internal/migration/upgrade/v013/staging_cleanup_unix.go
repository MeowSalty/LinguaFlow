//go:build !windows

package v013

func retryableStagingRemoval(error) bool { return false }
