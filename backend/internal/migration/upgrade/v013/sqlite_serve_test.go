package v013

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func sqliteServeFixture(t *testing.T) string {
	t.Helper()
	dir := localFixture(t, true)
	db := openLocalFixture(t, dir)
	if _, err := db.Exec(`UPDATE users SET username='server-admin',email='server@example.test' WHERE id=5`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func sqliteServeOptions(t *testing.T, dir string) SQLiteOptions {
	t.Helper()
	keys, pending, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	return SQLiteOptions{
		DataDir: dir, Mode: config.ModeServer, Keys: keys,
		KeyringFile: filepath.Join(t.TempDir(), "external-keyring.json"), PendingKeyring: pending,
		JWTSecret: strings.Repeat("retained-server-jwt", 3),
	}
}
