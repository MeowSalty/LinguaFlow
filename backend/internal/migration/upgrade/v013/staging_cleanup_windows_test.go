//go:build windows

package v013

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsUnpublishedStagingCleanupRetriesSelectedErrors(t *testing.T) {
	for _, code := range []error{windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		for _, failures := range []int{1, 4, 5} {
			t.Run(fmt.Sprintf("%v/failures-%d", code, failures), func(t *testing.T) {
				journal := unpublishedStagingFixture(t)
				pathErr := &os.PathError{Op: "unlinkat", Path: filepath.Join(journal.StageDir, "resource.epub"), Err: code}
				calls := 0
				var delays []time.Duration
				err := cleanupUnpublishedStagingWith(journal, func(path string) error {
					calls++
					if calls <= failures {
						return fmt.Errorf("wrapped deletion failure: %w", pathErr)
					}
					return os.RemoveAll(path)
				}, func(delay time.Duration) { delays = append(delays, delay) })
				wantCalls := min(failures+1, 5)
				wantDelays := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
				if calls != wantCalls || !slices.Equal(delays, wantDelays[:wantCalls-1]) {
					t.Fatalf("calls=%d delays=%v", calls, delays)
				}
				if failures < 5 {
					if err != nil {
						t.Fatal(err)
					}
					if exists, err := pathExists(journal.StageDir); err != nil || exists {
						t.Fatalf("stage survived successful retry: %v", err)
					}
					return
				}
				var gotPathErr *os.PathError
				if !errors.Is(err, code) || !errors.As(err, &gotPathErr) || gotPathErr != pathErr {
					t.Fatalf("native error chain lost: %v", err)
				}
				if !strings.Contains(err.Error(), journal.StageDir) || !strings.Contains(err.Error(), "after 5 removal attempts") {
					t.Fatalf("missing residual directory or attempts: %v", err)
				}
			})
		}
	}
}

func TestWindowsUnpublishedStagingCleanupDoesNotRetryOtherErrors(t *testing.T) {
	for _, cause := range []error{windows.ERROR_DISK_FULL, windows.ERROR_INVALID_NAME, os.ErrPermission, errors.New("Access is denied.")} {
		t.Run(cause.Error(), func(t *testing.T) {
			journal := unpublishedStagingFixture(t)
			calls := 0
			err := cleanupUnpublishedStagingWith(journal, func(string) error {
				calls++
				return cause
			}, unexpectedStagingWait(t))
			if !errors.Is(err, cause) || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestWindowsUnpublishedStagingCleanupRevalidatesAfterWaiting(t *testing.T) {
	for _, change := range []string{"replaced", "missing"} {
		t.Run(change, func(t *testing.T) {
			journal := unpublishedStagingFixture(t)
			calls, waits := 0, 0
			err := cleanupUnpublishedStagingWith(journal, func(string) error {
				calls++
				return windows.ERROR_ACCESS_DENIED
			}, func(time.Duration) {
				waits++
				if change == "missing" {
					if err := os.Remove(journal.StageDir); err != nil {
						t.Fatal(err)
					}
					return
				}
				if err := os.Rename(journal.StageDir, journal.StageDir+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(journal.StageDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(journal.StageDir, "preserve.txt"), []byte("replacement directory"), 0600); err != nil {
					t.Fatal(err)
				}
			})
			if calls != 1 || waits != 1 {
				t.Fatalf("calls=%d waits=%d", calls, waits)
			}
			if change == "missing" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "identity changed") || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				t.Fatalf("replacement was not rejected: %v", err)
			}
			if string(readLocalBytes(t, filepath.Join(journal.StageDir, "preserve.txt"))) != "replacement directory" {
				t.Fatal("replacement directory changed")
			}
		})
	}
}

func TestWindowsUnpublishedStagingCleanupRejectsJunction(t *testing.T) {
	journal := unpublishedStagingFixture(t)
	external := t.TempDir()
	marker := filepath.Join(external, "preserve.txt")
	if err := os.WriteFile(marker, []byte("external directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(journal.StageDir); err != nil {
		t.Fatal(err)
	}
	// Junctions exercise the reparse check without requiring symlink privilege.
	if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", journal.StageDir, external).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v %s", err, output)
	}
	err := cleanupUnpublishedStagingWith(journal, func(string) error {
		t.Fatal("junction reached removal")
		return nil
	}, unexpectedStagingWait(t))
	if err == nil || !strings.Contains(err.Error(), "after 0 removal attempts") {
		t.Fatalf("junction was not refused before removal: %v", err)
	}
	if string(readLocalBytes(t, marker)) != "external directory" {
		t.Fatal("junction target changed")
	}
}

func holdStagingFileWithoutDeleteSharing(t *testing.T, path string) func() {
	t.Helper()
	native, err := windows.UTF16PtrFromString(`\\?\` + filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(native, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	closeHandle := func() {
		if !closed {
			closed = true
			if err := windows.CloseHandle(handle); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(closeHandle)
	return closeHandle
}

func TestWindowsUnpublishedStagingCleanupHandlesUnicodeEPUBInUse(t *testing.T) {
	for _, release := range []bool{true, false} {
		t.Run(map[bool]string{true: "released after first failure", false: "still held after retries"}[release], func(t *testing.T) {
			journal := unpublishedStagingFixture(t)
			resource := filepath.Join(journal.StageDir, "jobs", "resources", "project-1", "0917bfc5548d4671", "落第賢者の学院無双 〜二度転生した最強賢者、400年後の世界を魔剣で無双〜.epub")
			if err := os.MkdirAll(filepath.Dir(resource), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(resource, []byte("synthetic EPUB resource bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			closeHandle := holdStagingFileWithoutDeleteSharing(t, resource)
			calls := 0
			var delays []time.Duration
			err := cleanupUnpublishedStagingWith(journal, func(path string) error {
				calls++
				return os.RemoveAll(path)
			}, func(delay time.Duration) {
				delays = append(delays, delay)
				if release {
					closeHandle()
				}
			})
			if release {
				if err != nil || calls != 2 || !slices.Equal(delays, []time.Duration{50 * time.Millisecond}) {
					t.Fatalf("released file: calls=%d delays=%v err=%v", calls, delays, err)
				}
				if exists, err := pathExists(journal.StageDir); err != nil || exists {
					t.Fatalf("stage survived successful retry: %v", err)
				}
				return
			}
			var pathErr *os.PathError
			if err == nil || calls != 5 || len(delays) != 4 || !errors.As(err, &pathErr) || !retryableStagingRemoval(err) {
				t.Fatalf("held file: calls=%d delays=%v err=%v", calls, delays, err)
			}
			if !strings.Contains(err.Error(), journal.StageDir) || !strings.Contains(err.Error(), "was not replaced") || !strings.Contains(pathErr.Path, ".epub") {
				t.Fatalf("residual file error lacks context: %v", err)
			}
			if string(readLocalBytes(t, resource)) != "synthetic EPUB resource bytes" {
				t.Fatal("held resource changed")
			}
			closeHandle()
		})
	}
}
