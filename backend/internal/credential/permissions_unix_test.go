//go:build !windows

package credential

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKeyringChecksUnixPermissionsWithoutRepair(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0400, 0640, 0604, 0620, 0602, 0610, 0601, 0777} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keyring.json")
			if _, err := PrepareKeyring(path, true); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			for _, create := range []bool{false, true} {
				_, err := PrepareKeyring(path, create)
				if mode&0077 == 0 {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "group and other") {
					t.Fatalf("insecure mode accepted or unexplained: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), string(before)) {
					t.Fatal("permission error exposed keyring contents")
				}
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatal("permission check changed file mode")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("permission check changed keyring contents")
			}
		})
	}
}
