//go:build windows

package v013

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func TestSQLiteServeRejectsWindowsKeyringNamespaceAliases(t *testing.T) {
	for _, prefix := range []string{`\\?\`, `\\.\`} {
		for _, existing := range []bool{false, true} {
			t.Run(prefix+map[bool]string{false: "pending", true: "existing"}[existing], func(t *testing.T) {
				source, path, options := windowsSQLitePathFixture(t, existing)
				options.KeyringFile = prefix + path
				assertWindowsSQLiteAliasRejected(t, source, path, options)
			})
		}
	}
}

func TestSQLiteServeRejectsWindowsShortKeyringPaths(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "existing"}[existing], func(t *testing.T) {
			source, path, options := windowsSQLitePathFixture(t, existing)
			// The absent target case must still inspect its nearest existing
			// ancestor, rather than accepting a short alias as an external path.
			options.KeyringFile = filepath.Join(windowsSQLiteShortPath(t, filepath.Dir(path)), filepath.Base(path))
			assertWindowsSQLiteAliasRejected(t, source, path, options)
		})
	}
}

func TestSQLiteServeRejectsWindowsShortDataDirectory(t *testing.T) {
	source, path, options := windowsSQLitePathFixture(t, true)
	options.DataDir = windowsSQLiteShortPath(t, source)
	assertWindowsSQLiteAliasRejected(t, source, path, options)
}

func TestSQLiteWindowsPathSpellingAllowsStandardPaths(t *testing.T) {
	source, path, _ := windowsSQLitePathFixture(t, true)
	for _, candidate := range []string{source, path, filepath.Join(source, "absent parent", "keyring.json")} {
		if err := validateSQLitePathSpelling(candidate); err != nil {
			t.Fatalf("standard path rejected: %v", err)
		}
	}
}

func windowsSQLitePathFixture(t *testing.T, existing bool) (string, string, SQLiteOptions) {
	t.Helper()
	source := sqliteServeFixture(t)
	path := filepath.Join(source, "private credential directory", "keyring.json")
	if err := credential.CreatePrivateDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	keys, pending, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	if existing {
		if _, err := credential.PublishPrivateFile(path, pending); err != nil {
			t.Fatal(err)
		}
		pending = nil
	}
	return source, path, SQLiteOptions{
		DataDir: source, Apply: true, Mode: config.ModeServer, Keys: keys,
		KeyringFile: path, PendingKeyring: pending, JWTSecret: strings.Repeat("server-jwt", 4),
	}
}

func windowsSQLiteShortPath(t *testing.T, path string) string {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatal(err)
	}
	if n >= uint32(len(buffer)) {
		t.Fatal("short test path exceeds Windows path limit")
	}
	short := windows.UTF16ToString(buffer[:n])
	if sameLocalPath(short, path) {
		t.Skip("the temporary volume does not provide an 8.3 alias")
	}
	return short
}

func assertWindowsSQLiteAliasRejected(t *testing.T, source, keyring string, options SQLiteOptions) {
	t.Helper()
	databaseBefore := readLocalBytes(t, filepath.Join(source, localDatabaseName))
	keyBefore, keyBeforeErr := os.ReadFile(keyring)
	report, err := RunSQLite(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "standard absolute paths") || report.Applied {
		t.Fatalf("Windows path alias was not rejected before migration: %+v %v", report, err)
	}
	if !bytes.Equal(databaseBefore, readLocalBytes(t, filepath.Join(source, localDatabaseName))) {
		t.Fatal("path validation changed the original SQLite database")
	}
	keyAfter, keyAfterErr := os.ReadFile(keyring)
	if (keyBeforeErr == nil) != (keyAfterErr == nil) || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("path validation changed or published the original keyring")
	}
	parent, base := filepath.Dir(source), filepath.Base(source)
	for _, pattern := range []string{"." + base + ".v013-*", base + ".v013-backup-*"} {
		matches, err := filepath.Glob(filepath.Join(parent, pattern))
		if err != nil || len(matches) != 0 {
			t.Fatalf("path validation created migration state: %v %v", matches, err)
		}
	}
}
