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
	"strconv"
	"strings"
	"time"

	entschema "entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	source "github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015/source"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

type baselineQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// baselineScan closes the cursor before the next statement on the same transaction.
func baselineScan(ctx context.Context, q baselineQueryer, query string, args []any, dest ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	if rows.Next() {
		return errors.New("baseline single-row query returned multiple rows")
	}
	return errors.Join(rows.Err(), rows.Close())
}

type baselineTable struct {
	name    string
	columns []string
	primary []string
	maxID   int64
	numeric bool
}

func schemaForBaseline(p *operation.Plan) []*entschema.Table {
	if p.Kind == "storage" {
		return migrate.Tables
	}
	return source.Tables
}

func baselineTables(p *operation.Plan) []baselineTable {
	schema := schemaForBaseline(p)
	tables := make([]baselineTable, 0, len(schema))
	for _, table := range schema {
		if table.Name == "linguaflow_migration_state" || table.Name == "linguaflow_migration_steps" {
			continue
		}
		item := baselineTable{name: table.Name}
		for _, column := range table.Columns {
			item.columns = append(item.columns, column.Name)
		}
		for _, column := range table.PrimaryKey {
			item.primary = append(item.primary, column.Name)
		}
		if len(table.PrimaryKey) == 1 && table.PrimaryKey[0].Type.Integer() {
			item.numeric = true
		}
		tables = append(tables, item)
	}
	return tables
}

func NormalizeConversionTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func CaptureBaseline(ctx context.Context, db *sql.DB, p *operation.Plan) (*operation.Baseline, error) {
	if p == nil || (p.Source.Driver != "sqlite" && p.Source.Driver != "postgres") {
		return nil, operation.Fail(operation.UnsupportedSource, "baseline requires a supported database and immutable plan")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil && p.Source.Driver == "sqlite" {
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	baseline := &operation.Baseline{
		Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p),
		SourceFingerprint: p.SourceFingerprint, SourceSchema: p.SourceSchema,
		SourceIdentity: p.Source.Identity, InputDigest: p.Source.InputDigest,
		ConversionTime: NormalizeConversionTime(time.Now()),
		Facts:          map[string]string{"driver": p.Source.Driver, "kind": p.Kind},
	}
	if err := captureBaselineAdditions(ctx, tx, p, baseline); err != nil {
		return nil, err
	}
	for _, table := range baselineTables(p) {
		evidence, changes, err := captureTableBaseline(ctx, tx, p, table, baseline.ConversionTime)
		if err != nil {
			return nil, fmt.Errorf("capture %s baseline: %w", table.name, err)
		}
		baseline.Tables = append(baseline.Tables, evidence)
		baseline.Changes = append(baseline.Changes, changes...)
	}
	sort.Slice(baseline.Changes, func(i, j int) bool {
		if baseline.Changes[i].Table != baseline.Changes[j].Table {
			return baseline.Changes[i].Table < baseline.Changes[j].Table
		}
		return baseline.Changes[i].ID < baseline.Changes[j].ID
	})
	p.Baseline = baseline
	if err := baseline.Bind(p); err != nil {
		p.Baseline = nil
		return nil, err
	}
	return baseline, nil
}

func captureTableBaseline(ctx context.Context, q baselineQueryer, p *operation.Plan, table baselineTable, conversionTime time.Time) (operation.TableBaseline, []operation.RowChange, error) {
	evidence := operation.TableBaseline{Name: table.name, Columns: append([]string(nil), table.columns...), PrimaryKey: append([]string(nil), table.primary...), NumericID: table.numeric}
	if len(table.primary) == 0 {
		return evidence, nil, operation.Fail(operation.UnsupportedSource, "baseline requires a stable primary key")
	}
	keysHash, preservedHash := sha256.New(), sha256.New()
	writeBaselineHash(keysHash, table.primary)
	writeBaselineHash(preservedHash, table.columns)
	var changes []operation.RowChange
	var cursor []any
	first := true
	for {
		query := "SELECT " + quotedColumns(table.columns) + " FROM " + quoteIdentifier(table.name)
		args := []any{}
		if !first {
			query += " WHERE (" + quotedColumns(table.primary) + ") > (" + placeholders(p.Source.Driver, len(table.primary)) + ")"
			args = append(args, cursor...)
		}
		query += " ORDER BY " + quotedColumns(table.primary) + " LIMIT 500"
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return evidence, nil, err
		}
		type capturedRow struct {
			values []any
			row    map[string]any
			id     int
			key    []any
		}
		batch := make([]capturedRow, 0, 500)
		for rows.Next() {
			values := make([]any, len(table.columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return evidence, nil, err
			}
			row := make(map[string]any, len(values))
			for i, name := range table.columns {
				row[name] = values[i]
			}
			key := make([]any, len(table.primary))
			for i, name := range table.primary {
				if row[name] == nil {
					rows.Close()
					return evidence, nil, operation.Fail(operation.UnsupportedSource, "baseline encountered a null primary key")
				}
				key[i] = row[name]
			}
			id := 0
			if table.numeric {
				keyID := int64Value(row[table.primary[0]])
				id = int(keyID)
				if int64(id) != keyID {
					rows.Close()
					return evidence, nil, operation.Fail(operation.UnsupportedSource, "numeric primary key is outside the receipt range")
				}
			}
			batch = append(batch, capturedRow{values: values, row: row, id: id, key: key})
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return evidence, nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, captured := range batch {
			id := captured.id
			row := captured.row
			if table.numeric && int64(id) > evidence.MaxID {
				evidence.MaxID = int64(id)
			}
			key := make([]string, 0, len(table.primary))
			for _, name := range table.primary {
				key = append(key, baselineValue(row[name]))
			}
			writeBaselineHash(keysHash, key)
			mutable := map[string]any{}
			var err error
			if table.numeric {
				mutable, err = expectedMutableValues(ctx, q, p, table.name, id, row, conversionTime)
			}
			if err != nil {
				return evidence, nil, err
			}
			change := operation.RowChange{Table: table.name, ID: id, Before: map[string]string{}, Expected: map[string]string{}}
			preserved := make([]string, 0, len(captured.values))
			for i, name := range table.columns {
				if expected, ok := mutable[name]; ok {
					change.Before[name] = baselineColumnDigest(table.name, name, captured.values[i])
					change.Expected[name] = baselineColumnDigest(table.name, name, expected)
					continue
				}
				preserved = append(preserved, baselineColumnValue(table.name, name, captured.values[i]))
			}
			writeBaselineHash(preservedHash, preserved)
			if table.numeric && len(change.Expected) != 0 {
				changes = append(changes, change)
			}
			evidence.Count++
			cursor = captured.key
		}
		first = false
		if len(batch) < 500 {
			break
		}
	}
	evidence.Keys = hex.EncodeToString(keysHash.Sum(nil))
	evidence.Preserved = hex.EncodeToString(preservedHash.Sum(nil))
	return evidence, changes, nil
}

func expectedMutableValues(ctx context.Context, q baselineQueryer, p *operation.Plan, table string, id int, row map[string]any, conversionTime time.Time) (map[string]any, error) {
	values := map[string]any{}
	if table == "storage_path_claims" {
		var generation int64
		err := baselineScan(ctx, q, "SELECT sp.execution_generation FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE sp.id="+placeholders(p.Source.Driver, 1)+" AND co.driver='s3' AND sp.lifecycle<>'removed' AND sp.bucket<>''", []any{row["space_id"]}, &generation)
		if err == nil {
			values["execution_generation"] = generation
			values["updated_at"] = sqliteConversionTime(p.Source.Driver, conversionTime)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if p.StorageManifest != "" {
		storageValues, err := expectedStorageValues(ctx, q, p, table, id, row, conversionTime)
		if err != nil {
			return nil, err
		}
		for name, value := range storageValues {
			values[name] = value
		}
	}
	if p.Kind != "upgrade" {
		return values, nil
	}
	switch table {
	case "users":
		for _, account := range p.Accounts {
			if account.UserID != id {
				continue
			}
			values["username"] = account.Username
			values["email"] = nil
			if account.Email != nil {
				values["email"] = *account.Email
			}
			break
		}
	case "instance_initializations":
		if p.Kind == "upgrade" {
			values["data_version"] = row["data_version"]
			values["updated_at"] = sqliteConversionTime(p.Source.Driver, conversionTime)
		}
	case "activity_logs":
		if stringValue(row["visibility_scope"]) == "legacy" {
			return expectedActivityVisibility(ctx, q, p.Source.Driver, row, conversionTime)
		}
	case "usage_records":
		if stringValue(row["visibility_scope"]) == "legacy" {
			return expectedUsageVisibility(ctx, q, p.Source.Driver, row, conversionTime)
		}
	}
	return values, nil
}

func expectedActivityVisibility(ctx context.Context, q baselineQueryer, driver string, row map[string]any, conversionTime time.Time) (map[string]any, error) {
	values := map[string]any{"visibility_scope": "unknown"}
	if !knownBaselineActivity(stringValue(row["action"])) {
		return values, nil
	}
	projectID := int64Value(row["project_activity_logs"])
	if projectID == 0 {
		var err error
		projectID, err = recoverBaselineActivityProject(ctx, q, driver, row)
		if err != nil {
			return nil, err
		}
	}
	if projectID > 0 {
		valid, orgID, err := baselineProjectVisibility(ctx, q, driver, projectID)
		if err != nil {
			return nil, err
		}
		if valid {
			values["visibility_scope"] = "project"
			values["project_activity_logs"] = projectID
			values["organization_activity_logs"] = orgID
			return values, nil
		}
	}
	if stringValue(row["action"]) == "quick_translate" && stringValue(row["resource_type"]) == "quick_translate" &&
		int64Value(row["project_activity_logs"]) == 0 && int64Value(row["organization_activity_logs"]) == 0 &&
		int64Value(row["user_activity_logs"]) > 0 && !baselineMetadataHasProject(row["metadata"]) {
		values["visibility_scope"] = "personal"
	}
	return values, nil
}

func expectedUsageVisibility(ctx context.Context, q baselineQueryer, driver string, row map[string]any, conversionTime time.Time) (map[string]any, error) {
	values := map[string]any{"visibility_scope": "unknown"}
	source := stringValue(row["source"])
	projectID := int64Value(row["project_usage_records"])
	if projectID == 0 || (source != "job" && source != "preview" && source != "quick_translate") {
		return values, nil
	}
	valid, orgID, err := baselineProjectVisibility(ctx, q, driver, projectID)
	if err != nil {
		return nil, err
	}
	if valid {
		values["visibility_scope"] = "project"
		values["organization_usage_records"] = orgID
	}
	return values, nil
}

func knownBaselineActivity(action string) bool {
	switch action {
	case "quick_translate", "job.create", "job.cancel", "job.retry", "job.pause", "job.resume",
		"resource.segment.update", "resource.segment.translation_preview.apply", "resource.segment.revision_preview.apply",
		"segment.approve", "segment.reject", "segment.issue_disposition", "segment.batch_review",
		"segment.approve_all", "segment.retranslate_rejected", "segment.search_replace", "segment.search_replace_undo",
		"qa.recheck", "glossary.sync_execute":
		return true
	default:
		return false
	}
}

func recoverBaselineActivityProject(ctx context.Context, q baselineQueryer, driver string, row map[string]any) (int64, error) {
	action, resourceType := stringValue(row["action"]), stringValue(row["resource_type"])
	resourceID := int64Value(row["resource_id"])
	switch action {
	case "quick_translate":
		if resourceType == "quick_translate" {
			return baselineMetadataProjectID(row["metadata"]), nil
		}
	case "job.create", "job.cancel", "job.retry", "job.pause", "job.resume":
		if resourceType == "job" && resourceID > 0 {
			var projectID sql.NullInt64
			err := baselineScan(ctx, q, "SELECT project_id FROM jobs WHERE id="+placeholders(driver, 1), []any{resourceID}, &projectID)
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil
			}
			return projectID.Int64, err
		}
	case "resource.segment.update", "segment.approve", "segment.reject", "segment.issue_disposition":
		if resourceType == "segment" && resourceID > 0 {
			return baselineRelatedProject(ctx, q, driver, `SELECT r.project_id FROM segments s JOIN resources r ON r.id=s.resource_id WHERE s.id=?`, resourceID)
		}
	case "segment.batch_review", "segment.approve_all", "segment.retranslate_rejected", "segment.search_replace", "segment.search_replace_undo":
		if resourceType == "resource" && resourceID > 0 {
			return baselineRelatedProject(ctx, q, driver, "SELECT project_id FROM resources WHERE id=?", resourceID)
		}
	case "qa.recheck":
		if resourceType == "project" && resourceID > 0 {
			return resourceID, nil
		}
	}
	return 0, nil
}

func baselineRelatedProject(ctx context.Context, q baselineQueryer, driver, query string, id int64) (int64, error) {
	var projectID sql.NullInt64
	err := baselineScan(ctx, q, strings.Replace(query, "?", placeholders(driver, 1), 1), []any{id}, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return projectID.Int64, err
}

func baselineProjectVisibility(ctx context.Context, q baselineQueryer, driver string, id int64) (bool, any, error) {
	var ownerUser, ownerOrg sql.NullInt64
	err := baselineScan(ctx, q, "SELECT owner_user_id,owner_org_id FROM projects WHERE id="+placeholders(driver, 1), []any{id}, &ownerUser, &ownerOrg)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if ownerUser.Valid && ownerUser.Int64 > 0 {
		return true, nil, nil
	}
	if !ownerUser.Valid && ownerOrg.Valid && ownerOrg.Int64 > 0 {
		return true, ownerOrg.Int64, nil
	}
	return false, nil, nil
}

func baselineMetadataHasProject(value any) bool {
	var metadata map[string]any
	if json.Unmarshal([]byte(stringValue(value)), &metadata) != nil {
		return false
	}
	_, exists := metadata["project_id"]
	return exists
}

func baselineMetadataProjectID(value any) int64 {
	data, ok := value.([]byte)
	if !ok {
		if text, valid := value.(string); valid {
			data = []byte(text)
		} else {
			return 0
		}
	}
	var metadata map[string]any
	// Ent decodes historical metadata with json.Unmarshal, so integer-valued
	// decimal/exponent numbers follow the service's float64 safety boundary.
	if json.Unmarshal(data, &metadata) != nil {
		return 0
	}
	switch id := metadata["project_id"].(type) {
	case json.Number:
		value, _ := id.Int64()
		if value > 0 {
			return value
		}
	case float64:
		if id > 0 && id < float64(1<<53) && id == float64(int64(id)) {
			return int64(id)
		}
	case int64:
		if id > 0 {
			return id
		}
	}
	return 0
}

func stringValue(value any) string {
	switch current := value.(type) {
	case string:
		return current
	case []byte:
		return string(current)
	default:
		return ""
	}
}

func int64Value(value any) int64 {
	switch current := value.(type) {
	case int:
		return int64(current)
	case int64:
		return current
	case json.Number:
		parsed, _ := current.Int64()
		return parsed
	case []byte:
		parsed, _ := strconv.ParseInt(string(current), 10, 64)
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(current, 10, 64)
		return parsed
	default:
		return 0
	}
}

func baselineRowID(table baselineTable, row map[string]any) int {
	if len(table.primary) != 1 {
		return 0
	}
	switch value := row[table.primary[0]].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case []byte:
		n, _ := strconv.Atoi(string(value))
		return n
	case string:
		n, _ := strconv.Atoi(value)
		return n
	default:
		return 0
	}
}

func quotedColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = quoteIdentifier(column)
	}
	return strings.Join(quoted, ",")
}

func baselineValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "null"
	case baselineSemantic:
		return "semantic:" + string(value)
	case []byte:
		return "bytes:" + hex.EncodeToString(value)
	case time.Time:
		return "time:" + value.UTC().Format(time.RFC3339Nano)
	case string:
		return "string:" + value
	case int:
		return "int:" + strconv.FormatInt(int64(value), 10)
	case int64:
		return "int:" + strconv.FormatInt(value, 10)
	default:
		return fmt.Sprintf("%T:%v", value, value)
	}
}

func valueDigest(value any) string { return operation.Digest(baselineValue(value)) }

var baselineTimeColumns = func() map[string]bool {
	columns := map[string]bool{}
	for _, tables := range [][]*entschema.Table{source.Tables, migrate.Tables} {
		for _, table := range tables {
			for _, column := range table.Columns {
				if column.Type == field.TypeTime {
					columns[table.Name+":"+column.Name] = true
				}
			}
		}
	}
	return columns
}()

func baselineColumnValue(table, column string, value any) string {
	if baselineTimeColumns[table+":"+column] {
		if parsed, ok := parseBaselineTime(stringValue(value)); ok {
			return baselineValue(parsed)
		}
	}
	return baselineValue(value)
}

func baselineColumnDigest(table, column string, value any) string {
	return operation.Digest(baselineColumnValue(table, column, value))
}

func sqliteConversionTime(driver string, value time.Time) any {
	value = NormalizeConversionTime(value)
	if driver == "sqlite" {
		return value.Format(timeutil.SQLiteLayout)
	}
	return value
}

func parseBaselineTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, timeutil.SQLiteLayout, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func writeBaselineHash(h hash.Hash, value any) {
	data, _ := json.Marshal(value)
	fmt.Fprintf(h, "%d:", len(data))
	_, _ = h.Write(data)
}

func VerifyBaseline(ctx context.Context, db *sql.DB, p *operation.Plan, ready bool) (map[string]int64, error) {
	if p == nil || p.Baseline == nil {
		return nil, operation.Fail(operation.BackupIncomplete, "bound source baseline is missing")
	}
	if err := p.Baseline.Bind(p); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil && p.Source.Driver == "sqlite" {
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return verifyBaselineQuery(ctx, tx, p, ready)
}

func VerifyBaselineClient(ctx context.Context, client *ent.Client, p *operation.Plan, ready bool) error {
	if p == nil || p.Baseline == nil {
		return operation.Fail(operation.BackupIncomplete, "bound source baseline is missing")
	}
	if err := p.Baseline.Bind(p); err != nil {
		return err
	}
	if _, err := verifyBaselineQuery(ctx, client, p, ready); err != nil {
		return err
	}
	return nil
}

func verifyBaselineQuery(ctx context.Context, q baselineQueryer, p *operation.Plan, ready bool) (map[string]int64, error) {
	counts := make(map[string]int64, len(p.Baseline.Tables)+1)
	changes := make(map[string]operation.RowChange, len(p.Baseline.Changes))
	for _, change := range p.Baseline.Changes {
		key := change.Table + ":" + strconv.Itoa(change.ID)
		if _, duplicate := changes[key]; duplicate || change.ID <= 0 || len(change.Expected) == 0 || len(change.Before) != len(change.Expected) {
			return nil, operation.Fail(operation.Incompatible, "baseline contains duplicate or invalid mutable-row evidence")
		}
		for name, digest := range change.Expected {
			if !operation.ValidHex(digest, 64) || !operation.ValidHex(change.Before[name], 64) {
				return nil, operation.Fail(operation.Incompatible, "baseline mutable-field digest is invalid")
			}
		}
		changes[key] = change
	}
	usedChanges := make(map[string]bool, len(changes))
	for _, evidence := range p.Baseline.Tables {
		if evidence.Name == "refresh_tokens" && p.Kind == "upgrade" {
			var remaining int64
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM refresh_tokens", nil, &remaining); err != nil {
				return nil, err
			}
			if remaining != 0 {
				return nil, operation.Fail(operation.BaselineChanged, "target retained source refresh tokens")
			}
			counts["revoked_refresh_tokens"] = evidence.Count
			continue
		}
		if len(evidence.PrimaryKey) == 0 || len(evidence.Columns) == 0 {
			return nil, operation.Fail(operation.Incompatible, "baseline table key is outside the finite route")
		}
		keysHash, preservedHash := sha256.New(), sha256.New()
		writeBaselineHash(keysHash, evidence.PrimaryKey)
		writeBaselineHash(preservedHash, evidence.Columns)
		query := "SELECT " + quotedColumns(evidence.Columns) + " FROM " + quoteIdentifier(evidence.Name)
		if evidence.NumericID {
			query += fmt.Sprintf(" WHERE %s<=%d", quoteIdentifier(evidence.PrimaryKey[0]), evidence.MaxID)
		}
		baseQuery := query
		var count int64
		var cursor []any
		for {
			query = baseQuery
			if cursor != nil {
				if evidence.NumericID {
					query += " AND "
				} else {
					query += " WHERE "
				}
				query += "(" + quotedColumns(evidence.PrimaryKey) + ") > (" + placeholders(p.Source.Driver, len(cursor)) + ")"
			}
			query += " ORDER BY " + quotedColumns(evidence.PrimaryKey) + " LIMIT 500"
			rows, err := q.QueryContext(ctx, query, cursor...)
			if err != nil {
				return nil, err
			}
			var captured [][]any
			for rows.Next() {
				values := make([]any, len(evidence.Columns))
				pointers := make([]any, len(values))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err := rows.Scan(pointers...); err != nil {
					rows.Close()
					return nil, err
				}
				captured = append(captured, values)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
			for _, values := range captured {
				row := make(map[string]any, len(values))
				for i, name := range evidence.Columns {
					row[name] = values[i]
				}
				id := baselineRowID(baselineTable{primary: evidence.PrimaryKey}, row)
				if evidence.NumericID && int64(id) != int64Value(row[evidence.PrimaryKey[0]]) {
					rows.Close()
					return nil, operation.Fail(operation.BaselineChanged, "target contains an invalid source primary key")
				}
				key := make([]string, 0, len(evidence.PrimaryKey))
				for _, name := range evidence.PrimaryKey {
					if row[name] == nil {
						return nil, operation.Fail(operation.BaselineChanged, "target has a null stable primary key")
					}
					key = append(key, baselineValue(row[name]))
				}
				writeBaselineHash(keysHash, key)
				changeKey := evidence.Name + ":" + strconv.Itoa(id)
				change, declared := changes[changeKey]
				preserved := make([]string, 0, len(values))
				for i, name := range evidence.Columns {
					if declared {
						expected, mutable := change.Expected[name]
						if mutable {
							actual := baselineColumnDigest(evidence.Name, name, values[i])
							if actual != change.Before[name] {
								actual, err = baselineCellDigest(ctx, q, p, evidence.Name, id, name, values[i])
								if err != nil {
									return nil, err
								}
							}
							if ready && evidence.Name == "instance_initializations" && name == "data_version" && p.Kind == "upgrade" {
								expected = valueDigest(int64(3))
							}
							if actual != expected && (ready || actual != change.Before[name]) {
								rows.Close()
								return nil, operation.Fail(operation.BaselineChanged, "target mutable field differs from its exact before/expected value")
							}
							continue
						}
					}
					preserved = append(preserved, baselineColumnValue(evidence.Name, name, values[i]))
				}
				if declared {
					for name := range change.Expected {
						if !containsString(evidence.Columns, name) {
							rows.Close()
							return nil, operation.Fail(operation.Incompatible, "baseline mutable field is not in its frozen table")
						}
					}
					usedChanges[changeKey] = true
				}
				writeBaselineHash(preservedHash, preserved)
				count++
				cursor = make([]any, len(evidence.PrimaryKey))
				for i, name := range evidence.PrimaryKey {
					cursor[i] = row[name]
				}
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return nil, err
			}
			if len(captured) < 500 {
				break
			}
		}
		if count != evidence.Count || hex.EncodeToString(keysHash.Sum(nil)) != evidence.Keys || hex.EncodeToString(preservedHash.Sum(nil)) != evidence.Preserved {
			return nil, operation.Fail(operation.BaselineChanged, "target source rows differ from the bound baseline")
		}
		counts[evidence.Name] = count
	}
	if err := verifyBaselineAdditions(ctx, q, p, ready); err != nil {
		return nil, err
	}
	for key := range changes {
		if !usedChanges[key] {
			return nil, operation.Fail(operation.BaselineChanged, "declared mutable source row is missing")
		}
	}
	if ready && p.Kind == "upgrade" {
		var version int
		if err := baselineScan(ctx, q, "SELECT data_version FROM instance_initializations WHERE id=1", nil, &version); err != nil {
			return nil, err
		}
		if version != 3 {
			return nil, operation.Fail(operation.BaselineChanged, "ready baseline requires data version 3")
		}
		if err := verifyReadyAuthentication(ctx, q, p.Source.Driver); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

func verifyReadyAuthentication(ctx context.Context, q baselineQueryer, driver string) error {
	var version int
	if err := baselineScan(ctx, q, "SELECT version FROM linguaflow_auth_format WHERE id=1", nil, &version); err != nil || version != 1 {
		return operation.Fail(operation.BaselineChanged, "ready authentication format marker is invalid")
	}
	for _, table := range []string{"auth_connections", "local_account_policies"} {
		var count int64
		if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM "+quoteIdentifier(table), nil, &count); err != nil {
			return err
		}
		if count != 1 {
			return operation.Fail(operation.BaselineChanged, "ready authentication singleton contains undeclared rows")
		}
	}
	var password int64
	if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM auth_connections WHERE id='password' AND kind='password' AND aggregate_revision=1 AND current_revision=1 AND auth_generation=1 AND state IS NULL AND identity_authority_id IS NULL AND archived_at IS NULL AND (identity_contract IS NULL OR identity_contract='')", nil, &password); err != nil {
		return err
	}
	if password != 1 {
		return operation.Fail(operation.BaselineChanged, "ready password connection differs from the declared authentication baseline")
	}
	for _, table := range []string{"external_identities", "login_bindings", "refresh_sessions", "refresh_tokens", "auth_channel_policies", "auth_connection_revisions", "auth_connection_secrets", "auth_transactions"} {
		var count int64
		if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM "+quoteIdentifier(table), nil, &count); err != nil {
			return err
		}
		if count != 0 {
			return operation.Fail(operation.BaselineChanged, "ready database contains session or external identity rows")
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func NormalizeBaselineTimestamps(ctx context.Context, client *ent.Client, p *operation.Plan) error {
	if p == nil || p.Baseline == nil {
		return operation.Fail(operation.BackupIncomplete, "bound source baseline is missing")
	}
	if err := p.Baseline.Bind(p); err != nil {
		return err
	}
	tables := make(map[string]map[string]bool, len(p.Baseline.Tables))
	for _, table := range p.Baseline.Tables {
		columns := make(map[string]bool, len(table.Columns))
		for _, column := range table.Columns {
			columns[column] = true
		}
		tables[table.Name] = columns
	}
	stamp := sqliteConversionTime(p.Source.Driver, p.Baseline.ConversionTime)
	for _, change := range p.Baseline.Changes {
		if _, declared := change.Expected["updated_at"]; !declared {
			continue
		}
		columns, exists := tables[change.Table]
		if !exists || !columns["updated_at"] || !columns["id"] || change.ID <= 0 {
			return operation.Fail(operation.Incompatible, "baseline timestamp target is outside its frozen table")
		}
		query := "UPDATE " + quoteIdentifier(change.Table) + " SET " + quoteIdentifier("updated_at") + "=" + placeholders(p.Source.Driver, 1) + " WHERE " + quoteIdentifier("id") + "=" + placeholdersOffset(p.Source.Driver, 2)
		result, err := client.ExecContext(ctx, query, stamp, change.ID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return operation.Fail(operation.BaselineChanged, "declared timestamp row is missing")
		}
	}
	return nil
}

type baselineSemantic string

func baselineStorageManifest(p *operation.Plan) (*storagemigrate.Manifest, error) {
	if p.StorageManifest == "" {
		return nil, nil
	}
	m, err := storagemigrate.ReadManifest(p.StorageManifest)
	if err != nil || operation.Digest(m) != p.StorageDigest {
		return nil, operation.Fail(operation.Incompatible, "immutable storage manifest changed")
	}
	return m, nil
}

func captureBaselineAdditions(ctx context.Context, q baselineQueryer, p *operation.Plan, b *operation.Baseline) error {
	added := map[string]int64{}
	settings := []string{}
	if err := capturePathClaimFacts(ctx, q, p, b); err != nil {
		return err
	}
	var claimIDs []int64
	if err := json.Unmarshal([]byte(b.Facts["path_claim_spaces"]), &claimIDs); err != nil {
		return err
	}
	added["storage_path_claims"] = int64(len(claimIDs))
	added["storage_namespaces"], _ = strconv.ParseInt(b.Facts["namespace_additions"], 10, 64)
	var retention int64
	if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM system_settings WHERE key='task_retention'", nil, &retention); err != nil {
		return err
	}
	if p.Kind == "upgrade" && retention == 0 {
		settings = append(settings, "task_retention")
	}
	m, err := baselineStorageManifest(p)
	if err != nil {
		return err
	}
	if m != nil {
		for _, e := range m.Entries {
			if !e.Rejected {
				added["blobs"]++
				added["blob_locations"]++
				added["source_revisions"]++
			}
		}
		added["storage_tasks"] = 1
		for _, backend := range []string{"legacy", m.DefaultBackendID} {
			var connections, spaces int64
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_connections WHERE backend_id="+placeholders(p.Source.Driver, 1)+" AND owner_kind='site'", []any{backend}, &connections); err != nil {
				return err
			}
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.backend_id="+placeholders(p.Source.Driver, 1)+" AND co.owner_kind='site'", []any{backend}, &spaces); err != nil {
				return err
			}
			if connections > 1 || spaces > 1 {
				return operation.Fail(operation.Incompatible, "storage manifest backend is ambiguous")
			}
			if connections == 0 {
				added["storage_connections"]++
			}
			if spaces == 0 {
				added["storage_spaces"]++
			}
		}
		var policies int64
		if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM system_settings WHERE key='storage_policy'", nil, &policies); err != nil {
			return err
		}
		if policies == 0 {
			settings = append(settings, "storage_policy")
		}
	}
	data, err := json.Marshal(added)
	if err != nil {
		return err
	}
	b.Facts["appended"] = string(data)
	data, err = json.Marshal(settings)
	if err != nil {
		return err
	}
	b.Facts["appended_settings"] = string(data)
	return nil
}

func expectedStorageValues(ctx context.Context, q baselineQueryer, p *operation.Plan, table string, id int, row map[string]any, conversionTime time.Time) (map[string]any, error) {
	values := map[string]any{}
	if table != "resources" && table != "projects" && table != "jobs" && table != "storage_spaces" && table != "system_settings" {
		return values, nil
	}
	m, err := baselineStorageManifest(p)
	if err != nil {
		return nil, err
	}
	stamp := sqliteConversionTime(p.Source.Driver, conversionTime)
	switch table {
	case "resources":
		for _, e := range m.Entries {
			if e.Rejected || e.ResourceID != id {
				continue
			}
			values["current_source_revision_id"] = baselineSemantic(fmt.Sprintf("revision:%s:%d", m.OperationID, id))
			values["source_generation"] = e.SourceGeneration + 1
			values["updated_at"] = stamp
		}
	case "projects":
		for _, prior := range m.Projects {
			if prior.ID != id {
				continue
			}
			values["storage_generation"] = prior.Generation + 2
			values["storage_state"] = "active"
			if prior.PreviousSpaceID == nil {
				values["storage_space_id"] = baselineSemantic("default-space:" + m.DefaultBackendID)
			}
			values["updated_at"] = stamp
		}
	case "jobs":
		for _, prior := range m.Jobs {
			if prior.ID != id {
				continue
			}
			values["status"] = "failed"
			values["error_message"] = "storage_migration_needs_action: legacy job input cannot be proven; create a new task from the current source"
			values["updated_at"] = stamp
		}
	case "storage_spaces":
		var backend string
		if err := baselineScan(ctx, q, "SELECT backend_id FROM storage_connections WHERE id="+placeholders(p.Source.Driver, 1)+" AND owner_kind='site'", []any{row["connection_id"]}, &backend); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if backend == "legacy" {
			bytes := int64Value(row["live_bytes"])
			for _, e := range m.Entries {
				if !e.Rejected && e.ObservedSize != nil {
					bytes += *e.ObservedSize
				}
			}
			values["live_bytes"], values["updated_at"] = bytes, stamp
		}
	case "system_settings":
		if stringValue(row["key"]) == "storage_policy" {
			value, changed, err := storagemigrate.CanonicalPolicy(stringValue(row["value"]), m.CapacityBytes, m.LogicalLimitBytes)
			if err != nil {
				return nil, err
			}
			if changed {
				values["value"], values["updated_at"] = value, stamp
			}
		}
	}
	return values, nil
}

func baselineCellDigest(ctx context.Context, q baselineQueryer, p *operation.Plan, table string, id int, column string, value any) (string, error) {
	if p.StorageManifest == "" || (table != "resources" || column != "current_source_revision_id") && (table != "projects" || column != "storage_space_id") {
		return baselineColumnDigest(table, column, value), nil
	}
	m, err := baselineStorageManifest(p)
	if err != nil {
		return "", err
	}
	if table == "projects" {
		var backend string
		if err := baselineScan(ctx, q, "SELECT co.backend_id FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE sp.id="+placeholders(p.Source.Driver, 1)+" AND co.owner_kind='site'", []any{value}, &backend); err != nil {
			return "", err
		}
		return valueDigest(baselineSemantic("default-space:" + backend)), nil
	}
	var resourceID int64
	var identity string
	err = baselineScan(ctx, q, "SELECT sr.resource_id,b.identity FROM source_revisions sr JOIN blobs b ON b.id=sr.source_blob_id WHERE sr.id="+placeholders(p.Source.Driver, 1), []any{value}, &resourceID, &identity)
	if err != nil {
		return "", err
	}
	if resourceID != int64(id) || identity != fmt.Sprintf("legacy-%s-%d", m.OperationID, id) {
		return "", operation.Fail(operation.BaselineChanged, "source revision does not match its durable resource receipt")
	}
	return valueDigest(baselineSemantic(fmt.Sprintf("revision:%s:%d", m.OperationID, id))), nil
}

func verifyBaselineAdditions(ctx context.Context, q baselineQueryer, p *operation.Plan, ready bool) error {
	var expected map[string]int64
	var settings []string
	if json.Unmarshal([]byte(p.Baseline.Facts["appended"]), &expected) != nil || json.Unmarshal([]byte(p.Baseline.Facts["appended_settings"]), &settings) != nil {
		return operation.Fail(operation.Incompatible, "baseline appended-row rules are missing")
	}
	m, err := baselineStorageManifest(p)
	if err != nil {
		return err
	}
	for _, table := range p.Baseline.Tables {
		if !table.NumericID || p.Kind == "upgrade" && table.Name == "refresh_tokens" {
			continue
		}
		var total int64
		where := fmt.Sprintf("%s>%d", quoteIdentifier(table.PrimaryKey[0]), table.MaxID)
		if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM "+quoteIdentifier(table.Name)+" WHERE "+where, nil, &total); err != nil {
			return err
		}
		if table.Name == "system_settings" {
			var permitted int64
			for _, key := range settings {
				var value string
				err := baselineScan(ctx, q, "SELECT value FROM system_settings WHERE "+where+" AND key="+placeholders(p.Source.Driver, 1), []any{key}, &value)
				if errors.Is(err, sql.ErrNoRows) {
					if ready {
						return operation.Fail(operation.BaselineChanged, "declared new policy is missing")
					}
					continue
				}
				if err != nil {
					return err
				}
				if key == "task_retention" {
					if !ready {
						return operation.Fail(operation.BaselineChanged, "task retention was published before ready")
					}
					var policy map[string]any
					if json.Unmarshal([]byte(value), &policy) != nil || operation.Digest(policy) != operation.Digest(map[string]any{"enabled": false, "retention_days": float64(30), "revision": float64(1)}) {
						return operation.Fail(operation.BaselineChanged, "new task retention differs from its default")
					}
				} else if key == "storage_policy" && m != nil {
					canonical, _, err := storagemigrate.CanonicalPolicy(`{"mode":"site_only","default_choice":"site","generation":0}`, m.CapacityBytes, m.LogicalLimitBytes)
					if err != nil || value != canonical {
						return operation.Fail(operation.BaselineChanged, "new storage policy differs from its manifest")
					}
				} else {
					return operation.Fail(operation.Incompatible, "unknown appended policy rule")
				}
				permitted++
			}
			if total != permitted {
				return operation.Fail(operation.BaselineChanged, "undeclared policy rows were appended")
			}
			continue
		}
		want := expected[table.Name]
		if total != want && (ready || total != 0) {
			return operation.Fail(operation.BaselineChanged, "appended row count differs from the declared manifest")
		}
		if total == 0 {
			continue
		}
		if m == nil && table.Name != "storage_path_claims" && table.Name != "storage_namespaces" {
			return operation.Fail(operation.BaselineChanged, "business rows were appended without a storage manifest")
		}
		var matched int64
		switch table.Name {
		case "blobs", "blob_locations", "source_revisions":
			for _, e := range m.Entries {
				if e.Rejected {
					continue
				}
				var query string
				switch table.Name {
				case "blobs":
					query = "SELECT COUNT(*) FROM blobs WHERE id>" + strconv.FormatInt(table.MaxID, 10) + " AND identity=" + placeholders(p.Source.Driver, 1)
				case "blob_locations":
					query = "SELECT COUNT(*) FROM blob_locations l JOIN blobs b ON b.active_location_id=l.id WHERE l.id>" + strconv.FormatInt(table.MaxID, 10) + " AND b.identity=" + placeholders(p.Source.Driver, 1)
				case "source_revisions":
					query = "SELECT COUNT(*) FROM source_revisions sr JOIN resources r ON r.current_source_revision_id=sr.id JOIN blobs b ON b.id=sr.source_blob_id WHERE sr.id>" + strconv.FormatInt(table.MaxID, 10) + " AND b.identity=" + placeholders(p.Source.Driver, 1)
				}
				var count int64
				if err := baselineScan(ctx, q, query, []any{fmt.Sprintf("legacy-%s-%d", m.OperationID, e.ResourceID)}, &count); err != nil {
					return err
				}
				matched += count
			}
		case "storage_tasks":
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_tasks WHERE "+where+" AND operation_id="+placeholders(p.Source.Driver, 1)+" AND idempotency_key="+placeholdersOffset(p.Source.Driver, 2)+" AND kind='legacy_migration' AND status='completed' AND phase='committed' AND error_code='' AND request_hash="+placeholdersOffset(p.Source.Driver, 3), []any{m.OperationID, m.OperationID, storagemigrate.Fingerprint(m)}, &matched); err != nil {
				return err
			}
		case "storage_connections":
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_connections WHERE "+where+" AND owner_kind='site' AND driver='local' AND status='enabled' AND backend_id IN ("+placeholders(p.Source.Driver, 2)+")", []any{"legacy", m.DefaultBackendID}, &matched); err != nil {
				return err
			}
		case "storage_spaces":
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE sp.id>"+strconv.FormatInt(table.MaxID, 10)+" AND co.owner_kind='site' AND ((co.backend_id='legacy' AND sp.status='read_only') OR (co.backend_id="+placeholders(p.Source.Driver, 1)+" AND sp.status='active'))", []any{m.DefaultBackendID}, &matched); err != nil {
				return err
			}
		case "storage_path_claims":
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_path_claims pc JOIN storage_spaces sp ON sp.id=pc.space_id JOIN storage_connections co ON co.id=sp.connection_id WHERE pc.id>"+strconv.FormatInt(table.MaxID, 10)+" AND co.driver='s3' AND sp.lifecycle<>'removed' AND sp.bucket<>'' AND pc.identity=sp.identity AND pc.bucket=sp.bucket AND pc.prefix=sp.prefix AND pc.execution_generation=sp.execution_generation", nil, &matched); err != nil {
				return err
			}
		case "storage_namespaces":
			if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_namespaces ns WHERE ns.id>"+strconv.FormatInt(table.MaxID, 10)+" AND EXISTS (SELECT 1 FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.driver='s3' AND sp.lifecycle<>'removed' AND sp.bucket=ns.bucket)", nil, &matched); err != nil {
				return err
			}
		default:
			return operation.Fail(operation.BaselineChanged, "undeclared business table gained rows")
		}
		if matched != total {
			return operation.Fail(operation.BaselineChanged, "appended rows do not match their durable receipts")
		}
	}
	if err := verifyPathClaimFacts(ctx, q, p, ready); err != nil {
		return err
	}
	if m != nil {
		var tasks int64
		if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM storage_tasks WHERE operation_id="+placeholders(p.Source.Driver, 1)+" AND phase='committed'", []any{m.OperationID}, &tasks); err != nil {
			return err
		}
		if ready || tasks != 0 {
			return verifyStorageBaselineReceipts(ctx, q, p, m)
		}
	}
	return nil
}

func capturePathClaimFacts(ctx context.Context, q baselineQueryer, p *operation.Plan, b *operation.Baseline) error {
	query := "SELECT sp.id FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.driver='s3' AND sp.bucket<>''"
	if p.Kind == "storage" {
		query += " AND sp.lifecycle<>'removed' AND NOT EXISTS (SELECT 1 FROM storage_path_claims pc WHERE pc.space_id=sp.id)"
	}
	query += " ORDER BY sp.id"
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	b.Facts["path_claim_spaces"] = string(data)
	var namespaces []string
	if p.Kind == "storage" {
		rows, err := q.QueryContext(ctx, "SELECT bucket FROM storage_namespaces ORDER BY bucket")
		if err != nil {
			return err
		}
		for rows.Next() {
			var bucket string
			if err := rows.Scan(&bucket); err != nil {
				rows.Close()
				return err
			}
			namespaces = append(namespaces, valueDigest(bucket))
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	data, err = json.Marshal(namespaces)
	if err != nil {
		return err
	}
	b.Facts["namespace_keys"] = string(data)
	query = "SELECT COUNT(DISTINCT sp.bucket) FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.driver='s3' AND sp.bucket<>''"
	if p.Kind == "storage" {
		query += " AND sp.lifecycle<>'removed' AND NOT EXISTS (SELECT 1 FROM storage_namespaces ns WHERE ns.bucket=sp.bucket)"
	}
	var count int64
	if err := baselineScan(ctx, q, query, nil, &count); err != nil {
		return err
	}
	b.Facts["namespace_additions"] = strconv.FormatInt(count, 10)
	return nil
}

func verifyPathClaimFacts(ctx context.Context, q baselineQueryer, p *operation.Plan, ready bool) error {
	if _, exists := p.Baseline.Facts["path_claim_spaces"]; !exists {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(p.Baseline.Facts["path_claim_spaces"]), &ids); err != nil {
		return operation.Fail(operation.Incompatible, "invalid path claim baseline")
	}
	var max int64
	for _, table := range p.Baseline.Tables {
		if table.Name == "storage_path_claims" {
			max = table.MaxID
		}
	}
	var total, matched int64
	if err := baselineScan(ctx, q, fmt.Sprintf("SELECT COUNT(*) FROM storage_path_claims WHERE id>%d", max), nil, &total); err != nil {
		return err
	}
	for _, id := range ids {
		var count int64
		query := "SELECT COUNT(*) FROM storage_path_claims pc JOIN storage_spaces sp ON sp.id=pc.space_id JOIN storage_connections co ON co.id=sp.connection_id WHERE sp.id=" + placeholders(p.Source.Driver, 1) + fmt.Sprintf(" AND pc.id>%d", max) + " AND co.driver='s3' AND sp.lifecycle<>'removed' AND pc.identity=sp.identity AND pc.bucket=sp.bucket AND pc.prefix=sp.prefix AND pc.execution_generation=sp.execution_generation"
		if err := baselineScan(ctx, q, query, []any{id}, &count); err != nil {
			return err
		}
		matched += count
	}
	if total != matched || ready && matched != int64(len(ids)) {
		return operation.Fail(operation.BaselineChanged, "path claims differ from their declared spaces")
	}
	var original []string
	if err := json.Unmarshal([]byte(p.Baseline.Facts["namespace_keys"]), &original); err != nil {
		return operation.Fail(operation.Incompatible, "namespace key baseline is invalid")
	}
	names, err := baselineNames(ctx, q, "SELECT bucket FROM storage_namespaces ORDER BY bucket")
	if err != nil {
		return err
	}
	var expectedBuckets []string
	if len(ids) > 0 || ready {
		expectedBuckets, err = baselineNames(ctx, q, "SELECT DISTINCT sp.bucket FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.driver='s3' AND sp.lifecycle<>'removed' AND sp.bucket<>'' ORDER BY sp.bucket")
		if err != nil {
			return err
		}
	}
	for _, bucket := range names {
		if !containsString(original, valueDigest(bucket)) && !containsString(expectedBuckets, bucket) {
			return operation.Fail(operation.BaselineChanged, "undeclared storage namespace was appended")
		}
	}
	if ready {
		for _, bucket := range expectedBuckets {
			if !containsString(names, bucket) {
				return operation.Fail(operation.BaselineChanged, "declared storage namespace is missing")
			}
		}
	}
	return nil
}

func verifyStorageBaselineReceipts(ctx context.Context, q baselineQueryer, p *operation.Plan, m *storagemigrate.Manifest) error {
	for _, e := range m.Entries {
		if e.Rejected {
			continue
		}
		var resourceID, projectID, blobProjectID, ownerID, locationBlobID, locationGeneration, locationSize int64
		var format, parser, verification, identity, ownerKind, purpose, blobStatus, objectKey, locationStatus, integrity, backend, provider string
		var revisionSize, blobSize sql.NullInt64
		var revisionSHA, blobSHA sql.NullString
		var current, deleted, deleteMarker bool
		var retain, locationRetain, verifiedAt any
		query := "SELECT sr.resource_id,sr.project_id,sr.format,sr.parser_version,sr.verification_state,sr.size,sr.sha256,sr.current,sr.deleted,sr.retain_until,b.identity,b.project_id,b.owner_kind,b.owner_id,b.purpose,b.size,b.sha256,b.status,b.location_generation,l.blob_id,l.object_key,l.size,l.status,l.integrity,l.provider_version,l.delete_marker,l.retain_until,l.verified_at,co.backend_id FROM resources r JOIN source_revisions sr ON sr.id=r.current_source_revision_id JOIN blobs b ON b.id=sr.source_blob_id JOIN blob_locations l ON l.id=b.active_location_id JOIN storage_spaces sp ON sp.id=l.space_id JOIN storage_connections co ON co.id=sp.connection_id WHERE r.id=" + placeholders(p.Source.Driver, 1)
		if err := baselineScan(ctx, q, query, []any{e.ResourceID}, &resourceID, &projectID, &format, &parser, &verification, &revisionSize, &revisionSHA, &current, &deleted, &retain, &identity, &blobProjectID, &ownerKind, &ownerID, &purpose, &blobSize, &blobSHA, &blobStatus, &locationGeneration, &locationBlobID, &objectKey, &locationSize, &locationStatus, &integrity, &provider, &deleteMarker, &locationRetain, &verifiedAt, &backend); err != nil {
			return err
		}
		if resourceID != int64(e.ResourceID) || projectID != int64(e.ProjectID) || blobProjectID != projectID || format != e.Format || parser != "legacy_unknown" || verification != e.Verification || !current || deleted || retain != nil || identity != fmt.Sprintf("legacy-%s-%d", m.OperationID, e.ResourceID) || purpose != "source" || blobStatus != "ready" || locationGeneration != 1 || objectKey != e.ObjectKey || locationStatus != "live" || integrity != e.Integrity || provider != "" || deleteMarker || locationRetain != nil || backend != "legacy" {
			return operation.Fail(operation.BaselineChanged, "imported source revision/blob/location differs from its exact manifest receipt")
		}
		var expectedBlobID int64
		if err := baselineScan(ctx, q, "SELECT id FROM blobs WHERE identity="+placeholders(p.Source.Driver, 1), []any{identity}, &expectedBlobID); err != nil {
			return err
		}
		if expectedBlobID != locationBlobID {
			return operation.Fail(operation.BaselineChanged, "imported location points to a different blob")
		}
		var user, org sql.NullInt64
		if err := baselineScan(ctx, q, "SELECT owner_user_id,owner_org_id FROM projects WHERE id="+placeholders(p.Source.Driver, 1), []any{e.ProjectID}, &user, &org); err != nil {
			return err
		}
		if user.Valid {
			if ownerKind != "user" || ownerID != user.Int64 {
				return operation.Fail(operation.BaselineChanged, "imported blob owner differs from its project")
			}
		} else if !org.Valid || ownerKind != "org" || ownerID != org.Int64 {
			return operation.Fail(operation.BaselineChanged, "imported blob organization differs from its project")
		}
		bytes := int64(0)
		if e.ObservedSize != nil {
			bytes = *e.ObservedSize
		}
		if locationSize != bytes {
			return operation.Fail(operation.BaselineChanged, "imported location size differs from manifest")
		}
		if e.Verification == "verified" {
			if e.ObservedSize == nil || !revisionSize.Valid || !blobSize.Valid || revisionSize.Int64 != bytes || blobSize.Int64 != bytes || !revisionSHA.Valid || !blobSHA.Valid || revisionSHA.String != e.ObservedSHA256 || blobSHA.String != e.ObservedSHA256 {
				return operation.Fail(operation.BaselineChanged, "imported source size/hash differs from manifest")
			}
		} else if revisionSize.Valid || blobSize.Valid || revisionSHA.Valid || blobSHA.Valid {
			return operation.Fail(operation.BaselineChanged, "unverified import invented size/hash evidence")
		}
		if e.Integrity == "available" {
			actualTime, ok := parseBaselineTime(stringValue(verifiedAt))
			if stamp, valid := verifiedAt.(time.Time); valid {
				actualTime, ok = stamp, true
			}
			if !ok || !NormalizeConversionTime(actualTime).Equal(NormalizeConversionTime(m.CreatedAt)) {
				return operation.Fail(operation.BaselineChanged, "imported location verification time differs from manifest")
			}
		} else if verifiedAt != nil {
			return operation.Fail(operation.BaselineChanged, "unavailable import invented verification time")
		}
	}
	var inputText any
	if err := baselineScan(ctx, q, "SELECT input FROM storage_tasks WHERE operation_id="+placeholders(p.Source.Driver, 1), []any{m.OperationID}, &inputText); err != nil {
		return err
	}
	var input map[string]any
	decoder := json.NewDecoder(strings.NewReader(stringValue(inputText)))
	decoder.UseNumber()
	if decoder.Decode(&input) != nil || stringValue(input["legacy_root"]) != m.LegacyRoot || stringValue(input["default_root"]) != m.DefaultRoot || int64Value(input["generation_protocol"]) != 2 {
		return operation.Fail(operation.BaselineChanged, "storage operation input differs from its manifest")
	}
	for _, backend := range []string{"legacy", m.DefaultBackendID} {
		var id int64
		var live, reserved, candidate, pending, owner int64
		var identity, nonce, kind, status string
		var capacity sql.NullInt64
		query := "SELECT sp.id,sp.identity,sp.marker_nonce,sp.owner_kind,sp.owner_id,sp.capacity_bytes,sp.status,sp.live_bytes,sp.reserved_bytes,sp.candidate_bytes,sp.pending_delete_bytes FROM storage_spaces sp JOIN storage_connections co ON co.id=sp.connection_id WHERE co.owner_kind='site' AND co.backend_id=" + placeholders(p.Source.Driver, 1)
		if err := baselineScan(ctx, q, query, []any{backend}, &id, &identity, &nonce, &kind, &owner, &capacity, &status, &live, &reserved, &candidate, &pending); err != nil {
			return err
		}
		key := "default_space_id"
		if backend == "legacy" {
			key = "legacy_space_id"
		}
		if int64Value(input[key]) != id {
			return operation.Fail(operation.BaselineChanged, "storage task space receipt differs from its backend")
		}
		var oldMax int64
		for _, table := range p.Baseline.Tables {
			if table.Name == "storage_spaces" {
				oldMax = table.MaxID
			}
		}
		if id <= oldMax {
			continue
		}
		wantLive := int64(0)
		wantStatus := "active"
		if backend == "legacy" {
			wantStatus = "read_only"
			for _, e := range m.Entries {
				if !e.Rejected && e.ObservedSize != nil {
					wantLive += *e.ObservedSize
				}
			}
		}
		if !operation.ValidHex(identity, 32) || !operation.ValidHex(nonce, 32) || kind != "site" || owner != 0 || status != wantStatus || live != wantLive || reserved != 0 || candidate != 0 || pending != 0 || capacity.Valid != (m.CapacityBytes != nil) || capacity.Valid && capacity.Int64 != *m.CapacityBytes {
			return operation.Fail(operation.BaselineChanged, "new storage space differs from its exact manifest defaults")
		}
	}
	return nil
}

func placeholdersOffset(driver string, index int) string {
	if driver == "postgres" {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

// BusinessFingerprint binds all business rows, DDL and allocation semantics.
// Operation receipts are excluded from both descriptors and data to avoid a
// digest depending on its own ready receipt.
func BusinessFingerprint(ctx context.Context, db *sql.DB, driver string) (string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil && driver == "sqlite" {
		tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	}
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return businessFingerprintQuery(ctx, tx, driver)
}

func BusinessFingerprintClient(ctx context.Context, client *ent.Client, driver string) (string, error) {
	return businessFingerprintQuery(ctx, client, driver)
}

func businessFingerprintQuery(ctx context.Context, q baselineQueryer, driver string) (string, error) {
	h := sha256.New()
	var namesQuery string
	var descriptors []string
	switch driver {
	case "sqlite":
		namesQuery = "SELECT name FROM sqlite_schema WHERE type='table' AND name NOT GLOB 'sqlite_*' AND name NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY name"
		descriptors = []string{
			"SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*' AND tbl_name NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY type,name",
			"PRAGMA user_version",
		}
		var exists int
		if err := baselineScan(ctx, q, "SELECT COUNT(*) FROM sqlite_schema WHERE name='sqlite_sequence'", nil, &exists); err != nil {
			return "", err
		}
		if exists != 0 {
			descriptors = append(descriptors, "SELECT name,seq FROM sqlite_sequence WHERE name NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY name")
		}
	case "postgres":
		namesQuery = "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY tablename"
		descriptors = []string{
			"SELECT table_name,column_name,ordinal_position,data_type,udt_name,is_nullable,column_default,is_identity,identity_generation,identity_start,identity_increment,identity_maximum,identity_minimum,identity_cycle,is_generated,generation_expression,character_maximum_length,numeric_precision,numeric_scale,collation_name FROM information_schema.columns WHERE table_schema='public' AND table_name NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY table_name,ordinal_position",
			"SELECT c.relname,k.conname,k.contype,pg_get_constraintdef(k.oid,true),k.convalidated,k.condeferrable,k.condeferred FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY c.relname,k.conname",
			"SELECT tablename,indexname,indexdef FROM pg_indexes WHERE schemaname='public' AND tablename NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY tablename,indexname",
			"SELECT s.relname,t.relname,a.attname,format_type(ps.seqtypid,NULL),ps.seqstart,ps.seqincrement,ps.seqmax,ps.seqmin,ps.seqcache,ps.seqcycle FROM pg_sequence ps JOIN pg_class s ON s.oid=ps.seqrelid JOIN pg_namespace n ON n.oid=s.relnamespace LEFT JOIN pg_depend d ON d.objid=s.oid AND d.classid='pg_class'::regclass AND d.deptype IN ('a','i') LEFT JOIN pg_class t ON t.oid=d.refobjid LEFT JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=d.refobjsubid WHERE n.nspname='public' AND COALESCE(t.relname,'') NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY s.relname",
		}
	default:
		return "", operation.Fail(operation.UnsupportedSource, "unsupported business fingerprint driver")
	}
	for _, query := range descriptors {
		if err := fingerprintBusinessRows(ctx, q, h, query); err != nil {
			return "", err
		}
	}
	names, err := baselineNames(ctx, q, namesQuery)
	if err != nil {
		return "", err
	}
	for _, name := range names {
		writeBaselineHash(h, name)
		primaryQuery := "SELECT name FROM pragma_table_info(" + "'" + strings.ReplaceAll(name, "'", "''") + "') WHERE pk>0 ORDER BY pk"
		if driver == "postgres" {
			primaryQuery = "SELECT a.attname FROM pg_index i JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,position) JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=k.attnum WHERE i.indisprimary AND n.nspname='public' AND t.relname='" + strings.ReplaceAll(name, "'", "''") + "' ORDER BY k.position"
		}
		primary, err := baselineNames(ctx, q, primaryQuery)
		if err != nil {
			return "", err
		}
		if len(primary) == 0 {
			return "", operation.Fail(operation.Incompatible, "business fingerprint requires a stable primary key")
		}
		if err := fingerprintBusinessRows(ctx, q, h, "SELECT * FROM "+quoteIdentifier(name)+" ORDER BY "+quotedColumns(primary)); err != nil {
			return "", err
		}
	}
	if driver == "postgres" {
		sequences, err := baselineNames(ctx, q, "SELECT s.relname FROM pg_class s JOIN pg_namespace n ON n.oid=s.relnamespace LEFT JOIN pg_depend d ON d.objid=s.oid AND d.classid='pg_class'::regclass AND d.deptype IN ('a','i') LEFT JOIN pg_class t ON t.oid=d.refobjid WHERE n.nspname='public' AND s.relkind='S' AND COALESCE(t.relname,'') NOT IN ('linguaflow_migration_state','linguaflow_migration_steps') ORDER BY s.relname")
		if err != nil {
			return "", err
		}
		for _, name := range sequences {
			writeBaselineHash(h, name)
			if err := fingerprintBusinessRows(ctx, q, h, "SELECT last_value,is_called FROM public."+quoteIdentifier(name)); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func baselineNames(ctx context.Context, q baselineQueryer, query string) ([]string, error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, errors.Join(rows.Err(), rows.Close())
}

func fingerprintBusinessRows(ctx context.Context, q baselineQueryer, h hash.Hash, query string) error {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	types, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	writeBaselineHash(h, columns)
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		canonical := make([]string, len(values))
		for i, value := range values {
			typeName := strings.ToUpper(types[i].DatabaseTypeName())
			if strings.Contains(typeName, "TIME") || typeName == "DATE" {
				if parsed, ok := parseBaselineTime(stringValue(value)); ok {
					value = parsed
				}
			}
			canonical[i] = baselineValue(value)
		}
		writeBaselineHash(h, operation.Digest(canonical))
		count++
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	writeBaselineHash(h, count)
	return nil
}
