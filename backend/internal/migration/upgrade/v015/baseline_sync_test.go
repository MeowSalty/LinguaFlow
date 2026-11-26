package v015

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func requireSyncIntegerError(t *testing.T, err error, field string) {
	t.Helper()
	var classified *operation.Error
	if !errors.As(err, &classified) || classified.Code != operation.UnsupportedSource || !strings.Contains(classified.Next, "sync_tasks."+field) {
		t.Fatalf("invalid %s: got %v, want %s with field context", field, err, operation.UnsupportedSource)
	}
}

func TestParseSyncTaskInt(t *testing.T) {
	for _, value := range []int{math.MinInt, 0, math.MaxInt} {
		got, err := parseSyncTaskInt(value, "total_segments")
		if err != nil || got != value {
			t.Fatalf("int %d: got %d, %v", value, got, err)
		}
	}
	for _, value := range []int64{math.MinInt64, math.MinInt32 - 1, math.MinInt32, -1, 0, math.MaxInt32, math.MaxInt32 + 1, math.MaxInt64} {
		text := strconv.FormatInt(value, 10)
		for _, form := range []struct {
			name  string
			value any
		}{
			{"int64", value}, {"json-number", json.Number(text)}, {"string", text}, {"bytes", []byte(text)},
		} {
			t.Run(form.name+"/"+text, func(t *testing.T) {
				got, err := parseSyncTaskInt(form.value, "total_segments")
				// Compare in int64 so this test never narrows an out-of-range
				// source value before the parser sees it on 32-bit platforms.
				if value < int64(math.MinInt) || value > int64(math.MaxInt) {
					requireSyncIntegerError(t, err, "total_segments")
					return
				}
				if err != nil || int64(got) != value {
					t.Fatalf("got %d, %v; want %d", got, err, value)
				}
			})
		}
	}
	below := new(big.Int).Sub(big.NewInt(int64(math.MinInt)), big.NewInt(1)).String()
	above := new(big.Int).Add(big.NewInt(int64(math.MaxInt)), big.NewInt(1)).String()
	for _, text := range []string{"", "1.5", "1e2", "invalid", " 1", below, above, "-9223372036854775809", "9223372036854775808"} {
		for _, form := range []struct {
			name  string
			value any
		}{
			{"json-number", json.Number(text)}, {"string", text}, {"bytes", []byte(text)},
		} {
			t.Run("invalid/"+form.name+"/"+text, func(t *testing.T) {
				_, err := parseSyncTaskInt(form.value, "checkpoint_version")
				requireSyncIntegerError(t, err, "checkpoint_version")
			})
		}
	}
	for _, value := range []any{nil, []byte(nil), float64(1), true, uint64(1)} {
		_, err := parseSyncTaskInt(value, "processed_segments")
		requireSyncIntegerError(t, err, "processed_segments")
	}
}

func syncBaselineRow() map[string]any {
	return map[string]any{
		"status": "running", "segment_ids": "[1,2]", "resource_ids": "[1]",
		"total_segments": int64(2), "processed_segments": int64(1), "checkpoint_version": int64(1), "next_segment_index": int64(1),
		"result": `{"total_updated":1,"total_skipped":0,"resources":[{"resource_id":1,"resource_path":"sync.txt","updated_count":1,"skipped_count":0}]}`,
	}
}

func TestBaselineSyncTaskIntegerFields(t *testing.T) {
	fields := []string{"total_segments", "processed_segments", "checkpoint_version", "next_segment_index"}
	p := &operation.Plan{Kind: "upgrade", Source: operation.Endpoint{Driver: "sqlite"}}
	for _, field := range fields {
		for _, kind := range []string{"invalid", "nil", "missing"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				row := syncBaselineRow()
				switch kind {
				case "invalid":
					row[field] = "invalid"
				case "nil":
					row[field] = nil
				case "missing":
					delete(row, field)
				}
				values, err := expectedMutableValues(t.Context(), nil, p, "sync_tasks", 17, row, time.Now())
				requireSyncIntegerError(t, err, field)
				if values != nil || !strings.Contains(err.Error(), "sync task 17:") {
					t.Fatal("invalid integer produced mutable values or lost task context", values, err)
				}
			})
		}
	}
	// Leave later fields invalid after repairing each earlier field. The
	// first error must identify the next field before any recovery decision.
	row := syncBaselineRow()
	for _, field := range fields {
		row[field] = "invalid"
	}
	for _, field := range fields {
		values, err := expectedMutableValues(t.Context(), nil, p, "sync_tasks", 17, row, time.Now())
		requireSyncIntegerError(t, err, field)
		if values != nil {
			t.Fatal("partial checkpoint produced mutable values")
		}
		row[field] = syncBaselineRow()[field]
	}
}

func TestBaselineSyncTaskRecoveryPolicy(t *testing.T) {
	p := &operation.Plan{Kind: "upgrade", Source: operation.Endpoint{Driver: "sqlite"}}
	at := time.Date(2026, 11, 26, 1, 2, 3, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		failed bool
	}{
		{"trusted", func(map[string]any) {}, false},
		{"pristine-pending", func(row map[string]any) {
			row["status"], row["processed_segments"], row["checkpoint_version"], row["next_segment_index"] = "pending", int64(0), int64(0), int64(0)
			row["result"], row["resource_ids"] = nil, "invalid"
		}, false},
		{"invalid-total", func(row map[string]any) { row["total_segments"] = int64(-1) }, true},
		{"invalid-processed", func(row map[string]any) { row["processed_segments"] = int64(0) }, true},
		{"invalid-version", func(row map[string]any) { row["checkpoint_version"] = int64(2) }, true},
		{"invalid-next", func(row map[string]any) { row["next_segment_index"] = int64(-1) }, true},
		{"duplicate-plan", func(row map[string]any) { row["segment_ids"] = "[1,1]" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := syncBaselineRow()
			tc.change(row)
			values, err := expectedMutableValues(t.Context(), nil, p, "sync_tasks", 17, row, at)
			if err != nil {
				t.Fatal(err)
			}
			if tc.failed {
				if values["status"] != "failed" || values["error"] == nil || values["finished_at"] != sqliteConversionTime("sqlite", at) {
					t.Fatal("untrusted checkpoint did not declare failed recovery", values)
				}
				if _, changed := values["result"]; changed {
					t.Fatal("failed recovery rewrote committed history")
				}
			} else if values["checkpoint_version"] != int64(2) || values["result"] != "" || values["status"] != nil {
				t.Fatal("trusted checkpoint lost recovery policy", values)
			}
		})
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			row := syncBaselineRow()
			row["status"], row["total_segments"], row["next_segment_index"] = status, int64(-1), int64(math.MaxInt)
			values, err := expectedMutableValues(t.Context(), nil, p, "sync_tasks", 17, row, at)
			if err != nil || len(values) != 2 {
				t.Fatal("terminal history entered recovery policy", values, err)
			}
			for _, field := range []string{"segment_ids", "resource_ids"} {
				if value, removed := values[field]; !removed || value != nil {
					t.Fatal("terminal vector conversion declaration lost", values)
				}
			}
		})
	}
}

func TestBaselineSyncTaskInvalidSQLiteIntegers(t *testing.T) {
	db, _, _ := releaseFixture(t, "serve")
	for _, query := range []string{
		`INSERT INTO projects(id,created_at,updated_at,name,config,owner_user_id) SELECT 1,created_at,updated_at,'sync fixture','{}',1 FROM users WHERE id=1`,
		`INSERT INTO glossary_entries(id,created_at,updated_at,source_key,source,target,project_id) SELECT 1,created_at,updated_at,'fixture','fixture','old',1 FROM users WHERE id=1`,
		`INSERT INTO sync_tasks(id,created_at,updated_at,old_target,new_target,total_segments,processed_segments,checkpoint_version,next_segment_index,status,segment_ids,resource_ids,project_id,entry_id,actor_user_id) SELECT 1,created_at,updated_at,'old','new',2,0,0,0,'pending','[1,2]','[]',1,1,1 FROM users WHERE id=1`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	p := fixturePlan(t, db, "serve")
	p.Baseline = nil
	// Binding bytes preserves the oversized decimal as BLOB in the released
	// INTEGER column instead of SQLite coercing numeric text into REAL.
	invalid := []byte(new(big.Int).Add(big.NewInt(int64(math.MaxInt)), big.NewInt(1)).String())
	for _, field := range []string{"total_segments", "processed_segments", "checkpoint_version", "next_segment_index"} {
		t.Run(field, func(t *testing.T) {
			query := "UPDATE sync_tasks SET " + quoteIdentifier(field) + "=$1 WHERE id=1"
			if _, err := db.ExecContext(t.Context(), query, invalid); err != nil {
				t.Fatal(err)
			}
			defer func() {
				original := int64(0)
				if field == "total_segments" {
					original = 2
				}
				if _, err := db.ExecContext(t.Context(), query, original); err != nil {
					t.Error("restore isolated fixture", err)
				}
			}()
			directory := t.TempDir()
			journal := new(operation.Journal)
			err := PrepareBaseline(t.Context(), db, p, journal, directory, false)
			requireSyncIntegerError(t, err, field)
			if p.Baseline != nil || journal.BaselineDigest != "" {
				t.Fatal("invalid source bound partial baseline evidence")
			}
			if _, err := os.Stat(filepath.Join(directory, "baseline.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid source wrote baseline.json", err)
			}
			var raw any
			var storageType, status string
			if err := db.QueryRowContext(t.Context(), "SELECT "+quoteIdentifier(field)+",typeof("+quoteIdentifier(field)+"),status FROM sync_tasks WHERE id=1").Scan(&raw, &storageType, &status); err != nil {
				t.Fatal(err)
			}
			stored, ok := raw.([]byte)
			if !ok || storageType != "blob" || !bytes.Equal(stored, invalid) || status != "pending" {
				t.Fatal("baseline rejection rewrote the source", raw, storageType, status)
			}
		})
	}
}
