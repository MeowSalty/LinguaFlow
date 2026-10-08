package v013

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// These columns belong to release 416b7fc909dda3ed9179bcdbaf4ba47b6b55779d.
// Do not derive this inventory or the accepted source encodings from the current
// ent schema or a newer SQLite driver's permissive timestamp decoder.
var sqliteLegacyTimes = map[string][]string{
	"activity_logs":                {"created_at", "updated_at"},
	"backends":                     {"created_at", "updated_at"},
	"bootstrap_prompt_templates":   {"created_at", "updated_at"},
	"execution_plan_templates":     {"created_at", "updated_at"},
	"execution_profiles":           {"created_at", "updated_at"},
	"glossary_entries":             {"created_at", "updated_at"},
	"jobs":                         {"created_at", "updated_at", "started_at"},
	"job_resources":                {"created_at", "updated_at", "started_at"},
	"job_rounds":                   {"created_at", "updated_at", "started_at", "finished_at"},
	"job_round_segments":           {},
	"org_memberships":              {"created_at", "updated_at"},
	"organizations":                {"created_at", "updated_at"},
	"projects":                     {"created_at", "updated_at"},
	"prune_prompt_templates":       {"created_at", "updated_at"},
	"refresh_tokens":               {"created_at", "updated_at", "expires_at", "revoked_at"},
	"resources":                    {"created_at", "updated_at"},
	"sse_events":                   {"created_at"},
	"segments":                     {"created_at", "updated_at"},
	"segment_revisions":            {"created_at"},
	"sync_tasks":                   {"created_at", "updated_at", "cancelled_at"},
	"system_settings":              {"created_at", "updated_at"},
	"tm_entries":                   {"created_at", "updated_at"},
	"translation_prompt_templates": {"created_at", "updated_at"},
	"usage_records":                {"created_at", "updated_at"},
	"users":                        {"created_at", "updated_at"},
}

const sqliteTargetTimeLayout = "2006-01-02T15:04:05.000000000Z"

var sqliteMonotonicSuffix = regexp.MustCompile(`^[+-][0-9]+(?:\.[0-9]+)?$`)
var sqliteFraction = regexp.MustCompile(`:[0-9]{2}[.,]([0-9]+)`)

func sqliteURI(path, mode string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{"mode": {mode}}
	uri.RawQuery = query.Encode()
	return uri.String()
}

func snapshotSQLite(ctx context.Context, source, target string) (err error) {
	if err := validatePath(source, false); err != nil {
		return err
	}
	// SQLite discovers sidecars itself when opening the main file. Validate
	// them before opening any connection, not later during ordinary copying.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		exists, err := pathExists(source + suffix)
		if err != nil {
			return err
		}
		if exists {
			if err := validatePath(source+suffix, false); err != nil {
				return err
			}
		}
	}
	db, err := sql.Open("sqlite", sqliteURI(source, "ro"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	return conn.Raw(func(driverConn any) (err error) {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not provide consistent backups")
		}
		backup, err := backuper.NewBackup(sqliteURI(target, "rwc"))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(256)
			if err != nil || !more {
				return err
			}
		}
	})
}

func convertSQLiteTimes(ctx context.Context, path string) (err error) {
	db, err := sql.Open("sqlite", sqliteURI(path, "rw"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 0 {
		return fmt.Errorf("expected unmigrated v0.13.0 SQLite format 0, found %d", version)
	}
	if err := inspectSQLiteLegacySchema(ctx, tx); err != nil {
		return err
	}
	tables := make([]string, 0, len(sqliteLegacyTimes))
	for table := range sqliteLegacyTimes {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		for _, column := range sqliteLegacyTimes[table] {
			if err := convertSQLiteColumn(ctx, tx, table, column); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version=1"); err != nil {
		return err
	}
	return tx.Commit()
}

func inspectSQLiteLegacySchema(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT name, type FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*' AND type <> 'index'")
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		if _, ok := sqliteLegacyTimes[name]; !ok || kind != "table" {
			rows.Close()
			return errors.New("SQLite schema is not the supported v0.13.0 schema (unexpected table, view or trigger)")
		}
		found[name] = true
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(found) != len(sqliteLegacyTimes) {
		return errors.New("SQLite v0.13.0 schema is incomplete")
	}
	for table, columns := range sqliteLegacyTimes {
		var invalidIDs int
		if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM "%s" WHERE id <= 0`, table)).Scan(&invalidIDs); err != nil {
			return err
		}
		if invalidIDs != 0 {
			return fmt.Errorf("unsupported nonpositive record ID in legacy table %s", table)
		}
		for _, column := range columns {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info(?) WHERE name=? AND lower(type)='datetime'", table, column).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("unsupported legacy timestamp column %s.%s", table, column)
			}
		}
	}
	return nil
}

func convertSQLiteColumn(ctx context.Context, tx *sql.Tx, table, column string) error {
	// Both identifiers come only from the frozen inventory above. Batch before
	// updating, so no cursor remains active while its own table is modified.
	query := fmt.Sprintf(`SELECT id, typeof("%s"), CAST("%s" AS TEXT) FROM "%s" WHERE id > ? ORDER BY id LIMIT 256`, column, column, table)
	update := fmt.Sprintf(`UPDATE "%s" SET "%s"=? WHERE id=?`, table, column)
	var after int64
	for {
		rows, err := tx.QueryContext(ctx, query, after)
		if err != nil {
			return err
		}
		type value struct {
			id        int64
			canonical string
		}
		values := make([]value, 0, 256)
		count := 0
		for rows.Next() {
			var id int64
			var kind string
			var raw sql.NullString
			if err := rows.Scan(&id, &kind, &raw); err != nil {
				rows.Close()
				return err
			}
			count++
			after = id
			if kind == "null" {
				continue
			}
			instant, parseErr := parseSQLiteLegacyTime(raw.String)
			if kind != "text" || !raw.Valid || parseErr != nil {
				rows.Close()
				return fmt.Errorf("unsupported legacy timestamp at %s id=%d column=%s; expected a text timestamp with an explicit timezone", table, id, column)
			}
			values = append(values, value{id, instant.Format(sqliteTargetTimeLayout)})
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, value := range values {
			if _, err := tx.ExecContext(ctx, update, value.canonical, value.id); err != nil {
				return err
			}
		}
		if count < 256 {
			return nil
		}
	}
}

func parseSQLiteLegacyTime(value string) (time.Time, error) {
	if before, monotonic, found := strings.Cut(value, " m="); found {
		if !sqliteMonotonicSuffix.MatchString(monotonic) {
			return time.Time{}, errors.New("invalid monotonic suffix")
		}
		value = before
	}
	if fraction := sqliteFraction.FindStringSubmatch(value); len(fraction) != 0 && len(fraction[1]) > 9 {
		return time.Time{}, errors.New("timestamp precision exceeds nanoseconds")
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
	} {
		if instant, err := time.Parse(layout, value); err == nil {
			return instant.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unrecognized timestamp")
}

func migrateSQLiteStaging(ctx context.Context, dir string) (report Report, err error) {
	return migrateSQLiteStagingWithOptions(ctx, dir, SQLiteOptions{Mode: config.ModeLocal})
}

func migrateSQLiteStagingWithOptions(ctx context.Context, dir string, options SQLiteOptions) (report Report, err error) {
	if timeutil.SQLiteLayout != sqliteTargetTimeLayout {
		return report, errors.New("SQLite target encoding changed; update and verify the v0.13.0 converter before migration")
	}
	if err := convertSQLiteTimes(ctx, filepath.Join(dir, localDatabaseName)); err != nil {
		return report, err
	}
	cfg, keys, err := prepareSQLiteStagingConfig(dir, options)
	if err != nil {
		return report, err
	}
	cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns = 1, 1
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	if _, err := db.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		return report, err
	}
	// Atlas may rebuild SQLite tables and toggle foreign keys. The unpublished
	// staging directory, rather than a PostgreSQL-style outer DDL transaction,
	// protects the original instance from partially completed schema changes.
	if err := client.Schema.Create(ctx); err != nil {
		return report, fmt.Errorf("prepare SQLite staging schema: %w", err)
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	report, err = Convert(ctx, tx.Client(), keys, options.Mode)
	if err != nil {
		if options.Mode == config.ModeLocal && strings.Contains(err.Error(), "existing active administrator named local") {
			return report, fmt.Errorf("%w; if this database was used by serve, migrate with sqlite --mode serve instead", err)
		}
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	if _, err := service.NewInitializationService(client).Validate(ctx, options.Mode); err != nil {
		return report, err
	}
	if err := service.NewCredentialService(client, keys, nil).ValidateKeys(ctx); err != nil {
		return report, err
	}
	if err := validateSQLiteData(ctx, db, dir); err != nil {
		return report, err
	}
	// Publish a self-contained database, never a database that still depends on
	// a staging WAL. Normal local startup will select WAL again.
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return report, err
	}
	if mode != "delete" {
		return report, errors.New("could not finalize the staging SQLite journal")
	}
	if cfg.Credentials.KeyringFile != "" {
		if options.Mode == config.ModeLocal {
			if _, err := credential.LoadKeyring(cfg.Credentials.KeyringFile); err != nil {
				return report, err
			}
		} else if err := options.verifyKeyring(cfg.Credentials.KeyringFile, !pathInside(options.DataDir, options.KeyringFile)); err != nil {
			return report, err
		}
	}
	return report, config.ValidateServerConfig(cfg)
}

func validateSQLiteData(ctx context.Context, db *sql.DB, dir string) error {
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("SQLite integrity check failed")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if invalid {
		return errors.New("SQLite foreign key check failed")
	}
	rows, err = db.QueryContext(ctx, "SELECT 'resources', id, storage_path FROM resources UNION ALL SELECT 'job_resources', id, output_path FROM job_resources WHERE output_path IS NOT NULL AND output_path <> ''")
	if err != nil {
		return err
	}
	defer rows.Close()
	root := filepath.Join(dir, "jobs")
	for rows.Next() {
		var table, path string
		var id int64
		if err := rows.Scan(&table, &id, &path); err != nil {
			return err
		}
		path = strings.ReplaceAll(path, `\`, "/")
		if !filepath.IsLocal(filepath.FromSlash(path)) || strings.Contains(path, ":") {
			return fmt.Errorf("invalid stored resource path in %s id=%d", table, id)
		}
		absolute := filepath.Join(root, filepath.FromSlash(path))
		if err := validatePath(absolute, false); err != nil {
			return fmt.Errorf("resource file is missing or unsafe in %s id=%d", table, id)
		}
		f, err := os.Open(absolute)
		if err != nil {
			return fmt.Errorf("resource file is unreadable in %s id=%d", table, id)
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return rows.Err()
}
