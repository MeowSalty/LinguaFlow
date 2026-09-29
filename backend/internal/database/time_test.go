package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func newTimeTestDriver(t *testing.T) (*sql.DB, *TimeDriver) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE moments (id INTEGER PRIMARY KEY, happened_at datetime)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE INDEX moments_time ON moments (happened_at, id)"); err != nil {
		t.Fatal(err)
	}
	return db, NewDriver(entsql.OpenDB(dialect.SQLite, db))
}

func TestTimeDriverSQLiteRoundTripAndOrdering(t *testing.T) {
	db, driver := newTimeTestDriver(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 3, 0, 0, 0, time.UTC)
	values := []time.Time{
		base.Add(time.Nanosecond).In(time.FixedZone("east", 8*60*60)),
		base,
		base.Add(100 * time.Millisecond).In(time.FixedZone("west", -7*60*60)),
		base.Add(10 * time.Nanosecond),
		time.Date(2026, 11, 1, 1, 30, 0, 123456789, time.FixedZone("EDT", -4*60*60)),
		time.Date(2026, 11, 1, 1, 30, 0, 123456789, time.FixedZone("EST", -5*60*60)),
		{},
	}
	for i, value := range values {
		args := []any{i + 1, value}
		if err := driver.Exec(ctx, "INSERT INTO moments (id, happened_at) VALUES (?, ?)", args, nil); err != nil {
			t.Fatal(err)
		}
		if original, ok := args[1].(time.Time); !ok || original != value {
			t.Fatal("time encoding mutated the caller's arguments")
		}
		var stored string
		if err := db.QueryRow("SELECT CAST(happened_at AS TEXT) FROM moments WHERE id = ?", i+1).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if want := value.UTC().Format("2006-01-02T15:04:05.000000000Z"); stored != want {
			t.Fatalf("stored %q, want %q", stored, want)
		}
		var rows entsql.Rows
		if err := driver.Query(ctx, "SELECT happened_at FROM moments WHERE happened_at = ? AND id = ?", []any{value.In(time.FixedZone("other", 9*60*60)), i + 1}, &rows); err != nil {
			t.Fatal(err)
		}
		if !rows.Next() {
			_ = rows.Close()
			t.Fatal("equal instant with a different offset did not match")
		}
		var actual time.Time
		err := rows.Scan(&actual)
		_ = rows.Close()
		if err != nil || !actual.Equal(value) || actual.Location() != time.UTC {
			t.Fatalf("roundtrip=%v error=%v, want UTC instant %v", actual, err, value)
		}
	}

	var rows entsql.Rows
	if err := driver.Query(ctx, "SELECT id FROM moments WHERE happened_at >= ? AND happened_at < ? ORDER BY happened_at, id", []any{base, base.Add(time.Second)}, &rows); err != nil {
		t.Fatal(err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	err := rows.Err()
	_ = rows.Close()
	if err != nil || fmt.Sprint(ids) != "[2 1 4 3]" {
		t.Fatalf("ordered ids=%v error=%v", ids, err)
	}

	if err := driver.Query(ctx, "EXPLAIN QUERY PLAN SELECT id FROM moments WHERE happened_at >= ? AND happened_at < ? ORDER BY happened_at, id", []any{base, base.Add(time.Second)}, &rows); err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail
	}
	if err := rows.Err(); err != nil || !strings.Contains(plan, "moments_time") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("range/order should use time index: %q error=%v", plan, err)
	}
}

func TestTimeDriverSQLiteNullableAndTransactions(t *testing.T) {
	db, driver := newTimeTestDriver(t)
	ctx := context.Background()
	value := time.Date(2026, 9, 29, 13, 5, 6, 987654321, time.FixedZone("east", 8*60*60))
	var missing *time.Time
	args := []any{1, &value, 2, missing, 3, sql.NullTime{Time: value, Valid: true}, 4, sql.NullTime{}}
	if err := driver.Exec(ctx, "INSERT INTO moments (id, happened_at) VALUES (?, ?), (?, ?), (?, ?), (?, ?)", args, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 3} {
		var stored string
		if err := db.QueryRow("SELECT CAST(happened_at AS TEXT) FROM moments WHERE id = ?", id).Scan(&stored); err != nil || stored != "2026-09-29T05:05:06.987654321Z" {
			t.Fatalf("nullable time id=%d stored=%q error=%v", id, stored, err)
		}
	}
	var nulls int
	if err := db.QueryRow("SELECT COUNT(*) FROM moments WHERE happened_at IS NULL").Scan(&nulls); err != nil || nulls != 2 {
		t.Fatalf("null count=%d error=%v", nulls, err)
	}
	for i, begin := range []func() (dialect.Tx, error){
		func() (dialect.Tx, error) { return driver.Tx(ctx) },
		func() (dialect.Tx, error) {
			return driver.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		},
	} {
		tx, err := begin()
		if err != nil {
			t.Fatal(err)
		}
		id := i + 5
		if err := tx.Exec(ctx, "INSERT INTO moments (id, happened_at) VALUES (?, ?)", []any{id, value}, nil); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		var rows entsql.Rows
		if err := tx.Query(ctx, "SELECT id FROM moments WHERE id = ? AND happened_at = ?", []any{id, value.UTC()}, &rows); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		found := rows.Next()
		_ = rows.Close()
		if !found {
			_ = tx.Rollback()
			t.Fatal("transaction query did not encode time predicate")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := driver.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(ctx, "DELETE FROM moments WHERE happened_at = ?", []any{value}, nil); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM moments").Scan(&count); err != nil || count != 6 {
		t.Fatalf("rollback count=%d error=%v", count, err)
	}
}

func TestOpenSQLiteEntTimePaths(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 9, 29, 12, 0, 0, 123456789, time.FixedZone("east", 8*60*60))
	builders := make([]*ent.UserCreate, 2)
	for i := range builders {
		builders[i] = client.User.Create().SetUsername(fmt.Sprintf("time-%d", i)).SetEmail(fmt.Sprintf("time-%d@example.invalid", i)).SetPasswordHash("test").SetCreatedAt(first.Add(time.Duration(i))).SetUpdatedAt(first.Add(time.Duration(i)))
	}
	users, err := client.User.CreateBulk(builders...).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := client.User.Query().Where(user.CreatedAtGTE(first), user.CreatedAtLT(first.Add(time.Nanosecond))).Count(ctx); err != nil || count != 1 {
		t.Fatalf("ent nanosecond range count=%d error=%v", count, err)
	}
	tx, err := client.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.User.UpdateOneID(users[0].ID).SetUpdatedAt(first.Add(time.Second)).Exec(ctx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT CAST(updated_at AS TEXT) FROM users WHERE id = ?", users[0].ID).Scan(&raw); err != nil || raw != "2026-09-29T04:00:01.123456789Z" {
		t.Fatalf("ent transaction stored=%q error=%v", raw, err)
	}
	loaded, err := client.User.Get(ctx, users[0].ID)
	if err != nil || !loaded.CreatedAt.Equal(first) || loaded.CreatedAt.Location() != time.UTC {
		t.Fatalf("ent read=%v error=%v", loaded, err)
	}
}

func TestSQLiteDefaultsIgnoreServerTimezone(t *testing.T) {
	// This test must remain sequential. Restore the process timezone before
	// returning so parallel tests run with their original timezone.
	previousLocation := time.Local
	t.Cleanup(func() { time.Local = previousLocation })
	ctx := context.Background()
	cfg := sqliteFormatConfig(t)
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	var previous time.Time
	for i, offset := range []int{8 * 60 * 60, -7 * 60 * 60} {
		time.Local = time.FixedZone("server", offset)
		u, err := client.User.Create().SetUsername(fmt.Sprintf("zone-%d", i)).SetEmail(fmt.Sprintf("zone-%d@example.invalid", i)).SetPasswordHash("test").Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if u.CreatedAt.Location() != time.UTC || u.UpdatedAt.Location() != time.UTC || u.CreatedAt.Before(previous) {
			t.Fatalf("default timestamps depend on server timezone: created=%v updated=%v previous=%v", u.CreatedAt, u.UpdatedAt, previous)
		}
		previous = u.CreatedAt
		var raw string
		if err := db.QueryRow("SELECT CAST(created_at AS TEXT) FROM users WHERE id = ?", u.ID).Scan(&raw); err != nil || raw != u.CreatedAt.Format("2006-01-02T15:04:05.000000000Z") {
			t.Fatalf("default raw timestamp=%q error=%v", raw, err)
		}
		updated, err := client.User.UpdateOneID(u.ID).SetDisplayName("updated").Save(ctx)
		if err != nil || updated.UpdatedAt.Location() != time.UTC || updated.UpdatedAt.Before(u.CreatedAt) {
			t.Fatalf("update timestamp=%v error=%v", updated, err)
		}
	}
}
