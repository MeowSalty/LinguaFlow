package database

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// TestClassifyPostgresStructural 验证有代表性的结构性 SQLSTATE
// 映射到 CategoryStructural，并在 Result.SQLState 中暴露错误码。
func TestClassifyPostgresStructural(t *testing.T) {
	t.Parallel()
	codes := []string{
		"42601", // syntax_error — the root cause of the original PG failure
		"42703", // undefined_column
		"42P01", // undefined_table
		"23505", // unique_violation
		"23502", // not_null_violation
		"23514", // check_violation
		"22P02", // invalid_text_representation
		"42804", // datatype_mismatch
		"42P07", // duplicate_table
		"42701", // duplicate_column
	}
	for _, code := range codes {
		err := &pgconn.PgError{Code: code, Message: "structural"}
		got := Classify(config.DatabaseDriverPostgres, err)
		if got.Category != CategoryStructural {
			t.Errorf("code %s: category=%s, want structural", code, got.Category)
		}
		if got.SQLState != code {
			t.Errorf("code %s: sqlstate=%q, want %q", code, got.SQLState, code)
		}

	}
}

// TestClassifyPostgresTransient 验证有代表性的瞬时 SQLSTATE
// 映射到 CategoryTransient。
func TestClassifyPostgresTransient(t *testing.T) {
	t.Parallel()
	codes := []string{
		"40001", // serialization_failure
		"40P01", // deadlock_detected
		"40003", // connection_failure
		"08000", // connection_exception
		"08001", // sqlclient_unable_to_establish_sqlconnection
		"08003", // connection_does_not_exist
		"08004", // sqlserver_rejected_establishment_of_sqlconnection
		"08006", // connection_failure
		"08007", // transaction_resolution_unknown
		"57P03", // cannot_connect_now
		"53300", // too_many_connections
		"53000", // insufficient_resources
		"53400", // configuration_limit_exceeded
		"53200", // out_of_memory
		"55006", // object_in_use
		"55P03", // lock_not_available
	}
	for _, code := range codes {
		err := &pgconn.PgError{Code: code, Message: "transient"}
		got := Classify(config.DatabaseDriverPostgres, err)
		if got.Category != CategoryTransient {
			t.Errorf("code %s: category=%s, want transient", code, got.Category)
		}
		if got.SQLState != code {
			t.Errorf("code %s: sqlstate=%q, want %q", code, got.SQLState, code)
		}
	}
}

// TestClassifyPostgresUnknownSQLState 验证未识别的 SQLSTATE 回退到 CategoryUnknown。
func TestClassifyPostgresUnknownSQLState(t *testing.T) {
	t.Parallel()
	err := &pgconn.PgError{Code: "99999", Message: "unknown"}
	got := Classify(config.DatabaseDriverPostgres, err)
	if got.Category != CategoryUnknown {
		t.Errorf("code 99999: category=%s, want unknown", got.Category)
	}
	if got.SQLState != "99999" {
		t.Errorf("code 99999: sqlstate=%q, want 99999", got.SQLState)
	}
}

// TestClassifyPostgresContextAndDeadline 验证 context 错误不会被归类为瞬时
// （调用方应自行处理 cancel）。
func TestClassifyPostgresContextAndDeadline(t *testing.T) {
	t.Parallel()
	ctxC, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineErr := context.DeadlineExceeded

	cases := []struct {
		name string
		err  error
	}{
		{"context.Canceled", ctxC.Err()},
		{"context.DeadlineExceeded", deadlineErr},
	}
	for _, tc := range cases {
		got := Classify(config.DatabaseDriverPostgres, tc.err)
		if got.Category != CategoryUnknown {
			t.Errorf("%s: category=%s, want unknown (caller must pre-check cancel)",
				tc.name, got.Category)
		}
	}
}

// TestClassifyPostgresConnectionErrors 验证非 PgError 的连接类错误被归类为瞬时。
func TestClassifyPostgresConnectionErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{"net.OpError", &net.OpError{Op: "dial", Net: "tcp",
			Err: errors.New("connect: connection refused")}},
		{"io.EOF", io.EOF},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		got := Classify(config.DatabaseDriverPostgres, tc.err)
		if got.Category != CategoryTransient {
			t.Errorf("%s: category=%s, want transient", tc.name, got.Category)
		}
		if got.SQLState != "" {
			t.Errorf("%s: sqlstate=%q, want empty", tc.name, got.SQLState)
		}
	}
}

// TestClassifyPostgresUnknownError 验证一般非 PgError 非连接错误回退到 CategoryUnknown。
func TestClassifyPostgresUnknownError(t *testing.T) {
	t.Parallel()
	err := errors.New("something unexpected")
	got := Classify(config.DatabaseDriverPostgres, err)
	if got.Category != CategoryUnknown {
		t.Errorf("generic err: category=%s, want unknown", got.Category)
	}
}

// TestClassifySQLiteTransient 验证 SQLite busy/locked 字符串映射到瞬时。
func TestClassifySQLiteTransient(t *testing.T) {
	t.Parallel()
	msgs := []string{
		"SQLITE_BUSY: database is locked",
		"SQLITE_BUSY:-steal: ...",
		"SQLITE_LOCKED: ...",
		"database is locked",
		"database table is locked",
	}
	for _, msg := range msgs {
		err := errors.New(msg)
		got := Classify(config.DatabaseDriverSQLite, err)
		if got.Category != CategoryTransient {
			t.Errorf("msg %q: category=%s, want transient", msg, got.Category)
		}
		if got.SQLState != "" {
			t.Errorf("msg %q: sqlstate=%q, want empty", msg, got.SQLState)
		}
	}
}

// TestClassifySQLiteUnknown 验证未知 SQLite 错误回退到 CategoryUnknown（保守策略 → 快速失败）。
func TestClassifySQLiteUnknown(t *testing.T) {
	t.Parallel()
	msgs := []string{
		"constraint failed: UNIQUE constraint failed",
		"SQLITE_ERROR: no such table",
		"some other random error",
	}
	for _, msg := range msgs {
		err := errors.New(msg)
		got := Classify(config.DatabaseDriverSQLite, err)
		if got.Category != CategoryUnknown {
			t.Errorf("msg %q: category=%s, want unknown", msg, got.Category)
		}
	}
}

// TestClassifySQLiteCaseInsensitive 验证字符串匹配不区分大小写。
func TestClassifySQLiteCaseInsensitive(t *testing.T) {
	t.Parallel()
	err := errors.New("SQLITE_BUSY vs sqlite_busy")
	if Classify(config.DatabaseDriverSQLite, err).Category != CategoryTransient {
		t.Error("uppercase match expected transient")
	}
}

// TestClassifyUnknownDriver 验证未识别的驱动对于所有错误都返回 CategoryUnknown（保守策略）。
func TestClassifyUnknownDriver(t *testing.T) {
	t.Parallel()
	transientPg := &pgconn.PgError{Code: "40P01", Message: "deadlock"}
	structuralPg := &pgconn.PgError{Code: "42601", Message: "syntax"}
	sqliteBusy := errors.New("SQLITE_BUSY")
	for _, err := range []error{transientPg, structuralPg, sqliteBusy} {
		got := Classify("mysql", err)
		if got.Category != CategoryUnknown {
			t.Errorf("unknown driver for %v: category=%s, want unknown", err, got.Category)
		}
	}
}

// TestClassifyNilError 验证对 nil 错误做了防御处理。
func TestClassifyNilError(t *testing.T) {
	t.Parallel()
	for _, driver := range []string{
		config.DatabaseDriverPostgres,
		config.DatabaseDriverSQLite,
		"mysql",
	} {
		got := Classify(driver, nil)
		if got.Category != CategoryUnknown {
			t.Errorf("driver %s nil err: category=%s, want unknown", driver, got.Category)
		}
	}
}

// TestCategoryString 验证 String() 方法便于日志可读性。
func TestCategoryString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c    Category
		want string
	}{
		{CategoryStructural, "structural"},
		{CategoryTransient, "transient"},
		{CategoryUnknown, "unknown"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("Category(%d).String()=%q, want %q", tc.c, got, tc.want)
		}
	}
}
