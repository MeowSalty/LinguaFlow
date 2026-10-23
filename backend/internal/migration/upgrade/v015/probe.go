package v015

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
)

type Inspection struct {
	Format           string   `json:"format"`
	Driver           string   `json:"driver"`
	Mode             string   `json:"mode,omitempty"`
	SchemaDigest     string   `json:"schema_digest"`
	Fingerprint      string   `json:"fingerprint"`
	Users            int64    `json:"users"`
	LegacyResources  int64    `json:"legacy_resources"`
	OldRefreshTokens int64    `json:"old_refresh_tokens"`
	NeedsAction      []string `json:"needs_action,omitempty"`
}

type SourceUser struct {
	ID                                  int
	Username, Email, PasswordHash, Role string
	Active                              bool
}
type actualColumn struct {
	Name, Type string
	Nullable   bool
	Primary    int
	Default    *string
	Identity   string
}
type actualIndex struct {
	Name    string
	Unique  bool
	Columns []string
	Where   string
}
type actualTable struct {
	Name, DDL   string
	Columns     []actualColumn
	Indexes     []actualIndex
	ForeignKeys []actualForeignKey
	Checks      []string
}

type actualForeignKey struct {
	Table, OnDelete, OnUpdate string
	Columns, RefColumns       []string
}

// COMPAT(migration-v015-auth1): recognize the exact released source before
// loading credentials or performing any schema writes.
// Tracking: backend/internal/migration/catalog/compatibility.md
func Probe(ctx context.Context, db *sql.DB, driver string) (*Inspection, error) {
	return probe(ctx, db, driver, nil, "")
}

// ProbeOperation accepts only the reservation and historical import explicitly
// bound by the operation. Ordinary inspect continues rejecting unfinished work.
func ProbeOperation(ctx context.Context, db *sql.DB, driver string, p *operation.Plan, manifestOperation string) (*Inspection, error) {
	return probe(ctx, db, driver, p, manifestOperation)
}

// ProbeRollback permits only this operation's abandoned reservation. Ordinary
// startup, inspect and resume continue to reject it before any writes.
func ProbeRollback(ctx context.Context, db *sql.DB, driver string, p *operation.Plan, manifestOperation string) (*Inspection, error) {
	return probe(context.WithValue(ctx, rollbackProbeKey{}, true), db, driver, p, manifestOperation)
}

type rollbackProbeKey struct{}

func probe(ctx context.Context, db *sql.DB, driver string, p *operation.Plan, manifestOperation string) (*Inspection, error) {
	if driver != "sqlite" && driver != "postgres" {
		return nil, operation.Fail(operation.UnsupportedSource, "unsupported database driver")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil && driver == "sqlite" {
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	tables, err := inspectTables(ctx, tx, driver)
	if err != nil {
		return nil, err
	}
	r := &Inspection{Driver: driver, Format: "empty", SchemaDigest: operation.Digest(tables)}
	if len(tables) == 0 {
		r.Fingerprint, err = fingerprint(ctx, tx, driver, tables)
		return r, err
	}
	if err := inspectMigrationState(ctx, tx, tables, p); err != nil {
		return nil, err
	}
	marked := hasTable(tables, "linguaflow_auth_format")
	want := source.Tables
	if marked {
		if err := singletonVersion(ctx, tx, "linguaflow_auth_format", 1); err != nil {
			return nil, err
		}
		want = migrate.Tables
		r.Format = "current"
	} else {
		r.Format = "v015"
	}
	if err := matchTables(tables, want, driver); err != nil {
		return nil, err
	}
	var id, version, dataVersion int
	var local sql.NullInt64
	rows, err := tx.QueryContext(ctx, "SELECT id,version,data_version,mode,local_user_id FROM instance_initializations")
	if err != nil {
		return nil, operation.Fail(operation.UnsupportedSource, "initialization contract is unavailable")
	}
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id, &version, &dataVersion, &r.Mode, &local); err != nil {
			rows.Close()
			return nil, err
		}
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if count != 1 || id != 1 || version != 1 || (r.Mode != "serve" && r.Mode != "local") || (r.Mode == "local" && (!local.Valid || local.Int64 <= 0)) || (r.Mode == "serve" && local.Valid) {
		return nil, operation.Fail(operation.UnsupportedSource, "source initialization identity is incomplete")
	}
	wantData := 2
	if marked {
		wantData = 3
	}
	if dataVersion != wantData {
		return nil, operation.Fail(operation.UnsupportedSource, "data version is outside the finite route")
	}
	if driver == "sqlite" {
		var v int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
			return nil, err
		}
		if v != 1 {
			return nil, operation.Fail(operation.UnsupportedSource, "timestamp format is outside the released route")
		}
	}
	for query, dest := range map[string]*int64{"SELECT COUNT(*) FROM users": &r.Users, "SELECT COUNT(*) FROM resources WHERE current_source_revision_id IS NULL": &r.LegacyResources, "SELECT COUNT(*) FROM refresh_tokens": &r.OldRefreshTokens} {
		if err := tx.QueryRowContext(ctx, query).Scan(dest); err != nil {
			return nil, err
		}
	}
	if r.Users == 0 {
		return nil, operation.Fail(operation.UnsupportedSource, "initialized source has no original identity")
	}
	if local.Valid {
		var name, role string
		var active bool
		if err := tx.QueryRowContext(ctx, "SELECT username,role,active FROM users WHERE id="+fmt.Sprint(local.Int64)).Scan(&name, &role, &active); err != nil || name != "local" || role != "admin" || !active {
			return nil, operation.Fail(operation.UnsupportedSource, "source local identity is invalid")
		}
	}
	if marked {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM local_account_policies WHERE id=1").Scan(&n); err != nil || n != 1 {
			return nil, operation.Fail(operation.UnsupportedSource, "current source authentication policy is incomplete")
		}
	}
	var unfinished int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM storage_tasks WHERE kind IN ('migration','project_migration','legacy_import','legacy_migration') AND status NOT IN ('completed','cancelled','failed') AND operation_id<>"+placeholder(driver), manifestOperation).Scan(&unfinished); err != nil {
		return nil, err
	}
	if unfinished > 0 {
		return nil, operation.Fail(operation.NeedsAction, "finish or roll back existing storage operations with their matching tool")
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM jobs WHERE status IN ('running','processing','paused','pending')").Scan(&unfinished); err != nil {
		return nil, err
	}
	if unfinished > 0 {
		r.NeedsAction = append(r.NeedsAction, "historical_jobs_require_snapshot_and_credentials_review")
	}
	if err := checkS3Prefixes(ctx, tx); err != nil {
		var classified *operation.Error
		if !errors.As(err, &classified) {
			return nil, err
		}
		r.NeedsAction = append(r.NeedsAction, "s3_path_conflict")
	}
	r.Fingerprint, err = fingerprint(ctx, tx, driver, tables)
	return r, err
}

func ReadUsers(ctx context.Context, db *sql.DB) ([]SourceUser, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,username,COALESCE(email,''),COALESCE(password_hash,''),role,active FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SourceUser
	for rows.Next() {
		var u SourceUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.Active); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

func Fingerprint(ctx context.Context, db *sql.DB, driver string) (string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil && driver == "sqlite" {
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	tables, err := inspectTables(ctx, tx, driver)
	if err != nil {
		return "", err
	}
	return fingerprint(ctx, tx, driver, tables)
}

func fingerprint(ctx context.Context, tx *sql.Tx, driver string, tables []actualTable) (string, error) {
	h := sha256.New()
	writeHash(h, tables)
	if driver == "sqlite" {
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return "", err
		}
		writeHash(h, version)
		var sequenceTable int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='sqlite_sequence'").Scan(&sequenceTable); err != nil {
			return "", err
		}
		if sequenceTable != 0 {
			rows, err := tx.QueryContext(ctx, "SELECT name,seq FROM sqlite_sequence ORDER BY name")
			if err != nil {
				return "", err
			}
			for rows.Next() {
				var name string
				var value int64
				if err := rows.Scan(&name, &value); err != nil {
					rows.Close()
					return "", err
				}
				if !hasTable(tables, name) || value < 0 {
					rows.Close()
					return "", operation.Fail(operation.UnsupportedSource, "unknown SQLite allocation sequence")
				}
				writeHash(h, struct {
					Name  string
					Value int64
				}{name, value})
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return "", err
			}
		}
	} else if driver == "postgres" {
		if err := fingerprintPostgresSequences(ctx, tx, h, tables); err != nil {
			return "", err
		}
	}
	for _, t := range tables {
		columns, order := []string{}, []string{}
		for _, c := range t.Columns {
			columns = append(columns, quoteIdentifier(c.Name))
			if c.Primary > 0 {
				order = append(order, quoteIdentifier(c.Name))
			}
		}
		if len(order) == 0 {
			order = columns
		}
		rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(columns, ",")+" FROM "+quoteIdentifier(t.Name)+" ORDER BY "+strings.Join(order, ","))
		if err != nil {
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return "", err
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = struct{ Bytes string }{hex.EncodeToString(b)}
				}
				if t, ok := v.(time.Time); ok {
					values[i] = t.UTC().Format(time.RFC3339Nano)
				}
			}
			writeHash(h, values)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeHash(h hash.Hash, v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(h, "%d:", len(data))
	h.Write(data)
}
func hasTable(tables []actualTable, name string) bool {
	for _, t := range tables {
		if t.Name == name {
			return true
		}
	}
	return false
}
func singletonVersion(ctx context.Context, tx *sql.Tx, table string, version int) error {
	rows, err := tx.QueryContext(ctx, "SELECT id,version FROM "+quoteIdentifier(table))
	if err != nil {
		return operation.Fail(operation.UnsupportedSource, "invalid format marker")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, v int
		if err := rows.Scan(&id, &v); err != nil || id != 1 || v != version {
			return operation.Fail(operation.UnsupportedSource, "unsupported format marker")
		}
		count++
	}
	if count != 1 {
		return operation.Fail(operation.UnsupportedSource, "incomplete format marker")
	}
	return rows.Err()
}

func inspectMigrationState(ctx context.Context, tx *sql.Tx, tables []actualTable, p *operation.Plan) error {
	if !hasTable(tables, "linguaflow_migration_state") {
		if hasTable(tables, "linguaflow_migration_steps") {
			return operation.Fail(operation.NeedsAction, "migration receipts lack their reservation")
		}
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,version,operation_id,plan_digest,target_signature,state FROM linguaflow_migration_state")
	if err != nil {
		return operation.Fail(operation.NeedsAction, "invalid migration reservation")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, v int
		var op, digest, signature, state string
		if err := rows.Scan(&id, &v, &op, &digest, &signature, &state); err != nil || id != 1 || v != 1 || !operation.ValidHex(op, 32) || !operation.ValidHex(digest, 64) || !operation.ValidHex(signature, 64) || (state != "ready" && !(p != nil && (state == "preparing" || state == "abandoned" && ctx.Value(rollbackProbeKey{}) == true) && op == p.ID && digest == operation.Digest(p) && signature == p.TargetSignature)) {
			return operation.Fail(operation.NeedsAction, "source contains an unfinished or unknown migration; use its exact operation")
		}
		count++
	}
	if count != 1 {
		return operation.Fail(operation.NeedsAction, "invalid migration reservation")
	}
	return rows.Err()
}

func placeholder(driver string) string {
	if driver == "postgres" {
		return "$1"
	}
	return "?"
}

func matchTables(actual []actualTable, expected []*schema.Table, driver string) error {
	byName := map[string]actualTable{}
	for _, t := range actual {
		if t.Name == "linguaflow_auth_format" || t.Name == "linguaflow_migration_state" || t.Name == "linguaflow_migration_steps" {
			continue
		}
		byName[t.Name] = t
	}
	if len(byName) != len(expected) {
		return operation.Fail(operation.UnsupportedSource, "unknown or mixed schema; preserve the source and identify its exact release")
	}
	for _, want := range expected {
		t, ok := byName[want.Name]
		if !ok || len(t.Columns) != len(want.Columns) {
			return operation.Fail(operation.UnsupportedSource, "source table or column set differs from the release")
		}
		columns := map[string]actualColumn{}
		for _, c := range t.Columns {
			columns[c.Name] = c
		}
		for _, c := range want.Columns {
			got, ok := columns[c.Name]
			if !ok || got.Nullable != c.Nullable || !compatibleType(got.Type, c, driver) {
				return operation.Fail(operation.UnsupportedSource, "source column contract differs from the release")
			}
			primary := 0
			for i, pk := range want.PrimaryKey {
				if pk.Name == c.Name {
					primary = i + 1
				}
			}
			if got.Primary != primary {
				return operation.Fail(operation.UnsupportedSource, "source primary key contract differs from release")
			}
			if (c.Default != nil && !defaultMatches(got.Default, c)) || (c.Default == nil && !c.Increment && got.Default != nil) {
				return operation.Fail(operation.UnsupportedSource, "source default differs from release")
			}
			if driver == "postgres" && c.Increment && got.Identity != "YES" && (got.Default == nil || !strings.HasPrefix(*got.Default, "nextval(")) {
				return operation.Fail(operation.UnsupportedSource, "source primary key allocation differs from release")
			}
		}
		if len(t.ForeignKeys) != len(want.ForeignKeys) {
			return operation.Fail(operation.UnsupportedSource, "source foreign key set differs from release")
		}
		for _, fk := range want.ForeignKeys {
			cols, refs := []string{}, []string{}
			for _, c := range fk.Columns {
				cols = append(cols, c.Name)
			}
			for _, c := range fk.RefColumns {
				refs = append(refs, c.Name)
			}
			found := false
			for _, got := range t.ForeignKeys {
				if fk.RefTable != nil && got.Table == fk.RefTable.Name && strings.Join(got.Columns, "\x00") == strings.Join(cols, "\x00") && strings.Join(got.RefColumns, "\x00") == strings.Join(refs, "\x00") && strings.EqualFold(got.OnDelete, string(fk.OnDelete)) && got.OnUpdate == "NO ACTION" {
					found = true
				}
			}
			if !found {
				return operation.Fail(operation.UnsupportedSource, "source ownership constraint differs from release")
			}
		}
		for _, index := range t.Indexes {
			if !index.Unique {
				continue
			}
			declared := false
			primary := []string{}
			for _, c := range want.PrimaryKey {
				primary = append(primary, c.Name)
			}
			if strings.Join(index.Columns, "\x00") == strings.Join(primary, "\x00") && index.Where == "" {
				declared = true
			}
			for _, c := range want.Columns {
				if c.Unique && len(index.Columns) == 1 && index.Columns[0] == c.Name && index.Where == "" {
					declared = true
				}
			}
			for _, i := range want.Indexes {
				if !i.Unique {
					continue
				}
				names := []string{}
				for _, c := range i.Columns {
					names = append(names, c.Name)
				}
				where := ""
				if i.Annotation != nil {
					where = i.Annotation.Where
				}
				if strings.Join(index.Columns, "\x00") == strings.Join(names, "\x00") && normalSQL(index.Where) == normalSQL(where) {
					declared = true
				}
			}
			if !declared {
				return operation.Fail(operation.UnsupportedSource, "unknown uniqueness constraint in source schema")
			}
		}
		for _, c := range want.Columns {
			if c.Unique && !uniqueIndex(t, []string{c.Name}, "") {
				return operation.Fail(operation.UnsupportedSource, "source uniqueness contract is incomplete")
			}
		}
		for _, i := range want.Indexes {
			names := []string{}
			for _, c := range i.Columns {
				names = append(names, c.Name)
			}
			where := ""
			if i.Annotation != nil {
				where = i.Annotation.Where
			}
			if i.Unique && !uniqueIndex(t, names, where) {
				return operation.Fail(operation.UnsupportedSource, "source index contract is incomplete")
			}
		}
		checks := map[string]int{}
		if want.Annotation != nil {
			for _, expression := range want.Annotation.Checks {
				key, err := canonicalCheck(expression, driver)
				if err != nil {
					return operation.Fail(operation.UnsupportedSource, "declared check is outside the fixed grammar")
				}
				checks[key]++
			}
			if want.Annotation.Check != "" {
				key, err := canonicalCheck(want.Annotation.Check, driver)
				if err != nil {
					return operation.Fail(operation.UnsupportedSource, "declared check is outside the fixed grammar")
				}
				checks[key]++
			}
		}
		for _, expression := range t.Checks {
			key, err := canonicalCheck(expression, driver)
			if err != nil || checks[key] == 0 {
				return operation.Fail(operation.UnsupportedSource, "unknown or changed source check constraint")
			}
			checks[key]--
		}
		for _, remaining := range checks {
			if remaining != 0 {
				return operation.Fail(operation.UnsupportedSource, "source check contract is incomplete")
			}
		}
	}
	return nil
}

func uniqueIndex(t actualTable, names []string, where string) bool {
	for _, i := range t.Indexes {
		if i.Unique && strings.Join(i.Columns, "\x00") == strings.Join(names, "\x00") && normalSQL(i.Where) == normalSQL(where) {
			return true
		}
	}
	return false
}
func normalSQL(s string) string {
	r := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "", "\"", "", "`", "", "(", "", ")", "")
	return strings.ToLower(r.Replace(s))
}

func defaultMatches(actual *string, c *schema.Column) bool {
	if actual == nil {
		return false
	}
	v := strings.TrimSpace(*actual)
	// PostgreSQL exposes SQL casts in catalog defaults.
	if index := strings.Index(v, "::"); index >= 0 {
		v = v[:index]
	}
	v = strings.Trim(v, "()")
	switch want := c.Default.(type) {
	case string:
		return v == "'"+strings.ReplaceAll(want, "'", "''")+"'"
	case bool:
		if want {
			return v == "true" || v == "1"
		}
		return v == "false" || v == "0"
	default:
		return v == fmt.Sprint(want)
	}
}
func compatibleType(actual string, c *schema.Column, driver string) bool {
	a := strings.ToLower(actual)
	switch c.Type {
	case field.TypeBool:
		return a == "bool" || a == "boolean" || a == "integer"
	case field.TypeTime:
		return a == "datetime" || a == "timestamp with time zone" || a == "timestamp without time zone" || a == "timestamptz"
	case field.TypeJSON:
		return a == "json" || a == "jsonb"
	case field.TypeBytes:
		return a == "blob" || a == "bytea"
	case field.TypeString, field.TypeEnum:
		return a == "text" || strings.HasPrefix(a, "varchar") || a == "character varying"
	case field.TypeFloat64, field.TypeFloat32:
		return a == "real" || a == "double precision" || a == "float"
	default:
		return strings.Contains(a, "int") || a == "numeric"
	}
}

func checkS3Prefixes(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT sp.bucket,sp.prefix FROM storage_spaces sp JOIN storage_connections c ON c.id=sp.connection_id WHERE c.driver='s3' ORDER BY sp.bucket,sp.id")
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string][]string{}
	for rows.Next() {
		var bucket, prefix string
		if err := rows.Scan(&bucket, &prefix); err != nil {
			return err
		}
		p := strings.TrimSuffix(prefix, "/")
		for _, other := range seen[bucket] {
			if p == other || p == "" || other == "" || strings.HasPrefix(p, other+"/") || strings.HasPrefix(other, p+"/") {
				return operation.Fail(operation.NeedsAction, "S3 path claims overlap")
			}
		}
		seen[bucket] = append(seen[bucket], p)
	}
	return rows.Err()
}

func inspectTables(ctx context.Context, tx *sql.Tx, driver string) ([]actualTable, error) {
	if driver == "postgres" {
		return inspectPostgresTables(ctx, tx)
	}
	var unknown int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type IN ('view','trigger') AND name NOT GLOB 'sqlite_*'").Scan(&unknown); err != nil {
		return nil, err
	}
	if unknown > 0 {
		return nil, operation.Fail(operation.UnsupportedSource, "source has unknown views or triggers")
	}
	rows, err := tx.QueryContext(ctx, "SELECT name,sql FROM sqlite_schema WHERE type='table' AND name NOT GLOB 'sqlite_*' ORDER BY name")
	if err != nil {
		return nil, err
	}
	var tables []actualTable
	for rows.Next() {
		var t actualTable
		if err := rows.Scan(&t.Name, &t.DDL); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range tables {
		t := &tables[i]
		t.Checks, err = sqliteChecks(t.DDL)
		if err != nil {
			return nil, operation.Fail(operation.UnsupportedSource, "source check DDL is outside the finite route")
		}
		rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+quoteIdentifier(t.Name)+")")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c actualColumn
			var cid, notNull int
			if err := rows.Scan(&cid, &c.Name, &c.Type, &notNull, &c.Default, &c.Primary); err != nil {
				rows.Close()
				return nil, err
			}
			c.Nullable = notNull == 0 && c.Primary == 0
			t.Columns = append(t.Columns, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, "PRAGMA foreign_key_list("+quoteIdentifier(t.Name)+")")
		if err != nil {
			return nil, err
		}
		foreign := map[int]*actualForeignKey{}
		for rows.Next() {
			var id, seq int
			var name, col, ref, update, del, match string
			if err := rows.Scan(&id, &seq, &name, &col, &ref, &update, &del, &match); err != nil {
				rows.Close()
				return nil, err
			}
			if foreign[id] == nil {
				foreign[id] = &actualForeignKey{Table: name, OnDelete: del, OnUpdate: update}
			}
			fk := foreign[id]
			fk.Columns = append(fk.Columns, col)
			fk.RefColumns = append(fk.RefColumns, ref)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		for _, fk := range foreign {
			t.ForeignKeys = append(t.ForeignKeys, *fk)
		}
		sort.Slice(t.ForeignKeys, func(i, j int) bool { return operation.Digest(t.ForeignKeys[i]) < operation.Digest(t.ForeignKeys[j]) })
		rows, err = tx.QueryContext(ctx, "PRAGMA index_list("+quoteIdentifier(t.Name)+")")
		if err != nil {
			return nil, err
		}
		var partial []bool
		for rows.Next() {
			var seq, unique, p int
			var origin string
			var idx actualIndex
			if err := rows.Scan(&seq, &idx.Name, &unique, &origin, &p); err != nil {
				rows.Close()
				return nil, err
			}
			idx.Unique = unique != 0
			t.Indexes = append(t.Indexes, idx)
			partial = append(partial, p != 0)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		for j := range t.Indexes {
			idx := &t.Indexes[j]
			rows, err = tx.QueryContext(ctx, "PRAGMA index_info("+quoteIdentifier(idx.Name)+")")
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var seq, cid int
				var name sql.NullString
				if err := rows.Scan(&seq, &cid, &name); err != nil {
					rows.Close()
					return nil, err
				}
				idx.Columns = append(idx.Columns, name.String)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
			if partial[j] {
				var ddl string
				if err := tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE name=?", idx.Name).Scan(&ddl); err != nil {
					return nil, err
				}
				_, idx.Where, _ = strings.Cut(strings.ToLower(ddl), " where ")
			}
		}
		sort.Slice(t.Indexes, func(i, j int) bool { return t.Indexes[i].Name < t.Indexes[j].Name })
	}
	return tables, nil
}

func inspectPostgresTables(ctx context.Context, tx *sql.Tx) ([]actualTable, error) {
	if err := snapshot.CheckPostgresCatalog(ctx, tx); err != nil {
		if errors.Is(err, snapshot.ErrPostgresCatalogNotEmpty) {
			return nil, operation.Fail(operation.UnsupportedSource, "unknown PostgreSQL catalog objects")
		}
		return nil, err
	}
	var unknown int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND (n.nspname<>'public' OR c.relkind IN ('v','m','f'))) + (SELECT COUNT(*) FROM pg_trigger tr JOIN pg_class c ON c.oid=tr.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND NOT tr.tgisinternal)`).Scan(&unknown); err != nil {
		return nil, err
	}
	if unknown != 0 {
		return nil, operation.Fail(operation.UnsupportedSource, "unknown PostgreSQL schemas, views or triggers")
	}
	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=current_schema() AND table_type='BASE TABLE' ORDER BY table_name")
	if err != nil {
		return nil, err
	}
	var tables []actualTable
	for rows.Next() {
		var t actualTable
		if err := rows.Scan(&t.Name); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range tables {
		t := &tables[i]
		rows, err := tx.QueryContext(ctx, `SELECT c.column_name,c.data_type,c.is_nullable,c.column_default,COALESCE((SELECT k.ordinal_position FROM information_schema.table_constraints tc JOIN information_schema.key_column_usage k ON k.constraint_name=tc.constraint_name AND k.constraint_schema=tc.constraint_schema WHERE tc.table_schema=c.table_schema AND tc.table_name=c.table_name AND tc.constraint_type='PRIMARY KEY' AND k.column_name=c.column_name),0),c.is_identity FROM information_schema.columns c WHERE c.table_schema=current_schema() AND c.table_name=$1 ORDER BY c.ordinal_position`, t.Name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c actualColumn
			var nullable string
			if err := rows.Scan(&c.Name, &c.Type, &nullable, &c.Default, &c.Primary, &c.Identity); err != nil {
				rows.Close()
				return nil, err
			}
			c.Nullable = nullable == "YES"
			t.Columns = append(t.Columns, c)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, `SELECT c.conname,r.relname,a.attname,ra.attname,c.confdeltype::text,c.confupdtype::text FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_class r ON r.oid=c.confrelid CROSS JOIN LATERAL unnest(c.conkey,c.confkey) WITH ORDINALITY AS k(col,refcol,ordinal) JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=k.col JOIN pg_attribute ra ON ra.attrelid=r.oid AND ra.attnum=k.refcol WHERE n.nspname=current_schema() AND t.relname=$1 AND c.contype='f' ORDER BY c.conname,k.ordinal`, t.Name)
		if err != nil {
			return nil, err
		}
		foreign := map[string]*actualForeignKey{}
		for rows.Next() {
			var name, table, col, ref, del, update string
			if err := rows.Scan(&name, &table, &col, &ref, &del, &update); err != nil {
				rows.Close()
				return nil, err
			}
			if foreign[name] == nil {
				foreign[name] = &actualForeignKey{Table: table, OnDelete: postgresForeignAction(del), OnUpdate: postgresForeignAction(update)}
			}
			fk := foreign[name]
			fk.Columns = append(fk.Columns, col)
			fk.RefColumns = append(fk.RefColumns, ref)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		for _, fk := range foreign {
			t.ForeignKeys = append(t.ForeignKeys, *fk)
		}
		sort.Slice(t.ForeignKeys, func(i, j int) bool { return operation.Digest(t.ForeignKeys[i]) < operation.Digest(t.ForeignKeys[j]) })
		rows, err = tx.QueryContext(ctx, `SELECT i.relname,ix.indisunique,pg_get_indexdef(ix.indexrelid),COALESCE(pg_get_expr(ix.indpred,ix.indrelid),'') FROM pg_index ix JOIN pg_class t ON t.oid=ix.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace JOIN pg_class i ON i.oid=ix.indexrelid WHERE n.nspname=current_schema() AND t.relname=$1 ORDER BY i.relname`, t.Name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var idx actualIndex
			var ddl string
			if err := rows.Scan(&idx.Name, &idx.Unique, &ddl, &idx.Where); err != nil {
				rows.Close()
				return nil, err
			}
			start := strings.Index(ddl, "(")
			end := strings.Index(ddl, ")")
			if start >= 0 && end > start {
				for _, column := range strings.Split(ddl[start+1:end], ",") {
					idx.Columns = append(idx.Columns, strings.Trim(strings.TrimSpace(column), `"`))
				}
			}
			t.Indexes = append(t.Indexes, idx)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		rows, err = tx.QueryContext(ctx, `SELECT pg_get_expr(c.conbin,c.conrelid) FROM pg_constraint c JOIN pg_class t ON t.oid=c.conrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname=current_schema() AND t.relname=$1 AND c.contype='c' ORDER BY c.conname`, t.Name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ddl string
			if err := rows.Scan(&ddl); err != nil {
				rows.Close()
				return nil, err
			}
			t.DDL += ddl + "\n"
			t.Checks = append(t.Checks, ddl)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return tables, nil
}

func postgresForeignAction(code string) string {
	switch code {
	case "a":
		return "NO ACTION"
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	}
	return "unknown"
}
