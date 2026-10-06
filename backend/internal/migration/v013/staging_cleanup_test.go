package v013

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func unpublishedStagingFixture(t *testing.T) localJournal {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "LinguaFlow")
	runID := "0123456789abcdef01234567"
	journal := localJournal{SourceDir: source, RunID: runID,
		StageDir: filepath.Join(parent, ".LinguaFlow.v013-stage-"+runID)}
	for _, path := range []string{source, journal.StageDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := directoryIdentity(journal.StageDir)
	if err != nil {
		t.Fatal(err)
	}
	journal.StageIdentity = identity
	return journal
}

func unexpectedStagingWait(t *testing.T) func(time.Duration) {
	t.Helper()
	return func(delay time.Duration) { t.Fatalf("unexpected cleanup delay: %s", delay) }
}

func TestUnpublishedStagingCleanupOnlyRemovesItsOwnRun(t *testing.T) {
	journal := unpublishedStagingFixture(t)
	unrelated := filepath.Join(filepath.Dir(journal.StageDir), ".LinguaFlow.v013-stage-ffffffffffffffffffffffff")
	if err := os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(unrelated, "preserve.txt")
	if err := os.WriteFile(marker, []byte("unrelated staging"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := cleanupUnpublishedStagingWith(journal, func(path string) error {
		calls++
		return os.RemoveAll(path)
	}, unexpectedStagingWait(t))
	if err != nil || calls != 1 {
		t.Fatalf("cleanup calls=%d err=%v", calls, err)
	}
	if exists, err := pathExists(journal.StageDir); err != nil || exists {
		t.Fatalf("stage survived cleanup: %v", err)
	}
	if string(readLocalBytes(t, marker)) != "unrelated staging" {
		t.Fatal("unrelated staging directory changed")
	}
	if exists, err := pathExists(journal.SourceDir); err != nil || !exists {
		t.Fatalf("original directory removed: %v", err)
	}
}

func TestUnpublishedStagingCleanupMissingDirectory(t *testing.T) {
	for _, disappearsDuringRemoval := range []bool{false, true} {
		t.Run(map[bool]string{false: "already missing", true: "disappears during removal"}[disappearsDuringRemoval], func(t *testing.T) {
			journal := unpublishedStagingFixture(t)
			if !disappearsDuringRemoval {
				if err := os.Remove(journal.StageDir); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			err := cleanupUnpublishedStagingWith(journal, func(path string) error {
				calls++
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				return errors.New("stale removal error after directory disappeared")
			}, unexpectedStagingWait(t))
			if err != nil || (calls == 1) != disappearsDuringRemoval {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestUnpublishedStagingCleanupRejectsUnsafeTargets(t *testing.T) {
	for _, kind := range []string{"relative source", "relative stage", "source root", "wrong parent", "other run", "malformed run", "empty identity", "changed identity", "unclean stage"} {
		t.Run(kind, func(t *testing.T) {
			journal := unpublishedStagingFixture(t)
			stage := journal.StageDir
			switch kind {
			case "relative source":
				journal.SourceDir = "LinguaFlow"
			case "relative stage":
				journal.StageDir = filepath.Base(stage)
			case "source root":
				journal.SourceDir = filepath.VolumeName(stage) + string(os.PathSeparator)
			case "wrong parent":
				journal.SourceDir = filepath.Join(t.TempDir(), "LinguaFlow")
			case "other run":
				journal.RunID = "ffffffffffffffffffffffff"
			case "malformed run":
				journal.RunID = "not-a-run-id"
			case "empty identity":
				journal.StageIdentity = ""
			case "changed identity":
				journal.StageIdentity = "different-directory"
			case "unclean stage":
				journal.StageDir += string(os.PathSeparator) + "."
			}
			err := cleanupUnpublishedStagingWith(journal, func(string) error {
				t.Fatal("unsafe target reached removal")
				return nil
			}, unexpectedStagingWait(t))
			if err == nil || !strings.Contains(err.Error(), "after 0 removal attempts") {
				t.Fatalf("unsafe target error=%v", err)
			}
			if exists, err := pathExists(stage); err != nil || !exists {
				t.Fatalf("unsafe cleanup changed staging: %v", err)
			}
		})
	}
}

func TestUnpublishedStagingCleanupRejectsLinkedDirectory(t *testing.T) {
	journal := unpublishedStagingFixture(t)
	moved := journal.StageDir + "-moved"
	if err := os.Rename(journal.StageDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, journal.StageDir); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	err := cleanupUnpublishedStagingWith(journal, func(string) error {
		t.Fatal("linked target reached removal")
		return nil
	}, unexpectedStagingWait(t))
	if err == nil {
		t.Fatal("linked staging directory accepted")
	}
	if exists, err := pathExists(moved); err != nil || !exists {
		t.Fatalf("symlink target changed: %v", err)
	}
}

func TestSQLiteUnpublishedCleanupReportsPrimaryAndCleanupFailures(t *testing.T) {
	for _, failConversion := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup only", true: "conversion and cleanup"}[failConversion], func(t *testing.T) {
			dir := localFixture(t, true)
			if failConversion {
				db := openLocalFixture(t, dir)
				if _, err := db.Exec(`UPDATE jobs SET execution_config='{"rounds":[]}' WHERE id=1`); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
			cleanupErr := errors.New("injected permanent cleanup failure")
			var stage string
			calls := 0
			report, err := runSQLite(t.Context(), SQLiteOptions{DataDir: dir, Mode: config.ModeLocal, Apply: failConversion}, func(journal localJournal) error {
				stage = journal.StageDir
				lock := filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-migration.lock")
				if unlock, err := acquireLocalLock(lock); err == nil {
					_ = unlock()
					t.Fatal("migration lock released before cleanup")
				}
				return cleanupUnpublishedStagingWith(journal, func(string) error {
					calls++
					return cleanupErr
				}, unexpectedStagingWait(t))
			})
			if !errors.Is(err, cleanupErr) || report.Applied || calls != 1 || stage == "" {
				t.Fatalf("report=%+v calls=%d err=%v", report, calls, err)
			}
			for _, text := range []string{stage, "after 1 removal attempts", "was not replaced", "residual files may remain"} {
				if !strings.Contains(err.Error(), text) {
					t.Fatalf("cleanup error lacks %q: %v", text, err)
				}
			}
			if failConversion && !strings.Contains(err.Error(), "migrate job 1 execution") {
				t.Fatalf("primary conversion failure lost: %v", err)
			}
			if !bytes.Equal(before, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) ||
				string(readLocalBytes(t, filepath.Join(dir, "jobs", "resources", "test.txt"))) != "original input" {
				t.Fatal("unpublished migration changed source data")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-switch.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("switch journal published: %v", err)
			}
			backups, err := filepath.Glob(dir + ".v013-backup-*")
			if err != nil || len(backups) != 0 {
				t.Fatalf("backup published: %v %v", backups, err)
			}
			unlock, err := acquireLocalLock(filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-migration.lock"))
			if err != nil {
				t.Fatalf("migration lock remained held after cleanup: %v", err)
			}
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
