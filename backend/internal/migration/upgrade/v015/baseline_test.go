package v015

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
)

func baselineTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "baseline.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBaselineStablePrimaryKeysAndTampering(t *testing.T) {
	for _, kind := range []string{"numeric", "string", "composite"} {
		t.Run(kind, func(t *testing.T) {
			ctx := t.Context()
			db := baselineTestDB(t)
			table := baselineTable{name: "business", columns: []string{"id", "payload"}, primary: []string{"id"}, numeric: kind == "numeric"}
			ddl := "CREATE TABLE business(id INTEGER PRIMARY KEY,payload TEXT NOT NULL)"
			if kind == "string" {
				ddl = "CREATE TABLE business(id TEXT PRIMARY KEY,payload TEXT NOT NULL)"
			}
			if kind == "composite" {
				ddl = "CREATE TABLE business(id TEXT NOT NULL,part INTEGER NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(id,part))"
				table.columns = []string{"id", "part", "payload"}
				table.primary = []string{"id", "part"}
			}
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 1; i <= 1001; i++ {
				var id any = i
				if kind != "numeric" {
					id = fmt.Sprintf("key-%04d", i)
				}
				query := "INSERT INTO business(id,payload) VALUES(?,?)"
				args := []any{id, "private-business-text"}
				if kind == "composite" {
					query = "INSERT INTO business(id,part,payload) VALUES(?,?,?)"
					args = []any{id, i % 7, "private-business-text"}
				}
				if _, err := tx.Exec(query, args...); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			p := &operation.Plan{Version: operation.Version, ID: strings.Repeat("a", 32), Kind: "storage", Source: operation.Endpoint{Driver: "sqlite"}}
			evidence, changes, err := captureTableBaseline(ctx, db, p, table, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			p.Baseline = &operation.Baseline{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), ConversionTime: time.Now().UTC(), Tables: []operation.TableBaseline{evidence}, Changes: changes, Facts: map[string]string{"appended": "{}", "appended_settings": "[]"}}
			if counts, err := VerifyBaseline(ctx, db, p, false); err != nil || counts["business"] != 1001 {
				t.Fatalf("stable %s keys: %v %v", kind, counts, err)
			}
			data, err := json.Marshal(p.Baseline)
			if err != nil || strings.Contains(string(data), "private-business-text") {
				t.Fatal("baseline exposed business plaintext", err)
			}
			var last any = 1001
			if kind != "numeric" {
				last = "key-1001"
			}
			if _, err := db.Exec("UPDATE business SET payload='tampered' WHERE id=?", last); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyBaseline(ctx, db, p, false); err == nil {
				t.Fatal("accepted a changed preserved row across batch boundary")
			}
			if _, err := db.Exec("UPDATE business SET payload='private-business-text' WHERE id=?", last); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("DELETE FROM business WHERE id=?", last); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyBaseline(ctx, db, p, false); err == nil {
				t.Fatal("accepted a missing stable primary key")
			}
		})
	}
}

func TestBaselineNumericPrimaryKeyRange(t *testing.T) {
	for _, tc := range []struct {
		name      string
		id        int64
		outside32 bool
	}{
		{name: "min-int64", id: math.MinInt64, outside32: true},
		{name: "below-min-int32", id: math.MinInt32 - 1, outside32: true},
		{name: "min-int32", id: math.MinInt32},
		{name: "zero", id: 0},
		{name: "max-int32", id: math.MaxInt32},
		{name: "above-max-int32", id: math.MaxInt32 + 1, outside32: true},
		{name: "max-int64", id: math.MaxInt64, outside32: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := baselineTestDB(t)
			if _, err := db.Exec("CREATE TABLE business(id INTEGER PRIMARY KEY,payload TEXT NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO business(id,payload) VALUES(?,?)", tc.id, "preserved"); err != nil {
				t.Fatal(err)
			}
			p := &operation.Plan{Kind: "storage", Source: operation.Endpoint{Driver: "sqlite"}}
			table := baselineTable{name: "business", columns: []string{"id", "payload"}, primary: []string{"id"}, numeric: true}
			evidence, changes, err := captureTableBaseline(t.Context(), db, p, table, time.Now().UTC())
			if strconv.IntSize == 32 && tc.outside32 {
				var classified *operation.Error
				if !errors.As(err, &classified) || classified.Code != operation.UnsupportedSource {
					t.Fatalf("out-of-range primary key: got %v, want %s", err, operation.UnsupportedSource)
				}
				if evidence.Count != 0 || len(changes) != 0 {
					t.Fatal("out-of-range primary key entered baseline evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Count != 1 || evidence.MaxID != max(int64(0), tc.id) || len(changes) != 0 {
				t.Fatalf("primary key %d: unexpected evidence %+v or changes %v", tc.id, evidence, changes)
			}
		})
	}
}

func TestBaselineAccountEmailUsesScalarAndNil(t *testing.T) {
	db, _, secret := releaseFixture(t, "serve")
	p := fixturePlan(t, db, "serve")
	find := func(b *operation.Baseline) operation.RowChange {
		for _, change := range b.Changes {
			if change.Table == "users" && change.ID == 1 {
				return change
			}
		}
		t.Fatal("missing account receipt")
		return operation.RowChange{}
	}
	change := find(p.Baseline)
	if p.Accounts[0].Email == nil || change.Expected["email"] != valueDigest(*p.Accounts[0].Email) {
		t.Fatal("email receipt did not dereference account value")
	}
	p.Accounts[0].Email = nil
	b, err := CaptureBaseline(t.Context(), db, p)
	if err != nil {
		t.Fatal(err)
	}
	if find(b).Expected["email"] != valueDigest(nil) {
		t.Fatal("nullable email receipt is not SQL NULL")
	}
	data, _ := json.Marshal(b)
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "Admin@EXAMPLE.invalid") {
		t.Fatal("baseline retained account plaintext or password hash")
	}
}

func TestBaselineHistoryMetadataPresenceAndTimestamps(t *testing.T) {
	for raw, want := range map[string]int64{`{"project_id":1.0}`: 1, `{"project_id":1e0}`: 1, `{"project_id":1.5}`: 0, `{"project_id":9007199254740992}`: 0} {
		if got := baselineMetadataProjectID(raw); got != want {
			t.Fatalf("historical project metadata %s: got %d want %d", raw, got, want)
		}
	}
	if baselineValue("2026-10-24T01:00:00Z") == baselineValue("2026-10-24T02:00:00+01:00") {
		t.Fatal("business text that resembles timestamps was canonicalized")
	}
	db, _, _ := releaseFixture(t, "serve")
	for _, metadata := range []string{`{}`, `{"project_id":null}`, `{"project_id":0}`, `{"project_id":"bad"}`} {
		row := map[string]any{"action": "quick_translate", "resource_type": "quick_translate", "user_activity_logs": int64(1), "metadata": metadata, "updated_at": "historical-time"}
		values, err := expectedActivityVisibility(t.Context(), db, "sqlite", row, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		want := "unknown"
		if metadata == `{}` {
			want = "personal"
		}
		if values["visibility_scope"] != want {
			t.Fatalf("metadata %s: %v", metadata, values)
		}
		if _, exists := values["updated_at"]; exists {
			t.Fatal("history receipt normalizes updated_at")
		}
	}
	usage, err := expectedUsageVisibility(t.Context(), db, "sqlite", map[string]any{"source": "unknown"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := usage["updated_at"]; exists {
		t.Fatal("usage receipt normalizes updated_at")
	}
}

func TestBusinessFingerprintExcludesOnlyOperationMetadata(t *testing.T) {
	db := baselineTestDB(t)
	for _, ddl := range []string{
		"CREATE TABLE business(id INTEGER PRIMARY KEY AUTOINCREMENT,value TEXT NOT NULL)",
		"CREATE TABLE linguaflow_auth_format(id INTEGER PRIMARY KEY,version INTEGER NOT NULL)",
		"CREATE TABLE linguaflow_migration_state(id INTEGER PRIMARY KEY,state TEXT)",
		"CREATE TABLE linguaflow_migration_steps(operation_id TEXT,step TEXT,result_digest TEXT,PRIMARY KEY(operation_id,step))",
		"INSERT INTO business(value) VALUES('original')",
		"INSERT INTO linguaflow_auth_format VALUES(1,1)",
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := func() string {
		value, err := BusinessFingerprint(t.Context(), db, "sqlite")
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := fingerprint()
	if !operation.ValidHex(before, 64) {
		t.Fatal("invalid result digest")
	}
	for _, query := range []string{
		"INSERT INTO linguaflow_migration_state VALUES(1,'ready')",
		"INSERT INTO linguaflow_migration_steps VALUES('operation','ready','self-reference')",
		"CREATE INDEX migration_receipt_index ON linguaflow_migration_steps(result_digest)",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if fingerprint() != before {
		t.Fatal("operation receipt rows or descriptors entered result digest")
	}
	if _, err := db.Exec("CREATE INDEX business_value ON business(value)"); err != nil {
		t.Fatal(err)
	}
	if fingerprint() == before {
		t.Fatal("business schema change was omitted")
	}
	if _, err := db.Exec("DROP INDEX business_value"); err != nil {
		t.Fatal(err)
	}
	if fingerprint() != before {
		t.Fatal("schema fingerprint is not deterministic")
	}
	if _, err := db.Exec("UPDATE sqlite_sequence SET seq=120 WHERE name='business'"); err != nil {
		t.Fatal(err)
	}
	if fingerprint() == before {
		t.Fatal("allocation high-water change was omitted")
	}
	if _, err := db.Exec("UPDATE sqlite_sequence SET seq=1 WHERE name='business'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE linguaflow_auth_format SET version=2"); err != nil {
		t.Fatal(err)
	}
	if fingerprint() == before {
		t.Fatal("auth marker change was omitted")
	}
}

func TestBaselineReadyTamperRollsBackAndBindsResult(t *testing.T) {
	ctx := context.Background()
	db, _, original := releaseFixture(t, "serve")
	p := fixturePlan(t, db, "serve")
	if err := operation.Initialize(ctx, db, "sqlite", p); err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(context.Context, *sql.DB, *operation.Plan) error{ConvertSchema, ConvertAccounts, ConvertHistory} {
		if err := step(ctx, db, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := operation.Step(ctx, db, "sqlite", p, "storage", func(c *ent.Client) error { return NormalizeBaselineTimestamps(ctx, c, p) }); err != nil {
		t.Fatal(err)
	}
	if err := Complete(ctx, db, p, func(c *ent.Client) error {
		_, err := c.AuthConnection.Create().SetID("undeclared-oidc").SetKind(authconnection.KindOidc).SetState(authconnection.StateConfiguring).Save(ctx)
		return err
	}); err == nil {
		t.Fatal("ready accepted an undeclared authentication connection")
	}
	err := Complete(ctx, db, p, func(c *ent.Client) error {
		_, err := c.ExecContext(ctx, "UPDATE users SET password_hash='tampered' WHERE id=1")
		return err
	})
	if err == nil {
		t.Fatal("ready accepted a tampered source password hash")
	}
	var password string
	var version int
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id=1").Scan(&password); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT data_version FROM instance_initializations WHERE id=1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if password != original || version != 2 {
		t.Fatal("failed ready transaction escaped rollback")
	}
	if err := Complete(ctx, db, p, func(*ent.Client) error { return nil }); err != nil {
		t.Fatal(err)
	}
	result, err := operation.ResultDigest(ctx, db, p)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := BusinessFingerprint(ctx, db, "sqlite")
	if err != nil || result != actual {
		t.Fatal("transaction-local result differs after its receipt commits", err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE users SET username='undeclared' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err == nil {
		t.Fatal("ready baseline accepted undeclared account metadata")
	}
}

func TestBaselineCurrentStoragePreservesSessionsAndReconcilesClaims(t *testing.T) {
	ctx := t.Context()
	db := baselineTestDB(t)
	c := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB("sqlite3", db))))
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := c.User.Create().SetUsername("existing-owner").SetPasswordHash("private-password-hash").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AuthConnection.Create().SetID("password").SetKind(authconnection.KindPassword).Save(ctx); err != nil {
		t.Fatal(err)
	}
	session, err := c.RefreshSession.Create().SetUserID(user.ID).SetAuthVersion(1).SetConnectionID("password").SetConnectionGeneration(1).SetLoginPolicyRevision(1).SetExpiresAt(time.Now().Add(time.Hour)).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := c.RefreshToken.Create().SetUserID(user.ID).SetSessionID(session.ID).SetTokenHash("private-token-hash").SetGeneration(0).SetExpiresAt(session.ExpiresAt).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := c.StorageConnection.Create().SetName("remote").SetBackendID("remote").SetDriver(storageconnection.DriverS3).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	space, err := c.StorageSpace.Create().SetConnectionID(connection.ID).SetName("remote-space").SetIdentity("space-identity").SetMarkerNonce("space-marker").SetBucket("bucket").SetPrefix("prefix").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := &operation.Plan{Version: operation.Version, ID: strings.Repeat("b", 32), Kind: "storage", Source: operation.Endpoint{Driver: "sqlite"}}
	project, err := c.Project.Create().SetName("existing-project").SetOwnerUserID(user.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := c.Resource.Create().SetProjectID(project.ID).SetPath("source.txt").SetStoragePath("existing-source.txt").SetFormat("txt").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	object, err := c.Blob.Create().SetIdentity("existing-source").SetProjectID(project.ID).SetOwnerID(user.ID).SetPurpose("source").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := c.SourceRevision.Create().SetResourceID(resource.ID).SetProjectID(project.ID).SetSourceBlobID(object.ID).SetFormat("txt").SetParserVersion("existing-parser").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.StorageSpace.Create().SetConnectionID(connection.ID).SetName("existing-claim").SetIdentity("other-identity").SetMarkerNonce("other-marker").SetBucket(space.Bucket).SetPrefix("other-prefix").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := c.StoragePathClaim.Create().SetSpaceID(other.ID).SetIdentity(other.Identity).SetBucket(other.Bucket).SetPrefix(other.Prefix).SetExecutionGeneration(other.ExecutionGeneration).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := CaptureBaseline(ctx, db, p)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(b)
	if strings.Contains(string(data), "private-token-hash") || strings.Contains(string(data), "private-password-hash") || strings.Contains(string(data), "space-identity") {
		t.Fatal("storage baseline retained secret or business values")
	}
	if _, err := VerifyBaseline(ctx, db, p, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StoragePathClaim.Create().SetSpaceID(space.ID).SetIdentity(space.Identity).SetBucket(space.Bucket).SetPrefix(space.Prefix).SetExecutionGeneration(space.ExecutionGeneration).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO storage_namespaces(bucket) VALUES(?)", space.Bucket); err != nil {
		t.Fatal(err)
	}
	if err := c.StoragePathClaim.UpdateOneID(claim.ID).SetExecutionGeneration(other.ExecutionGeneration).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := NormalizeBaselineTimestamps(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBaselineClient(ctx, c, p, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE refresh_tokens SET token_hash='tampered-token' WHERE id=?", token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err == nil {
		t.Fatal("storage baseline allowed a changed existing session token")
	}
	if _, err := db.Exec("UPDATE refresh_tokens SET token_hash='private-token-hash' WHERE id=?", token.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE source_revisions SET parser_version='tampered-parser' WHERE id=?", revision.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err == nil {
		t.Fatal("storage baseline skipped existing source revision DTOs")
	}
	if _, err := db.Exec("UPDATE source_revisions SET parser_version='existing-parser' WHERE id=?", revision.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO storage_namespaces(bucket) VALUES('unrelated-bucket')"); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err == nil {
		t.Fatal("storage baseline allowed an undeclared namespace")
	}
}

func TestBaselineStorageInputReceiptRejectsTampering(t *testing.T) {
	ctx := t.Context()
	options, _, _, _ := newSQLiteRollbackFixture(t)
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
		t.Fatal(err)
	}
	p, j, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := operation.LoadBaseline(options.Directory, p, j); err != nil {
		t.Fatal(err)
	}
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := baselineStorageManifest(p)
	if err != nil || m == nil {
		t.Fatal("missing storage manifest", err)
	}
	var original string
	if err := db.QueryRow("SELECT input FROM storage_tasks WHERE operation_id=?", m.OperationID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBaseline(ctx, db, p, true); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generation_protocol", "legacy_root", "legacy_space_id"} {
		t.Run(key, func(t *testing.T) {
			var input map[string]any
			decoder := json.NewDecoder(strings.NewReader(original))
			decoder.UseNumber()
			if err := decoder.Decode(&input); err != nil {
				t.Fatal(err)
			}
			if int64Value(input["generation_protocol"]) != 2 {
				t.Fatal("UseNumber lost the durable protocol version")
			}
			if key == "legacy_root" {
				input[key] = filepath.Join(t.TempDir(), "wrong-root")
			} else {
				input[key] = json.Number("0")
			}
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE storage_tasks SET input=? WHERE operation_id=?", string(data), m.OperationID); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyBaseline(ctx, db, p, true); err == nil {
				t.Fatal("accepted a tampered storage input receipt")
			}
			if _, err := db.Exec("UPDATE storage_tasks SET input=? WHERE operation_id=?", original, m.OperationID); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyBaseline(ctx, db, p, true); err != nil {
				t.Fatal("restored original receipt rejected", err)
			}
		})
	}
}
