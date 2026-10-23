//go:build !windows

package v013

import (
	"errors"
	"os"
	"testing"
)

func TestUnixUnpublishedStagingCleanupDoesNotRetryPermissionFailures(t *testing.T) {
	journal := unpublishedStagingFixture(t)
	calls := 0
	err := cleanupUnpublishedStagingWith(journal, func(string) error {
		calls++
		return os.ErrPermission
	}, unexpectedStagingWait(t))
	if !errors.Is(err, os.ErrPermission) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
