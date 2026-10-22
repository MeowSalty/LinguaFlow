package database

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestSQLiteOpenCancelsWhileFormatProbeIsLocked(t *testing.T) {
	for _, test := range []struct {
		name, query string
	}{
		{"default DSN", ""},
		{"pragma timeout", "?_pragma=busy_timeout(5000)"},
		{"timeout aliases", "?_busy_timeout=5000&_timeout=8000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, release := holdSQLiteExclusiveFileLock(t)
			if test.query == "" {
				cfg.Database.DSN = ""
			} else {
				cfg.Database.DSN = cfg.DatabasePath() + test.query
			}
			before, err := os.ReadFile(cfg.DatabasePath())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, client, err := Open(ctx, cfg)
				if client != nil {
					err = errors.Join(err, client.Close())
				}
				result <- err
			}()
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("locked startup must wait for cancellation: %v", err)
				}
				// 机器繁忙时，Open 可能尚未获调度，上下文就已超时。
				// 若已进入 SQLite，则验证错误仍保留阶段信息和锁错误码。
				var sqliteErr *sqlite.Error
				if errors.As(err, &sqliteErr) {
					if sqliteErr.Code()&0xff != 5 {
						t.Fatalf("startup error lost the SQLite lock cause: %v", err)
					}
					if !strings.Contains(err.Error(), "probe timestamp format") {
						t.Fatalf("startup error lost its probe stage: %v", err)
					}
				}
			case <-time.After(2 * time.Second):
				release()
				select {
				case <-result:
				case <-time.After(time.Second):
				}
				t.Fatal("native busy timeout prevented prompt startup cancellation")
			}
			after, err := os.ReadFile(cfg.DatabasePath())
			if err != nil || string(before) != string(after) {
				t.Fatalf("canceled format probe changed the database: %v", err)
			}
		})
	}
}

func TestSQLiteOpenWaitsForFormatProbeFileLock(t *testing.T) {
	cfg, release := holdSQLiteExclusiveFileLock(t)
	// 必须先确认数据库格式有效，才能将回滚日志模式切换为运行时默认的 WAL 配置。
	cfg.Database.DSN = ""
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type openResult struct {
		db     *sql.DB
		client *ent.Client
		err    error
	}
	result := make(chan openResult, 1)
	go func() {
		db, client, err := Open(ctx, cfg)
		result <- openResult{db, client, err}
	}()
	select {
	case opened := <-result:
		if opened.client != nil {
			opened.client.Close()
		}
		t.Fatalf("Open returned before the competing lock was released: %v", opened.err)
	case <-time.After(150 * time.Millisecond):
	}
	release()
	select {
	case opened := <-result:
		if opened.err != nil {
			t.Fatalf("Open did not recover from a transient probe lock: %v", opened.err)
		}
		defer opened.client.Close()
		assertSQLitePoolJournal(t, opened.db, "wal", 1)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestSQLiteFormatProbeHasDefaultLockDeadline(t *testing.T) {
	cfg, _ := holdSQLiteExclusiveFileLock(t)
	started := time.Now()
	probe, err := openSQLiteFormatProbe(t.Context(), cfg.DatabaseDSN(), true)
	if probe != nil {
		probe.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("format probe without a caller deadline must have its own limit: %v", err)
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 {
		t.Fatalf("probe deadline lost the underlying lock error: %v", err)
	}
	if elapsed := time.Since(started); elapsed < sqliteFormatProbeTimeout || elapsed > 2*sqliteFormatProbeTimeout {
		t.Fatalf("probe exceeded its default wait budget: %s", elapsed)
	}
}

func holdSQLiteExclusiveFileLock(t *testing.T) (*config.ServerConfig, func()) {
	t.Helper()
	cfg := sqliteFormatConfig(t)
	cfg.Database.DSN = cfg.DatabasePath() + "?_pragma=busy_timeout(5000)"
	db, client, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// 保持已初始化的数据库处于回滚日志模式；独占写锁会稳定地阻塞其他连接读取元数据。
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, rollbackErr := conn.ExecContext(ctx, "ROLLBACK")
			if err := errors.Join(rollbackErr, conn.Close()); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return cfg, release
}
