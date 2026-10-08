package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	entschema "github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
)

func TestPostgresTimeConnectionsAndPrecision(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 3, MaxIdleConns: 3}
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	connections := make([]*sql.Conn, 0, 3)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range 3 {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		var zone string
		if err := conn.QueryRowContext(ctx, "SHOW timezone").Scan(&zone); err != nil || zone != "UTC" {
			t.Fatalf("pooled connection timezone=%q error=%v", zone, err)
		}
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	connections = nil
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// A changed server/session representation must not leak into decoded instants.
	if _, err := db.ExecContext(ctx, "SET TIME ZONE 'Asia/Kathmandu'"); err != nil {
		t.Fatal(err)
	}
	driver := NewDriver(entsql.OpenDB(dialect.Postgres, db))
	tx, err := driver.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := tx.Exec(ctx, "CREATE TEMP TABLE time_codec_probe (instant timestamptz) ON COMMIT DROP", []any{}, nil); err != nil {
		t.Fatal(err)
	}
	value := time.Date(2026, 9, 29, 18, 1, 2, 123456789, time.FixedZone("offset", -7*60*60))
	if err := tx.Exec(ctx, "INSERT INTO time_codec_probe (instant) VALUES ($1)", []any{value}, nil); err != nil {
		t.Fatal(err)
	}
	var rows entsql.Rows
	if err := tx.Query(ctx, "SELECT instant, pg_typeof(instant)::text FROM time_codec_probe WHERE instant = $1", []any{value.UTC()}, &rows); err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		_ = rows.Close()
		t.Fatal("equal offset time did not match PostgreSQL microsecond storage")
	}
	var actual time.Time
	var pgType string
	err = rows.Scan(&actual, &pgType)
	_ = rows.Close()
	if err != nil || !actual.Equal(value.UTC().Truncate(time.Microsecond)) || actual.Location() != time.UTC || pgType != "timestamp with time zone" {
		t.Fatalf("PostgreSQL time=%v type=%q error=%v", actual, pgType, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresMigrationLock(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}

	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{
		Driver:       config.DatabaseDriverPostgres,
		DSN:          dsn,
		MaxOpenConns: 2,
		MaxIdleConns: 1,
	}
	dbOne, clientOne, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open first PostgreSQL connection: %v", err)
	}
	defer func() { _ = clientOne.Close() }()
	dbTwo, clientTwo, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open second PostgreSQL connection: %v", err)
	}
	defer func() { _ = clientTwo.Close() }()

	unlockOne, err := AcquireMigrationLock(context.Background(), dbOne, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatalf("acquire first migration lock: %v", err)
	}
	defer func() {
		if unlockOne != nil {
			_ = unlockOne()
		}
	}()

	blockedCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := AcquireMigrationLock(blockedCtx, dbTwo, config.DatabaseDriverPostgres); err == nil {
		t.Fatal("second migration lock should block")
	} else if !strings.Contains(err.Error(), "connection timeout") {
		t.Fatalf("blocked lock error=%q want timeout", err)
	}

	if err := unlockOne(); err != nil {
		t.Fatalf("release first migration lock: %v", err)
	}
	unlockOne = nil
	unlockTwo, err := AcquireMigrationLock(context.Background(), dbTwo, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatalf("acquire migration lock after release: %v", err)
	}
	if err := unlockTwo(); err != nil {
		t.Fatalf("release second migration lock: %v", err)
	}
}

func TestPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{
		Driver:          config.DatabaseDriverPostgres,
		DSN:             dsn,
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
	}
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer func() { _ = client.Close() }()

	var serverVersion int
	if err := db.QueryRowContext(ctx, "SHOW server_version_num").Scan(&serverVersion); err != nil {
		t.Fatalf("read PostgreSQL version: %v", err)
	}
	if serverVersion < 160000 {
		t.Fatalf("PostgreSQL version=%d want 16 or newer", serverVersion)
	}
	unlock, err := AcquireMigrationLock(ctx, db, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatalf("acquire migration lock: %v", err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		_ = unlock()
		t.Fatalf("create schema: %v", err)
	}
	if err := client.Schema.Create(ctx); err != nil {
		_ = unlock()
		t.Fatalf("create schema a second time: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("release migration lock: %v", err)
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	cleanups := make([]func(), 0, 12)
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()

	newUser := func(label string) *ent.User {
		u, err := client.User.Create().
			SetUsername("postgres-" + label + "-" + suffix).
			SetPasswordHash("integration-test-hash").
			SetEmail("postgres-" + label + "-" + suffix + "@example.invalid").
			Save(ctx)
		if err != nil {
			t.Fatalf("create user %s: %v", label, err)
		}
		cleanups = append(cleanups, func() { _ = client.User.DeleteOneID(u.ID).Exec(context.Background()) })
		return u
	}
	newOrganization := func(label string) *ent.Organization {
		org, err := client.Organization.Create().
			SetName("Postgres " + label + " " + suffix).
			SetSlug("postgres-" + label + "-" + suffix).
			Save(ctx)
		if err != nil {
			t.Fatalf("create organization %s: %v", label, err)
		}
		cleanups = append(cleanups, func() { _ = client.Organization.DeleteOneID(org.ID).Exec(context.Background()) })
		return org
	}
	trackBackend := func(b *ent.Backend) {
		cleanups = append(cleanups, func() { _ = client.Backend.DeleteOneID(b.ID).Exec(context.Background()) })
	}

	userOne := newUser("user-one")
	userTwo := newUser("user-two")
	orgOne := newOrganization("org-one")
	orgTwo := newOrganization("org-two")
	if userOne.CreatedAt.IsZero() || userOne.UpdatedAt.IsZero() {
		t.Fatal("time mixin fields were not populated")
	}

	token, err := client.RefreshToken.Create().
		SetTokenHash("postgres-token-" + suffix).
		SetExpiresAt(time.Now().Add(time.Hour).UTC()).
		SetUser(userOne).
		Save(ctx)
	if err != nil {
		t.Fatalf("create refresh token: %v", err)
	}
	cleanups = append(cleanups, func() { _ = client.RefreshToken.DeleteOneID(token.ID).Exec(context.Background()) })
	tokenUser, err := token.QueryUser().Only(ctx)
	if err != nil || tokenUser.ID != userOne.ID {
		t.Fatalf("query refresh token user: user=%v error=%v", tokenUser, err)
	}

	backendName := "shared-user-backend-" + suffix
	userBackend, err := client.Backend.Create().
		SetName(backendName).
		SetScope("user").
		SetOwnerUser(userOne).
		SetBackendType(entbackend.BackendTypeOpenai).
		SetOptions(map[string]any{"model": "integration-model", "stream": true}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user backend: %v", err)
	}
	trackBackend(userBackend)
	if userBackend.BackendType != entbackend.BackendTypeOpenai || userBackend.Options["model"] != "integration-model" {
		t.Fatalf("unexpected enum or JSON values: type=%q options=%v", userBackend.BackendType, userBackend.Options)
	}
	backendOwner, err := userBackend.QueryOwnerUser().Only(ctx)
	if err != nil || backendOwner.ID != userOne.ID {
		t.Fatalf("query backend owner: owner=%v error=%v", backendOwner, err)
	}

	_, err = client.Backend.Create().
		SetName(backendName).
		SetScope("user").
		SetOwnerUser(userOne).
		SetBackendType(entbackend.BackendTypeAnthropic).
		Save(ctx)
	if err == nil || !ent.IsConstraintError(err) {
		t.Fatalf("same user/name should violate partial unique index: %v", err)
	}
	otherUserBackend, err := client.Backend.Create().
		SetName(backendName).
		SetScope("user").
		SetOwnerUser(userTwo).
		SetBackendType(entbackend.BackendTypeGoogle).
		Save(ctx)
	if err != nil {
		t.Fatalf("same name for another user: %v", err)
	}
	trackBackend(otherUserBackend)

	orgBackendName := "shared-org-backend-" + suffix
	orgBackend, err := client.Backend.Create().
		SetName(orgBackendName).
		SetScope("org").
		SetOwnerOrg(orgOne).
		SetBackendType(entbackend.BackendTypeOpenai).
		Save(ctx)
	if err != nil {
		t.Fatalf("create organization backend: %v", err)
	}
	trackBackend(orgBackend)
	_, err = client.Backend.Create().
		SetName(orgBackendName).
		SetScope("org").
		SetOwnerOrg(orgOne).
		SetBackendType(entbackend.BackendTypeAnthropic).
		Save(ctx)
	if err == nil || !ent.IsConstraintError(err) {
		t.Fatalf("same organization/name should violate partial unique index: %v", err)
	}
	otherOrgBackend, err := client.Backend.Create().
		SetName(orgBackendName).
		SetScope("org").
		SetOwnerOrg(orgTwo).
		SetBackendType(entbackend.BackendTypeGoogle).
		Save(ctx)
	if err != nil {
		t.Fatalf("same name for another organization: %v", err)
	}
	trackBackend(otherOrgBackend)

	plan, err := client.ExecutionPlanTemplate.Create().
		SetName("postgres-plan-" + suffix).
		SetOwnerUser(userOne).
		SetRounds([]entschema.ExecutionRoundConfig{{
			Mode:      "translate",
			BackendID: userBackend.ID,
			Translate: &entschema.TranslateRoundConfig{BatchSize: 8, Concurrency: 2},
		}}).
		Save(ctx)
	if err != nil {
		t.Fatalf("create execution plan JSON: %v", err)
	}
	cleanups = append(cleanups, func() { _ = client.ExecutionPlanTemplate.DeleteOneID(plan.ID).Exec(context.Background()) })
	loadedPlan, err := client.ExecutionPlanTemplate.Get(ctx, plan.ID)
	if err != nil || len(loadedPlan.Rounds) != 1 || loadedPlan.Rounds[0].BackendID != userBackend.ID {
		t.Fatalf("read execution plan JSON: plan=%v error=%v", loadedPlan, err)
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	usage, err := tx.UsageRecord.Create().
		SetSource("postgres-integration").
		SetAPICalls(2).
		SetInputTokens(100).
		SetOutputTokens(50).
		SetUser(userOne).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("create usage record in transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit usage record: %v", err)
	}
	cleanups = append(cleanups, func() { _ = client.UsageRecord.DeleteOneID(usage.ID).Exec(context.Background()) })
	loadedUsage, err := client.UsageRecord.Get(ctx, usage.ID)
	if err != nil || loadedUsage.APICalls != 2 || loadedUsage.InputTokens != 100 || loadedUsage.OutputTokens != 50 {
		t.Fatalf("read committed usage record: usage=%v error=%v", loadedUsage, err)
	}
}
