package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func init() {
	if os.Getenv("LINGUAFLOW_TEST_POSTGRES_TOOL_HELPER") != "1" {
		return
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	version := os.Getenv("LINGUAFLOW_TEST_" + strings.ToUpper(strings.ReplaceAll(name, "pg_", "PG_")) + "_VERSION")
	if name != "pg_dump" && name != "pg_restore" || version == "" {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString(name + " (PostgreSQL) " + version + "\n")
	os.Exit(0)
}

func installPostgresToolFixture(t *testing.T, names ...string) {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("locate PostgreSQL tool test helper")
	}
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal("read PostgreSQL tool test helper")
	}
	for _, name := range names {
		filename := name
		if runtime.GOOS == "windows" {
			filename += ".exe"
		}
		if err := os.WriteFile(filepath.Join(root, filename), contents, 0o700); err != nil {
			t.Fatal("install simulated PostgreSQL tool")
		}
	}
	t.Setenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN", root)
	t.Setenv("LINGUAFLOW_TEST_POSTGRES_TOOL_HELPER", "1")
	t.Setenv("LINGUAFLOW_TEST_PG_DUMP_VERSION", "16.4")
	t.Setenv("LINGUAFLOW_TEST_PG_RESTORE_VERSION", "16.4")
}

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

func TestPostgresToolMajorUsesPre10MajorMinor(t *testing.T) {
	for version, want := range map[string]string{"9.6.24": "9.6", "10.23": "10", "16.4": "16"} {
		if got := postgresToolMajor(version); got != want {
			t.Fatalf("postgresToolMajor(%q)=%q, want %q", version, got, want)
		}
	}
}

func TestInspectPostgresDumpDoesNotRequireRestoreTool(t *testing.T) {
	installPostgresToolFixture(t, "pg_dump")
	version, err := InspectPostgresDump(context.Background())
	if err != nil || version != "16.4" {
		t.Fatalf("InspectPostgresDump()=(%q,%v), want 16.4,nil", version, err)
	}
	if _, err := InspectPostgresTools(context.Background()); err == nil || !strings.Contains(err.Error(), "pg_restore") {
		t.Fatalf("InspectPostgresTools() error=%v, want missing pg_restore", err)
	}
}

func TestInspectPostgresRestoreDoesNotRequireDumpTool(t *testing.T) {
	installPostgresToolFixture(t, "pg_restore")
	version, err := InspectPostgresRestore(context.Background())
	if err != nil || version != "16.4" {
		t.Fatalf("InspectPostgresRestore()=(%q,%v), want 16.4,nil", version, err)
	}
	if _, err := InspectPostgresDump(context.Background()); err == nil || !strings.Contains(err.Error(), "pg_dump") {
		t.Fatalf("InspectPostgresDump() error=%v, want missing pg_dump", err)
	}
}

func TestInspectPostgresToolsAllowsPatchVersionDifference(t *testing.T) {
	installPostgresToolFixture(t, "pg_dump", "pg_restore")
	t.Setenv("LINGUAFLOW_TEST_PG_DUMP_VERSION", "16.4")
	t.Setenv("LINGUAFLOW_TEST_PG_RESTORE_VERSION", "16.9")
	version, err := InspectPostgresTools(context.Background())
	if err != nil || version != "16.4" {
		t.Fatalf("InspectPostgresTools()=(%q,%v), want dump version 16.4,nil", version, err)
	}
}

func TestInspectPostgresToolsRejectsDifferentMajorVersions(t *testing.T) {
	installPostgresToolFixture(t, "pg_dump", "pg_restore")
	t.Setenv("LINGUAFLOW_TEST_PG_DUMP_VERSION", "16.4")
	t.Setenv("LINGUAFLOW_TEST_PG_RESTORE_VERSION", "17.1")
	if _, err := InspectPostgresTools(context.Background()); err == nil || !strings.Contains(err.Error(), "major versions") {
		t.Fatalf("InspectPostgresTools() error=%v, want major-version mismatch", err)
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
