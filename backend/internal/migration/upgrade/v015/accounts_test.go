package v015

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func TestAccountMappingCompleteAllocationAndEmailNull(t *testing.T) {
	db, _, hash := releaseFixture(t, "serve")
	users, err := ReadUsers(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	users = append(users, SourceUser{ID: 2, Username: "ALICE", Email: "Bad Email", PasswordHash: hash, Role: "user", Active: true}, SourceUser{ID: 3, Username: "alice", Email: "different@example.invalid", PasswordHash: hash, Role: "user", Active: true})
	if _, err := MapAccounts(users, Mapping{}, "serve", 0); err == nil {
		t.Fatal("normalized collision or invalid email accepted")
	}
	path := filepath.Join(t.TempDir(), "mapping.json")
	if err := operation.Write(path, map[string]any{"2": map[string]any{"username": "bob", "email": nil}, "3": map[string]any{"username": "alice"}}, true); err != nil {
		t.Fatal(err)
	}
	mapping, err := ReadMapping(path)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := MapAccounts(users, mapping, "serve", 0)
	if err != nil {
		t.Fatal(err)
	}
	if accounts[1].UserID != 2 || accounts[1].Username != "bob" || accounts[1].Email != nil || accounts[2].Username != "alice" {
		t.Fatal(accounts)
	}
	for _, input := range []map[string]any{{"01": map[string]any{"username": "newname"}}, {"2": map[string]any{"role": "admin"}}, {"2": map[string]any{"username": nil}}} {
		path := filepath.Join(t.TempDir(), "invalid.json")
		if err := operation.Write(path, input, true); err != nil {
			t.Fatal(err)
		}
		mapping, err := ReadMapping(path)
		if err == nil {
			_, err = MapAccounts(users, mapping, "serve", 0)
		}
		if err == nil {
			t.Fatal("unsupported mapping accepted")
		}
	}
}

func TestAccountMappingReservedHashAndOriginalLocalIdentity(t *testing.T) {
	db, _, hash := releaseFixture(t, "local")
	users, err := ReadUsers(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	name := "local"
	users = append(users, SourceUser{ID: 9, Username: name, Email: "user@example.invalid", PasswordHash: hash, Role: "user", Active: true})
	if _, err := MapAccounts(users, Mapping{}, "local", 1); err == nil {
		t.Fatal("public local name accepted")
	}
	users = users[:1]
	replacement := "renamed"
	mapping := Mapping{"1": {Username: &replacement}}
	if _, err := MapAccounts(users, mapping, "local", 1); err == nil {
		t.Fatal("original local identity renamed")
	}
	for _, bad := range []string{"", hash[:59], "$2b$03" + hash[6:], hash[:28] + "A" + hash[29:], hash[:59] + "B"} {
		users[0].PasswordHash = bad
		_, err := MapAccounts(users, Mapping{}, "local", 1)
		var known *operation.Error
		if !errors.As(err, &known) || known.Code != operation.UnsupportedSource {
			t.Fatalf("bad bcrypt accepted: %v", err)
		}
	}
}
