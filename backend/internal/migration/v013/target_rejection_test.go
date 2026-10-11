package v013

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestLegacySQLiteRejectionPreservesDataAndRecoveryState(t *testing.T) {
	for _, mode := range []string{config.ModeLocal, config.ModeServer} {
		for _, apply := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/rehearsal", true: "/apply"}[apply], func(t *testing.T) {
				dir := localFixture(t, true)
				parent := filepath.Dir(dir)
				// Even a pending historical journal must remain untouched. A
				// compatible historical executable owns recovery of that switch.
				journal := filepath.Join(parent, ".LinguaFlow.v013-switch.json")
				if err := os.WriteFile(journal, []byte("pending historical recovery"), 0600); err != nil {
					t.Fatal(err)
				}
				before := legacyStrategyFiles(t, parent)
				cleanupCalled := false
				report, err := runSQLite(t.Context(), SQLiteOptions{DataDir: dir, Mode: mode, Apply: apply}, func(localJournal) error {
					cleanupCalled = true
					return nil
				})
				if !errors.Is(err, ErrTargetIncompatible) || report.Applied || cleanupCalled {
					t.Fatalf("unsupported SQLite migration reached work: %+v %v", report, err)
				}
				if !reflect.DeepEqual(before, legacyStrategyFiles(t, parent)) {
					t.Fatal("rejection altered database, keys, journal, or created lock/staging files")
				}
			})
		}
	}
}

func TestLegacySQLiteRejectionDoesNotCreateMissingDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "not-created")
	for _, run := range []func() error{
		func() error { _, err := RunLocal(t.Context(), LocalOptions{DataDir: dir, Apply: true}); return err },
		func() error {
			_, err := RunSQLite(t.Context(), SQLiteOptions{DataDir: dir, Mode: config.ModeServer, Apply: true})
			return err
		},
	} {
		if err := run(); !errors.Is(err, ErrTargetIncompatible) {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejection created files: %v %v", entries, err)
	}
}
