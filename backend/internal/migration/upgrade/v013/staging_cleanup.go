package v013

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// This cleanup is only for the current invocation's unpublished directory.
// Recovery has its own receipt requirements and must not use this helper after
// a switch journal has been published. Cleanup finishes before releasing the
// migration lock, even when the migration context has already been cancelled.
func cleanupUnpublishedStaging(journal localJournal) error {
	return cleanupUnpublishedStagingWith(journal, os.RemoveAll, time.Sleep)
}

func cleanupUnpublishedStagingWith(journal localJournal, remove func(string) error, wait func(time.Duration)) (err error) {
	delays := [...]time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	attempts := 0
	defer func() {
		if err != nil {
			err = fmt.Errorf("cleanup unpublished staging directory %s failed after %d removal attempts; directory switch did not start and original data directory %s was not replaced; residual files may remain: %w", journal.StageDir, attempts, journal.SourceDir, err)
		}
	}()
	var removalErr error
	for {
		exists, err := validateUnpublishedStaging(journal)
		if err != nil {
			return errors.Join(removalErr, fmt.Errorf("validate staging directory before removal: %w", err))
		}
		if !exists {
			return nil
		}
		attempts++
		removalErr = remove(journal.StageDir)
		if removalErr == nil {
			return nil
		}
		// A concurrent disappearance is already the desired result, including
		// when RemoveAll reported a stale error on its last permitted attempt.
		if exists, err := pathExists(journal.StageDir); err == nil && !exists {
			return nil
		}
		if attempts > len(delays) || !retryableStagingRemoval(removalErr) {
			return removalErr
		}
		wait(delays[attempts-1])
		// Revalidate after waiting: another process may have replaced the path.
	}
}

func validateUnpublishedStaging(journal localJournal) (bool, error) {
	runID, err := hex.DecodeString(journal.RunID)
	if err != nil || len(runID) != 12 || journal.StageIdentity == "" ||
		!filepath.IsAbs(journal.SourceDir) || !filepath.IsAbs(journal.StageDir) ||
		!sameLocalPath(journal.SourceDir, filepath.Clean(journal.SourceDir)) ||
		!sameLocalPath(journal.StageDir, filepath.Clean(journal.StageDir)) {
		return false, errors.New("refusing to remove an unrecognized staging path")
	}
	parent := filepath.Dir(journal.SourceDir)
	expected := filepath.Join(parent, "."+filepath.Base(journal.SourceDir)+".v013-stage-"+journal.RunID)
	if sameLocalPath(parent, journal.SourceDir) || !sameLocalPath(journal.StageDir, expected) {
		return false, errors.New("refusing to remove a staging path outside this migration run")
	}
	exists, err := pathExists(journal.StageDir)
	if err != nil || !exists {
		return exists, err
	}
	if err := validatePath(journal.StageDir, true); err != nil {
		return false, err
	}
	actual, err := directoryIdentity(journal.StageDir)
	if err != nil {
		return false, err
	}
	if actual != journal.StageIdentity {
		return false, errors.New("staging directory identity changed; preserved for inspection")
	}
	return true, nil
}
