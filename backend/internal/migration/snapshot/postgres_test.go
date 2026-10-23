package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePostgresToolVersionKeepsCompleteVersion(t *testing.T) {
	for _, test := range []struct {
		output string
		want   string
	}{
		{output: "pg_dump (PostgreSQL) 16.4\n", want: "16.4"},
		{output: "pg_restore (PostgreSQL) 9.6.24\n", want: "9.6.24"},
	} {
		got, ok := parsePostgresToolVersion(test.output)
		if !ok || got != test.want {
			t.Fatalf("parsePostgresToolVersion(%q)=(%q,%t), want %q,true", test.output, got, ok, test.want)
		}
	}
	if _, ok := parsePostgresToolVersion("pg_dump (PostgreSQL) development"); ok {
		t.Fatal("accepted an unversioned PostgreSQL tool")
	}
}

func TestPostgresRestoreUsesParsedDatabaseAndReadOnlySnapshotPath(t *testing.T) {
	database, err := postgresRestoreDatabase("postgres://restore-user:secret@example.test:5433/restore_target?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.dump")
	if err := os.WriteFile(path, []byte("dump"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePostgresSnapshotPath(path); err != nil {
		t.Fatal(err)
	}
	args := postgresRestoreArgs(database, path)
	if args[len(args)-2] != "--dbname=restore_target" {
		t.Fatalf("restore args=%v", args)
	}
	if strings.Contains(strings.Join(args, " "), "secret") {
		t.Fatal("restore args contain the DSN password")
	}
}

func TestPostgresEnvironmentBindsDSNAndClearsRoutingEnv(t *testing.T) {
	serviceFile := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(serviceFile, []byte("[review]\nhost=service.example\nport=6543\nuser=service-user\npassword=service-password\ndbname=service-db\nsslmode=disable\n"), 0o600); err != nil {
		t.Fatal("create service fixture")
	}
	t.Setenv("PGHOST", "inherited.example")
	t.Setenv("PGHOSTADDR", "192.0.2.44")
	t.Setenv("PGPORT", "6544")
	t.Setenv("PGUSER", "inherited-user")
	t.Setenv("PGPASSWORD", "inherited-password")
	t.Setenv("PGDATABASE", "inherited-db")
	t.Setenv("PGSERVICE", "review")
	t.Setenv("PGSERVICEFILE", serviceFile)
	t.Setenv("PGSSLMODE", "require")
	t.Setenv("PGSSLROOTCERT", "system")
	t.Setenv("PGSSLCRL", "inherited-crl.pem")
	t.Setenv("PGSSLPASSWORD", "inherited-password")
	t.Setenv("PGTARGETSESSIONATTRS", "any")
	environment, err := postgresEnvironment("postgres://user:secret@example.test:5433/parsed_db?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, value := range environment {
		key, item, ok := strings.Cut(value, "=")
		if ok {
			values[key] = item
		}
	}
	for key, want := range map[string]string{
		"PGHOST":        "example.test",
		"PGPORT":        "5433",
		"PGUSER":        "user",
		"PGPASSWORD":    "secret",
		"PGDATABASE":    "parsed_db",
		"PGSSLMODE":     "verify-full",
		"PGSSLROOTCERT": "system",
		"PGSSLPASSWORD": "inherited-password",
	} {
		if values[key] != want {
			t.Fatalf("%s did not match the explicit DSN or pgx SSL setting", key)
		}
	}
	for _, key := range []string{"PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE", "PGSSLCRL", "PGTARGETSESSIONATTRS"} {
		if _, ok := values[key]; ok {
			t.Fatalf("inherited routing variable %s reached the dump/restore process", key)
		}
	}
}

func TestPostgresEnvironmentRejectsDistinctFallbackEndpoints(t *testing.T) {
	_, err := postgresEnvironment("postgres://app:must-not-leak@primary.example:5432,secondary.example:5433/target?sslmode=verify-full")
	if err == nil {
		t.Fatal("accepted a PostgreSQL fallback that can select a different server")
	}
	if !strings.Contains(err.Error(), "single checked host and port") {
		t.Fatalf("distinct fallback was rejected for an unexpected reason: %v", err)
	}
	if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "primary.example") || strings.Contains(err.Error(), "secondary.example") {
		t.Fatal("PostgreSQL endpoint rejection exposed connection details")
	}
}

func TestPostgresEnvironmentKeepsSameEndpointTLSFallback(t *testing.T) {
	environment, err := postgresEnvironment("postgres://app:secret@primary.example:5432/target?sslmode=prefer")
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, value := range environment {
		key, item, ok := strings.Cut(value, "=")
		if ok {
			values[key] = item
		}
	}
	if values["PGHOST"] != "primary.example" || values["PGPORT"] != "5432" || values["PGSSLMODE"] != "prefer" {
		t.Fatalf("same-endpoint TLS fallback was not preserved: host=%q port=%q sslmode=%q", values["PGHOST"], values["PGPORT"], values["PGSSLMODE"])
	}
}

func TestPostgresEnvironmentUsesEffectiveKeywordSSLMode(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGSERVICEFILE", "")
	t.Setenv("PGSSLMODE", "require")
	environment, err := postgresEnvironment("host=example.test port=5432 user=app password=secret dbname=parsed_db sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range environment {
		key, item, ok := strings.Cut(value, "=")
		if ok && key == "PGSSLMODE" && item == "disable" {
			return
		}
	}
	t.Fatal("dump/restore SSL mode did not match the effective keyword DSN")
}

func TestPostgresEnvironmentErrorsDoNotExposePassword(t *testing.T) {
	_, err := postgresEnvironment("postgres://user:do-not-leak@example.test/parsed_db?sslmode=invalid")
	if err == nil {
		t.Fatal("invalid SSL mode was accepted")
	}
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatal("PostgreSQL configuration error exposed a password")
	}
}
