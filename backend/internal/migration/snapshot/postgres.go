package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func postgresTool(name string) (string, error) {
	toolName := name
	root := os.Getenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN")
	if root == "" {
		root = os.Getenv("LINGUAFLOW_AUTH_POSTGRES_BIN")
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if root != "" {
		path := filepath.Join(root, name)
		if err := operation.ValidatePath(path, false); err != nil {
			return "", operation.Fail(operation.UnsupportedTarget, toolName+" is unavailable in the configured PostgreSQL tools directory")
		}
		return path, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", operation.Fail(operation.UnsupportedTarget, toolName+" is unavailable; install it or configure LINGUAFLOW_MIGRATION_POSTGRES_BIN")
	}
	return path, nil
}

var toolVersion = regexp.MustCompile(`PostgreSQL\)\s+([0-9]+(?:\.[0-9]+)*(?:beta[0-9]+|rc[0-9]+)?)`)

func parsePostgresToolVersion(output string) (string, bool) {
	match := toolVersion.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func inspectPostgresTool(ctx context.Context, name string) (string, error) {
	path, err := postgresTool(name)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", operation.Fail(operation.UnsupportedTarget, "cannot inspect "+name+" version")
	}
	version, ok := parsePostgresToolVersion(string(out))
	if !ok {
		return "", operation.Fail(operation.UnsupportedTarget, "unknown "+name+" version")
	}
	return version, nil
}

func InspectPostgresDump(ctx context.Context) (string, error) {
	return inspectPostgresTool(ctx, "pg_dump")
}

func InspectPostgresRestore(ctx context.Context) (string, error) {
	return inspectPostgresTool(ctx, "pg_restore")
}

func postgresToolMajor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) > 1 {
		major, err := strconv.Atoi(parts[0])
		if err == nil && major < 10 {
			return parts[0] + "." + parts[1]
		}
	}
	return parts[0]
}

func InspectPostgresTools(ctx context.Context) (string, error) {
	dumpVersion, err := InspectPostgresDump(ctx)
	if err != nil {
		return "", err
	}
	restoreVersion, err := InspectPostgresRestore(ctx)
	if err != nil {
		return "", err
	}
	if postgresToolMajor(dumpVersion) != postgresToolMajor(restoreVersion) {
		return "", operation.Fail(operation.UnsupportedTarget, "pg_dump and pg_restore major versions must match")
	}
	return dumpVersion, nil
}

func checkPostgresToolMajor(ctx context.Context, db *sql.DB, version, tool string) error {
	var serverVersion int
	if err := db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::integer").Scan(&serverVersion); err != nil {
		return operation.Fail(operation.NeedsAction, "cannot inspect PostgreSQL server version")
	}
	serverMajor := strconv.Itoa(serverVersion / 10000)
	if serverVersion < 100000 {
		serverMajor += "." + strconv.Itoa(serverVersion/100%100)
	}
	if serverMajor != postgresToolMajor(version) {
		return operation.Fail(operation.UnsupportedTarget, "PostgreSQL server and "+tool+" must use the same major version")
	}
	return nil
}

func checkPostgresMajor(ctx context.Context, db *sql.DB) (string, error) {
	version, err := InspectPostgresDump(ctx)
	if err != nil {
		return "", err
	}
	if err := checkPostgresToolMajor(ctx, db, version, "pg_dump"); err != nil {
		return "", err
	}
	return version, nil
}

func postgresEnvironment(dsn string) ([]string, error) {
	settings, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, operation.Fail(operation.UnsupportedSource, "invalid PostgreSQL configuration")
	}
	parsed, err := url.Parse(dsn)
	isURL := err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql")
	query := url.Values{}
	if isURL {
		rawQuery, parseErr := url.ParseQuery(parsed.RawQuery)
		if parseErr != nil {
			return nil, operation.Fail(operation.UnsupportedSource, "invalid PostgreSQL connection parameters")
		}
		query = make(url.Values, len(rawQuery))
		for name, values := range rawQuery {
			canonical := strings.ToLower(name)
			query[canonical] = append(query[canonical], values...)
		}
	}
	if settings.ValidateConnect != nil {
		return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL target session selection cannot be reproduced by dump/restore tools")
	}
	if hasDistinctPostgresFallback(settings) {
		return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL dump/restore requires a single checked host and port")
	}
	if settings.MinProtocolVersion != "3.0" || settings.MaxProtocolVersion != "3.0" || settings.RequireAuth != "" || settings.ChannelBinding != "" && settings.ChannelBinding != "prefer" || settings.KerberosSrvName != "" || settings.KerberosSpn != "" {
		return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL authentication or protocol settings cannot be reproduced by dump/restore tools")
	}
	for name := range settings.RuntimeParams {
		switch name {
		case "application_name", "client_encoding", "timezone":
		default:
			return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL runtime settings cannot be reproduced by dump/restore tools")
		}
	}
	sslMode, err := postgresSSLMode(settings, query, isURL)
	if err != nil {
		return nil, err
	}
	hosts, ports := postgresHostsAndPorts(settings)

	set := map[string]string{
		"PGHOST":     hosts,
		"PGPORT":     ports,
		"PGUSER":     settings.User,
		"PGPASSWORD": settings.Password,
		"PGDATABASE": settings.Database,
		"PGSSLMODE":  sslMode,
		"PGOPTIONS":  "-c timezone=UTC",
	}
	if settings.ConnectTimeout > 0 {
		set["PGCONNECT_TIMEOUT"] = strconv.FormatInt(int64(settings.ConnectTimeout/time.Second), 10)
	}
	for name, value := range settings.RuntimeParams {
		switch name {
		case "application_name":
			set["PGAPPNAME"] = value
		case "client_encoding":
			set["PGCLIENTENCODING"] = value
		}
	}
	if settings.SSLNegotiation != "" {
		set["PGSSLNEGOTIATION"] = settings.SSLNegotiation
	}
	for _, parameter := range []struct {
		name string
		env  string
	}{
		{name: "sslrootcert", env: "PGSSLROOTCERT"},
		{name: "sslcert", env: "PGSSLCERT"},
		{name: "sslkey", env: "PGSSLKEY"},
		{name: "sslpassword", env: "PGSSLPASSWORD"},
		{name: "sslsni", env: "PGSSLSNI"},
		{name: "sslnegotiation", env: "PGSSLNEGOTIATION"},
	} {
		value, present, err := postgresParameter(query, parameter.name, parameter.env)
		if err != nil {
			return nil, err
		}
		if present {
			if value == "" && (parameter.name == "sslrootcert" || parameter.name == "sslcert" || parameter.name == "sslkey") {
				return nil, operation.Fail(operation.UnsupportedTarget, "empty PostgreSQL TLS file parameters cannot be reproduced by dump/restore tools")
			}
			if value != "" {
				set[parameter.env] = value
			}
		}
	}
	if postgresServiceConfigured(query) && settings.TLSConfig != nil {
		if settings.TLSConfig.RootCAs != nil && !postgresParameterPresent(query, "sslrootcert", "PGSSLROOTCERT") {
			return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL service TLS roots cannot be reproduced by dump/restore tools")
		}
		if len(settings.TLSConfig.Certificates) != 0 && (!postgresParameterPresent(query, "sslcert", "PGSSLCERT") || !postgresParameterPresent(query, "sslkey", "PGSSLKEY")) {
			return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL service client certificates cannot be reproduced by dump/restore tools")
		}
	}

	environment := make([]string, 0, len(os.Environ())+len(set))
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if ok && strings.HasPrefix(strings.ToUpper(key), "PG") {
			continue
		}
		environment = append(environment, value)
	}
	for _, key := range []string{
		"PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY", "PGSSLPASSWORD", "PGSSLSNI", "PGSSLNEGOTIATION", "PGCONNECT_TIMEOUT", "PGAPPNAME", "PGCLIENTENCODING", "PGOPTIONS",
	} {
		if value, ok := set[key]; ok {
			environment = append(environment, key+"="+value)
		}
	}
	return environment, nil
}

func postgresParameter(query url.Values, name, env string) (string, bool, error) {
	if values, ok := query[name]; ok {
		if len(values) != 1 {
			return "", false, operation.Fail(operation.UnsupportedTarget, "repeated PostgreSQL TLS parameters cannot be reproduced by dump/restore tools")
		}
		return values[0], true, nil
	}
	value, ok := postgresEnvironmentValue(env)
	return value, ok && value != "", nil
}

func postgresParameterPresent(query url.Values, name, env string) bool {
	_, present, err := postgresParameter(query, name, env)
	return present && err == nil
}

func postgresEnvironmentValue(name string) (string, bool) {
	for _, value := range os.Environ() {
		key, item, ok := strings.Cut(value, "=")
		if ok && strings.EqualFold(key, name) {
			return item, true
		}
	}
	return "", false
}

func postgresServiceConfigured(query url.Values) bool {
	if _, ok := query["service"]; ok {
		return true
	}
	value, ok := postgresEnvironmentValue("PGSERVICE")
	return ok && value != ""
}

func postgresSSLMode(settings *pgx.ConnConfig, query url.Values, isURL bool) (string, error) {
	if isURL {
		if _, ok := query["ssl"]; ok {
			return "", operation.Fail(operation.UnsupportedTarget, "PostgreSQL ssl alias cannot be reproduced by dump/restore tools")
		}
		if values, ok := query["sslmode"]; ok {
			if len(values) != 1 {
				return "", operation.Fail(operation.UnsupportedTarget, "repeated PostgreSQL sslmode cannot be reproduced by dump/restore tools")
			}
			if values[0] == "" {
				return "prefer", nil
			}
			return values[0], nil
		}
	}
	if !isURL && settings.TLSConfig != nil {
		return "", operation.Fail(operation.UnsupportedTarget, "TLS dump/restore requires a URL configuration preserving explicit libpq TLS inputs")
	}
	if isURL && !postgresServiceConfigured(query) {
		if value, ok := postgresEnvironmentValue("PGSSLMODE"); ok && value != "" {
			return value, nil
		}
	}
	plainFallback, tlsFallback := false, false
	for _, fallback := range settings.Fallbacks {
		if fallback.Host != settings.Host || fallback.Port != settings.Port {
			continue
		}
		if fallback.TLSConfig == nil {
			plainFallback = true
		} else {
			tlsFallback = true
		}
	}
	switch {
	case settings.TLSConfig == nil && tlsFallback:
		return "allow", nil
	case settings.TLSConfig == nil:
		return "disable", nil
	case plainFallback:
		return "prefer", nil
	case settings.TLSConfig.InsecureSkipVerify && settings.TLSConfig.VerifyPeerCertificate != nil:
		return "verify-ca", nil
	case settings.TLSConfig.InsecureSkipVerify:
		return "require", nil
	default:
		return "verify-full", nil
	}
}

func postgresHostsAndPorts(settings *pgx.ConnConfig) (string, string) {
	hosts := []string{settings.Host}
	ports := []string{strconv.Itoa(int(settings.Port))}
	seen := map[string]struct{}{settings.Host + "\x00" + ports[0]: {}}
	for _, fallback := range settings.Fallbacks {
		port := strconv.Itoa(int(fallback.Port))
		key := fallback.Host + "\x00" + port
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		hosts = append(hosts, fallback.Host)
		ports = append(ports, port)
	}
	return strings.Join(hosts, ","), strings.Join(ports, ",")
}

func hasDistinctPostgresFallback(settings *pgx.ConnConfig) bool {
	for _, fallback := range settings.Fallbacks {
		if fallback.Host != settings.Host || fallback.Port != settings.Port {
			return true
		}
	}
	return false
}

func postgresRestoreDatabase(dsn string) (string, error) {
	settings, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", operation.Fail(operation.UnsupportedSource, "invalid PostgreSQL configuration")
	}
	if settings.Database == "" {
		return "", operation.Fail(operation.UnsupportedTarget, "PostgreSQL target database is required for restore")
	}
	return settings.Database, nil
}

func postgresRestoreArgs(database, path string) []string {
	return []string{"--no-password", "--no-owner", "--no-acl", "--exit-on-error", "--single-transaction", "--dbname=" + database, path}
}

func validatePostgresSnapshotPath(path string) error {
	if err := operation.ValidatePath(path, false); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func Postgres(ctx context.Context, db *sql.DB, cfg *config.ServerConfig, target string) error {
	if _, err := checkPostgresMajor(ctx, db); err != nil {
		return err
	}
	if err := operation.ValidatePath(filepath.Dir(target), true); err != nil {
		return err
	}
	created, err := credential.PublishPrivateFile(target, nil)
	if err != nil {
		return err
	}
	if !created {
		return operation.Fail(operation.TargetNotEmpty, "PostgreSQL snapshot file exists")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var snapshotID string
	if err := tx.QueryRowContext(ctx, "SELECT pg_export_snapshot()").Scan(&snapshotID); err != nil {
		return err
	}
	path, err := postgresTool("pg_dump")
	if err != nil {
		return err
	}
	environment, err := postgresEnvironment(cfg.DatabaseDSN())
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, path, "--format=custom", "--no-password", "--no-owner", "--no-acl", "--snapshot="+snapshotID, "--file="+target)
	command.Env = environment
	if err := command.Run(); err != nil {
		return operation.Fail(operation.BackupIncomplete, "pg_dump failed; preserve incomplete backup materials")
	}
	file, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return tx.Commit()
}

func RestorePostgres(ctx context.Context, cfg *config.ServerConfig, path string) error {
	if err := validatePostgresSnapshotPath(path); err != nil {
		return err
	}
	database, err := postgresRestoreDatabase(cfg.DatabaseDSN())
	if err != nil {
		return err
	}
	db, _, err := Open(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := EmptyPostgres(ctx, db); err != nil {
		return err
	}
	restoreVersion, err := InspectPostgresRestore(ctx)
	if err != nil {
		return err
	}
	if err := checkPostgresToolMajor(ctx, db, restoreVersion, "pg_restore"); err != nil {
		return err
	}
	tool, err := postgresTool("pg_restore")
	if err != nil {
		return err
	}
	environment, err := postgresEnvironment(cfg.DatabaseDSN())
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, tool, postgresRestoreArgs(database, path)...)
	command.Env = environment
	if err := command.Run(); err != nil {
		return operation.Fail(operation.NeedsAction, "pg_restore failed in the independent target")
	}
	return nil
}
