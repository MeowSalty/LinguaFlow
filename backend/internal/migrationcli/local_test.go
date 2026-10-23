package migrationcli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/v013"
)

func TestMigrationCommandsRejectBeforeInputsOrSideEffects(t *testing.T) {
	for _, command := range []string{"postgres", "sqlite", "local"} {
		for _, apply := range []bool{false, true} {
			t.Run(command+map[bool]string{false: "/rehearsal", true: "/apply"}[apply], func(t *testing.T) {
				clearDeploymentEnvironment(t)
				parent := t.TempDir()
				dir := filepath.Join(parent, "existing-data")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				original := []byte("preserved existing database and recovery data")
				path := filepath.Join(dir, "linguaflow.db")
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				// If secrets were read or PostgreSQL connected first, the error
				// would differ. Compatibility failure must precede either action.
				t.Setenv("LINGUAFLOW_DATABASE_DSN_FILE", filepath.Join(parent, "missing-secret"))
				t.Setenv("LINGUAFLOW_JWT_SECRET_FILE", filepath.Join(parent, "missing-jwt"))
				root := NewCommand()
				args := []string{"v013", command, "--data-dir", dir}
				if command == "sqlite" {
					args = append(args, "--mode", "serve")
				}
				if apply {
					args = append(args, "--apply")
				}
				root.SetArgs(args)
				if err := root.Execute(); !errors.Is(err, v013.ErrTargetIncompatible) {
					t.Fatalf("command reached input/connection handling: %v", err)
				}
				after, err := os.ReadFile(path)
				if err != nil || !reflect.DeepEqual(original, after) {
					t.Fatalf("existing database changed: %v", err)
				}
				entries, err := os.ReadDir(parent)
				if err != nil || len(entries) != 1 || entries[0].Name() != "existing-data" {
					t.Fatalf("rejection created staging or lock files: %v %v", entries, err)
				}
				entries, err = os.ReadDir(dir)
				if err != nil || len(entries) != 1 || entries[0].Name() != "linguaflow.db" {
					t.Fatalf("rejection created keys or metadata: %v %v", entries, err)
				}
			})
		}
	}
}
