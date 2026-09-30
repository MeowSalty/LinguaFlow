package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdministratorPasswordInput(t *testing.T) {
	value, err := readAdministratorPassword(strings.NewReader("  password  \r\n"), "", true)
	if err != nil || value != "  password  " {
		t.Fatalf("password was altered: %q %v", value, err)
	}
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte("password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := readAdministratorPassword(nil, file, false); err != nil || value != "password" {
		t.Fatalf("file input=%q %v", value, err)
	}
	for _, test := range []struct {
		file  string
		stdin bool
	}{{"", false}, {file, true}, {file + "-missing", false}} {
		if _, err := readAdministratorPassword(strings.NewReader("secret"), test.file, test.stdin); err == nil {
			t.Fatal("invalid password source accepted")
		}
	}
}
