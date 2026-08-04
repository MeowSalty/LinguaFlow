// Package database 提供数据库连接设置与错误分类。
//
// 本文件实现写入错误分类工具包（方案 B），按驱动（Postgres/SQLite）将
// UPDATE 失败的错误分为结构性（不可恢复，命中即 fail-fast）与瞬时
// （可重试，跳过该段待下一轮 pending 过滤拾取）。
package database

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// Category 表示写入错误的性质分级。
type Category int

const (
	// CategoryStructural 不可恢复：SQL 42601/42703/42P01/23505... 命中即 fail-fast。
	CategoryStructural Category = iota
	// CategoryTransient 可重试：deadlock/lock/timeout/connection 类，跳过该段待重试。
	CategoryTransient
	// CategoryUnknown 未知错误，保守归结构性（fail-fast 宁可误停不可误静默）。
	CategoryUnknown
)

// String 返回 Category 的可读名称，用于日志。
func (c Category) String() string {
	switch c {
	case CategoryStructural:
		return "structural"
	case CategoryTransient:
		return "transient"
	default:
		return "unknown"
	}
}

// Result 是错误分类的结果。
type Result struct {
	Category Category
	// SQLState 是 Postgres 的 SQLSTATE（5 位）；SQLite 为空字符串。
	SQLState string
}

// Classify 按 driver 对写入错误分类。
//
// 对于 context.Canceled / context.DeadlineExceeded，返回 CategoryUnknown——
// 调用方应在调用 Classify 前自行判 `errors.Is(err, context.Canceled)` 等，
// 让 round_executor 的 ctx 检查接管取消流程，而非当作瞬时错误重试。
//
// 驱动未知时，所有 err 归 CategoryUnknown（保守）。
func Classify(driver string, err error) Result {
	if err == nil {
		return Result{Category: CategoryUnknown}
	}

	switch driver {
	case config.DatabaseDriverPostgres:
		return classifyPostgres(err)
	case config.DatabaseDriverSQLite:
		return classifySQLite(err)
	default:
		return Result{Category: CategoryUnknown}
	}
}

// classifyPostgres 按 SQLSTATE 与错误类型分类 Postgres 写入错误。
func classifyPostgres(err error) Result {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return Result{
			Category: postgresCategoryByCode(pgErr.Code),
			SQLState: pgErr.Code,
		}
	}

	// 非 PgError：连接/网络类。context.Canceled/DeadlineExceeded 单独处理，
	// 这里归 Unknown（调用方已有 cancel 分支），其余连接类错归瞬时。
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return Result{Category: CategoryUnknown}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return Result{Category: CategoryTransient}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return Result{Category: CategoryTransient}
	}
	// pgconn.Timeout 归瞬时（连接超时可重试）。
	if pgconn.Timeout(err) {
		return Result{Category: CategoryTransient}
	}
	return Result{Category: CategoryUnknown}
}

// postgresCategoryByCode 按五位 SQLSTATE 分类。
func postgresCategoryByCode(code string) Category {
	switch code {
	// 瞬时类（可重试）
	case "40001", // serialization_failure
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
		"53200", // out_of_memory（通常可重试）
		"55006", // object_in_use
		"55P03": // lock_not_available（LOCK NOWAIT / lock_timeout）
		return CategoryTransient

	// 结构性类（不可恢复，fail-fast）
	case "42601", // syntax_error
		"42703", // undefined_column
		"42P01", // undefined_table
		"23505", // unique_violation
		"23502", // not_null_violation
		"23514", // check_violation
		"22P02", // invalid_text_representation
		"42804", // datatype_mismatch
		"42P07", // duplicate_table
		"42701": // duplicate_column
		return CategoryStructural

	default:
		// 其余 SQLSTATE 保守归 Unknown
		return CategoryUnknown
	}
}

// classifySQLite 按字符串匹配分类 SQLite（modernc.org/sqlite 无结构化错误码）。
func classifySQLite(err error) Result {
	msg := strings.ToLower(err.Error())
	switch {
	case containsSQLiteTransient(msg):
		return Result{Category: CategoryTransient}
	default:
		// 其余保守归 Unknown（→ 结构性 → fail-fast）
		return Result{Category: CategoryUnknown}
	}
}

// containsSQLiteTransient 判断错误信息是否为 SQLite 瞬时类（BUSY/LOCKED）。
func containsSQLiteTransient(msg string) bool {
	// modernc.org/sqlite 错误形如 "SQLITE_BUSY: ..." 或 "database is locked"
	return strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}
